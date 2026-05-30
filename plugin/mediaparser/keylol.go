package mediaparser

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const keylolUA = "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 Mobile"

var (
	keylolAPIBase  = "https://keylol.com/api/mobile/index.php"
	steamAPIBase   = "https://store.steampowered.com/api/appdetails"
	keylolImageExt = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|webp|gif)(?:\?|$)`)
)

type keylolBlock struct {
	Kind  string
	Text  string
	URL   string
	Title string
	Desc  string
	Cover string
}

func parseKeylol(cfg config, raw string) (mediaMeta, error) {
	tid := keylolThreadID(raw)
	if tid == "" {
		return mediaMeta{}, fmt.Errorf("keylol thread id not found")
	}
	api, err := url.Parse(keylolAPIBase)
	if err != nil {
		return mediaMeta{}, err
	}
	q := api.Query()
	q.Set("module", "viewthread")
	q.Set("tid", tid)
	q.Set("page", "1")
	q.Set("version", "5")
	api.RawQuery = q.Encode()

	headers := keylolHeaders(cfg, raw)
	body, finalURL, status, err := fetchText(api.String(), headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("keylol API HTTP %d: %s", status, truncate(body, 180))
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return mediaMeta{}, err
	}
	vars := getMap(data, "Variables")
	thread := getMap(vars, "thread")
	posts := getSlice(vars, "postlist")
	if len(posts) == 0 {
		msg := firstNonEmpty(getString(data, "Message", "messagestr"), getString(data, "Message", "messageval"))
		if strings.TrimSpace(cfg.KeylolCookie) == "" {
			return mediaMeta{}, fmt.Errorf("keylol requires cookie: %s", firstNonEmpty(msg, "postlist empty"))
		}
		return mediaMeta{}, fmt.Errorf("keylol postlist empty: %s", firstNonEmpty(msg, "no first post"))
	}
	post, _ := posts[0].(map[string]any)
	if post == nil {
		return mediaMeta{}, fmt.Errorf("keylol first post invalid")
	}

	messageHTML := firstNonEmpty(getString(post, "message"), getString(post, "message_html"))
	blocks := keylolBuildBlocks(messageHTML, getMap(post, "attachments"), finalURL)
	blocks = keylolEnrichSteamBlocks(blocks)
	blocks = keylolEnrichVideoBlocks(cfg, blocks)
	images := keylolImageGroupsFromBlocks(blocks)
	desc := keylolDescFromBlocks(blocks)
	title := firstNonEmpty(getString(thread, "subject"), getString(post, "subject"))
	author := cardDisplayAuthor(firstNonEmpty(getString(post, "author"), getString(thread, "author")))
	authorID := firstNonEmpty(getString(post, "authorid"), getString(thread, "authorid"))
	timestamp := keylolTime(firstNonEmpty(getString(post, "dateline"), getString(thread, "dateline")))
	avatar := keylolAvatarURL(authorID)

	return mediaMeta{
		URL:          raw,
		SourceURL:    raw,
		Platform:     "keylol",
		Title:        title,
		Author:       author,
		Avatar:       avatar,
		Timestamp:    timestamp,
		Desc:         desc,
		ImageURLs:    images,
		ImageHeads:   keylolHeaders(cfg, raw),
		KeylolBlocks: blocks,
	}, nil
}

func keylolHeaders(cfg config, referer string) map[string]string {
	headers := buildHeaders(false, firstNonEmpty(referer, "https://keylol.com/"), keylolUA)
	headers["Accept"] = "application/json,text/plain,*/*"
	if cfg.KeylolCookie != "" {
		headers["Cookie"] = cfg.KeylolCookie
	}
	return headers
}

func keylolThreadID(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		for _, key := range []string{"tid", "ptid"} {
			if v := u.Query().Get(key); regexp.MustCompile(`^\d+$`).MatchString(v) {
				return v
			}
		}
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)/(?:t|thread-)(\d+)(?:[-/.]|$)`),
		regexp.MustCompile(`(?i)[?&]tid=(\d+)`),
	} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func keylolExtractImages(messageHTML string, attachments map[string]any, base string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	add := func(raw string) {
		raw = strings.TrimSpace(htmlUnescape(raw))
		raw = strings.Trim(raw, ` "'`)
		if raw == "" {
			return
		}
		raw = absolutize(base, ensureHTTPS(raw))
		if !keylolUsableImage(raw) || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, []string{raw})
	}
	imgRe := regexp.MustCompile(`(?is)<img\b[^>]*(?:file|zoomfile|src)=["']([^"']+)["'][^>]*>`)
	for _, m := range imgRe.FindAllStringSubmatch(messageHTML, -1) {
		add(m[1])
	}
	for _, raw := range nestedHTTPURLs(attachments, 6) {
		add(raw)
	}
	for _, raw := range keylolAttachmentImageURLs(attachments) {
		add(raw)
	}
	return dedupeMediaGroups(out)
}

func keylolBuildBlocks(messageHTML string, attachments map[string]any, base string) []keylolBlock {
	messageHTML = keylolStripHiddenTips(messageHTML)
	messageHTML = keylolStripShowhideControls(messageHTML)
	messageHTML = keylolReplaceSteamLinks(messageHTML, base)
	messageHTML = keylolReplaceTextLinks(messageHTML, base)
	messageHTML = keylolReplaceCollapseBlocks(messageHTML)
	messageHTML = keylolReplaceHeadings(messageHTML)
	blocks := []keylolBlock{}
	seenImages := map[string]bool{}
	var textBuf strings.Builder
	appendTextBlock := func(text string) {
		for _, part := range keylolTextBlocks(text) {
			if len(blocks) > 0 && part.Kind == "text" && blocks[len(blocks)-1].Kind == "text" {
				blocks[len(blocks)-1].Text = strings.TrimSpace(blocks[len(blocks)-1].Text + "\n" + part.Text)
				continue
			}
			blocks = append(blocks, part)
		}
	}
	flushText := func() {
		text := keylolCleanBlockText(textBuf.String())
		textBuf.Reset()
		if text == "" || keylolNoiseLine(text) {
			return
		}
		appendTextBlock(text)
	}
	addImage := func(raw string, inline bool) {
		raw = strings.TrimSpace(html.UnescapeString(htmlUnescape(raw)))
		raw = strings.Trim(raw, ` "'`)
		if raw == "" {
			return
		}
		raw = absolutize(base, ensureHTTPS(raw))
		if !keylolContentImageOK(raw) || seenImages[raw] {
			return
		}
		flushText()
		seenImages[raw] = true
		kind := "image"
		if inline {
			kind = "inline_image"
		}
		blocks = append(blocks, keylolBlock{Kind: kind, URL: raw})
	}
	addVideo := func(tag string) {
		block := keylolVideoBlockFromIframe(tag, base)
		if block.URL == "" {
			return
		}
		flushText()
		blocks = append(blocks, block)
	}
	tokenRE := regexp.MustCompile(`(?is)<iframe\b[^>]*>.*?</iframe>|<img\b[^>]*>|<br\s*/?>|</p>|</div>|</li>|</tr>|</h[1-6]>`)
	last := 0
	for _, loc := range tokenRE.FindAllStringIndex(messageHTML, -1) {
		textBuf.WriteString(messageHTML[last:loc[0]])
		token := messageHTML[loc[0]:loc[1]]
		lowToken := strings.ToLower(token)
		if strings.HasPrefix(lowToken, "<img") {
			addImage(keylolImageURLFromTag(token), keylolInlineImageTag(token))
		} else if strings.HasPrefix(lowToken, "<iframe") {
			addVideo(token)
		} else {
			textBuf.WriteString("\n")
		}
		last = loc[1]
	}
	textBuf.WriteString(messageHTML[last:])
	flushText()
	for _, raw := range nestedHTTPURLs(attachments, 6) {
		raw = absolutize(base, ensureHTTPS(raw))
		if keylolContentImageOK(raw) && !seenImages[raw] {
			seenImages[raw] = true
			blocks = append(blocks, keylolBlock{Kind: "image", URL: raw})
		}
	}
	for _, raw := range keylolAttachmentImageURLs(attachments) {
		raw = absolutize(base, ensureHTTPS(raw))
		if keylolContentImageOK(raw) && !seenImages[raw] {
			seenImages[raw] = true
			blocks = append(blocks, keylolBlock{Kind: "image", URL: raw})
		}
	}
	return keylolCompactBlocks(blocks)
}

func keylolAttachmentImageURLs(attachments map[string]any) []string {
	if len(attachments) == 0 {
		return nil
	}
	out := []string{}
	for _, raw := range attachments {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if isImage := strings.TrimSpace(getString(m, "isimage")); isImage != "" && isImage != "1" {
			continue
		}
		base := strings.TrimSpace(getString(m, "url"))
		path := strings.TrimSpace(getString(m, "attachment"))
		if base == "" || path == "" {
			continue
		}
		u := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
		out = append(out, ensureHTTPS(u))
	}
	return out
}

func keylolReplaceCollapseBlocks(s string) string {
	re := regexp.MustCompile(`(?is)<div\b[^>]*class=["'][^"']*\bsff_collapse\b[^"']*["'][^>]*>\s*<div\b[^>]*class=["'][^"']*\bsff_collapse_b\b[^"']*["'][^>]*>(.*?)</div>`)
	s = re.ReplaceAllStringFunc(s, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 2 {
			return match
		}
		title := keylolCleanBlockText(m[1])
		title = strings.TrimSpace(strings.TrimPrefix(title, ">"))
		if title == "" {
			title = "折叠内容"
		}
		return "\n[keylol_collapse]" + title + "\n"
	})
	s = regexp.MustCompile(`(?is)<div\b[^>]*class=["'][^"']*\bsff_collapse_d\b[^"']*["'][^>]*>.*?点击隐藏.*?</div>\s*</div>`).ReplaceAllString(s, "\n")
	return s
}

func keylolReplaceHeadings(s string) string {
	re := regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]>`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		text := keylolCleanBlockText(m[2])
		if text == "" {
			return "\n"
		}
		if m[1] == "1" {
			return "\n[keylol_h1]" + text + "\n"
		}
		return "\n[keylol_h3]" + text + "\n"
	})
}

func keylolTextBlocks(text string) []keylolBlock {
	out := []keylolBlock{}
	buf := []string{}
	flush := func() {
		if len(buf) == 0 {
			return
		}
		out = append(out, keylolBlock{Kind: "text", Text: strings.Join(buf, "\n")})
		buf = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[keylol_steam]"):
			flush()
			block := keylolSteamBlockFromMarker(line)
			if block.URL != "" {
				out = append(out, block)
			}
		case strings.HasPrefix(line, "[keylol_asf]"):
			flush()
			id := strings.TrimSpace(strings.TrimPrefix(line, "[keylol_asf]"))
			if regexp.MustCompile(`^\d+$`).MatchString(id) {
				out = append(out, keylolBlock{Kind: "asf_link", Title: id})
			}
		case strings.HasPrefix(line, "[keylol_toolbar]"):
			flush()
			text := strings.TrimSpace(strings.TrimPrefix(line, "[keylol_toolbar]"))
			if text != "" {
				out = append(out, keylolBlock{Kind: "toolbar", Text: text})
			}
		case strings.HasPrefix(line, "[keylol_collapse]"):
			flush()
			out = append(out, keylolBlock{Kind: "collapse", Text: strings.TrimSpace(strings.TrimPrefix(line, "[keylol_collapse]"))})
		case strings.HasPrefix(line, "[keylol_h1]"):
			flush()
			out = append(out, keylolBlock{Kind: "heading1", Text: strings.TrimSpace(strings.TrimPrefix(line, "[keylol_h1]"))})
		case strings.HasPrefix(line, "[keylol_h3]"):
			flush()
			out = append(out, keylolBlock{Kind: "heading2", Text: strings.TrimSpace(strings.TrimPrefix(line, "[keylol_h3]"))})
		default:
			if keylolLooksLikeSteamToolbar(line) {
				flush()
				out = append(out, keylolBlock{Kind: "toolbar", Text: keylolCleanSteamToolbar(line)})
				continue
			}
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

func keylolReplaceSteamLinks(s, base string) string {
	re := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*store\.steampowered\.com/app/\d+[^"']*)["'][^>]*>(.*?)</a>`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		href := absolutize(base, html.UnescapeString(htmlUnescape(m[1])))
		if keylolSteamAppID(href) == "" {
			return match
		}
		title := keylolSteamTitleFromText(keylolCleanBlockText(m[2]), href)
		return "\n[keylol_steam]" + url.QueryEscape(href) + "|" + url.QueryEscape(title) + "\n"
	})
}

func keylolSteamBlockFromMarker(line string) keylolBlock {
	payload := strings.TrimSpace(strings.TrimPrefix(line, "[keylol_steam]"))
	parts := strings.SplitN(payload, "|", 2)
	rawURL, _ := url.QueryUnescape(parts[0])
	title := ""
	if len(parts) > 1 {
		title, _ = url.QueryUnescape(parts[1])
	}
	rawURL = strings.TrimSpace(rawURL)
	if keylolSteamAppID(rawURL) == "" {
		return keylolBlock{}
	}
	return keylolBlock{
		Kind:  "steam_card",
		URL:   rawURL,
		Title: keylolSteamTitleFromText(title, rawURL),
	}
}

func keylolSteamAppID(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(htmlUnescape(raw)))
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		if !strings.Contains(strings.ToLower(u.Host), "store.steampowered.com") {
			return ""
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && strings.EqualFold(parts[0], "app") && regexp.MustCompile(`^\d+$`).MatchString(parts[1]) {
			return parts[1]
		}
	}
	return ""
}

func keylolSteamTitleFromText(text, rawURL string) string {
	text = strings.TrimSpace(text)
	for _, prefix := range []string{"Steam 上的", "Steam上的"} {
		text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
	}
	if text != "" && !strings.Contains(strings.ToLower(text), "store.steampowered.com") {
		return text
	}
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 3 {
			name := strings.ReplaceAll(parts[2], "_", " ")
			name = strings.ReplaceAll(name, "-", " ")
			if name = strings.TrimSpace(name); name != "" {
				return name
			}
		}
	}
	return "Steam 游戏"
}

func keylolEnrichSteamBlocks(blocks []keylolBlock) []keylolBlock {
	for i := range blocks {
		if blocks[i].Kind != "steam_card" || blocks[i].URL == "" {
			continue
		}
		info, err := keylolFetchSteamApp(blocks[i].URL)
		if err != nil {
			continue
		}
		if info.Title != "" {
			blocks[i].Title = info.Title
		}
		if info.Desc != "" {
			blocks[i].Desc = info.Desc
		}
		if info.Cover != "" {
			blocks[i].Cover = info.Cover
		}
	}
	return blocks
}

func keylolEnrichVideoBlocks(cfg config, blocks []keylolBlock) []keylolBlock {
	for i := range blocks {
		if blocks[i].Kind != "video_embed" || blocks[i].URL == "" {
			continue
		}
		if strings.Contains(strings.ToLower(blocks[i].URL), "bilibili.com/video/") {
			view, err := fetchBiliView(cfg, blocks[i].URL)
			if err != nil {
				continue
			}
			if view.Title != "" {
				blocks[i].Title = view.Title
			}
			if view.Desc != "" {
				blocks[i].Desc = truncate(keylolCleanBlockText(view.Desc), 160)
			}
			if view.Pic != "" {
				blocks[i].Cover = ensureHTTPS(view.Pic)
			}
		}
	}
	return blocks
}

func keylolFetchSteamApp(rawURL string) (keylolBlock, error) {
	appID := keylolSteamAppID(rawURL)
	if appID == "" {
		return keylolBlock{}, fmt.Errorf("steam app id not found")
	}
	api, err := url.Parse(steamAPIBase)
	if err != nil {
		return keylolBlock{}, err
	}
	q := api.Query()
	q.Set("appids", appID)
	q.Set("l", "zh")
	q.Set("cc", "cn")
	api.RawQuery = q.Encode()
	body, _, status, err := fetchText(api.String(), map[string]string{"Accept": "application/json"}, true)
	if err != nil {
		return keylolBlock{}, err
	}
	if status >= 400 {
		return keylolBlock{}, fmt.Errorf("steam API HTTP %d", status)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return keylolBlock{}, err
	}
	app := getMap(data, appID)
	success, _ := app["success"].(bool)
	if !success {
		return keylolBlock{}, fmt.Errorf("steam appdetails failed")
	}
	detail := getMap(app, "data")
	return keylolBlock{
		Kind:  "steam_card",
		URL:   rawURL,
		Title: getString(detail, "name"),
		Desc:  truncate(keylolCleanBlockText(firstNonEmpty(getString(detail, "short_description"), getString(detail, "about_the_game"))), 200),
		Cover: firstNonEmpty(getString(detail, "header_image"), getString(detail, "capsule_image")),
	}, nil
}

func keylolStripHiddenTips(s string) string {
	s = regexp.MustCompile(`(?is)<div\b[^>]*class=["'][^"']*\baimg_tip\b[^"']*["'][^>]*>.*?</div>\s*</div>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<div\b[^>]*class=["'][^"']*\brnd_ai_pr\b[^"']*["'][^>]*>.*?</div>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)</?ignore_js_op[^>]*>`).ReplaceAllString(s, "")
	return s
}

func keylolStripShowhideControls(s string) string {
	s = regexp.MustCompile(`(?is)<p>\s*隐藏内容[^<]*<a\b[^>]*class=["'][^"']*\bshowhide-btn\b[^"']*["'][^>]*>.*?</a>\s*</p>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<a\b[^>]*class=["'][^"']*\bshowhide-btn\b[^"']*["'][^>]*>.*?</a>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<div>\s*<a\b[^>]*>\s*点击隐藏\s*</a>\s*</div>`).ReplaceAllString(s, "")
	return s
}

func keylolReplaceTextLinks(s, base string) string {
	re := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		m := re.FindStringSubmatch(match)
		if len(m) < 3 {
			return match
		}
		if regexp.MustCompile(`(?is)<img\b`).MatchString(m[2]) {
			return m[2]
		}
		text := keylolCleanBlockText(m[2])
		href := absolutize(base, html.UnescapeString(htmlUnescape(m[1])))
		if appID := keylolASFAppID(match + " " + href + " " + text); appID != "" {
			return "\n[keylol_toolbar]复制ASF代码\n[keylol_asf]" + appID + "\n"
		}
		if text == "" || strings.EqualFold(text, "链接") || strings.EqualFold(text, "link") {
			text = href
		}
		return " " + text + " "
	})
}

func keylolVideoBlockFromIframe(tag, base string) keylolBlock {
	re := regexp.MustCompile(`(?is)\bsrc\s*=\s*["']([^"']+)["']`)
	m := re.FindStringSubmatch(tag)
	if len(m) < 2 {
		return keylolBlock{}
	}
	src := absolutize(base, html.UnescapeString(htmlUnescape(m[1])))
	if bv := bvRE.FindString(src); bv != "" {
		return keylolBlock{
			Kind:  "video_embed",
			URL:   "https://www.bilibili.com/video/" + bv,
			Title: "Bilibili 视频 " + bv,
		}
	}
	return keylolBlock{}
}

func keylolASFAppID(raw string) string {
	raw = html.UnescapeString(htmlUnescape(raw))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)!addlicense\s+asf\s+a/(\d+)`),
		regexp.MustCompile(`(?i)\ba/(\d+)\b`),
	} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	if strings.Contains(strings.ToLower(raw), "asf") || strings.Contains(raw, "复制ASF代码") || strings.Contains(raw, "复制asf代码") {
		if m := regexp.MustCompile(`\b(\d{5,10})\b`).FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func keylolLooksLikeSteamToolbar(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return strings.Contains(line, "Steam商店") ||
		strings.Contains(line, "Steam评测区") ||
		strings.Contains(line, "其乐相关帖") ||
		strings.Contains(line, "Steam客户端中查看") ||
		strings.Contains(line, "复制ASF代码")
}

func keylolCleanSteamToolbar(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimRight(line, "|｜ ")
	line = regexp.MustCompile(`\s*([|｜])\s*`).ReplaceAllString(line, " $1 ")
	line = regexp.MustCompile(`\s+`).ReplaceAllString(line, " ")
	return strings.TrimSpace(line)
}

func keylolImageURLFromTag(tag string) string {
	for _, attr := range []string{"zoomfile", "file", "src"} {
		re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(attr) + `\s*=\s*["']([^"']+)["']`)
		if m := re.FindStringSubmatch(tag); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func keylolInlineImageTag(tag string) bool {
	w := keylolTagIntAttr(tag, "width")
	h := keylolTagIntAttr(tag, "height")
	if w > 0 && h > 0 && w <= 180 && h <= 120 {
		return true
	}
	return false
}

func keylolTagIntAttr(tag, name string) int {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `=["']?(\d+)`)
	m := re.FindStringSubmatch(tag)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func keylolContentImageOK(raw string) bool {
	low := strings.ToLower(raw)
	if !keylolImageExt.MatchString(low) {
		return false
	}
	for _, bad := range []string{
		"/uc_server/data/avatar/", "/common/usergroup/", "userinfo.gif", "qq_share.png",
		"fav.gif", "agree.gif", "collection.png", "rec_add.gif", "fj_btn.png",
		"roster-rank-icon", "magic/",
	} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	return true
}

func keylolCleanBlockText(s string) string {
	s = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>|<script[^>]*>.*?</script>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>|</h[1-6]>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = html.UnescapeString(htmlUnescape(s))
	s = regexp.MustCompile(`(?i)\[/?(?:attach|img|url|quote|size|color|font|align|b|i|u|list|\*)(?:=[^\]]*)?\]`).ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	spaceRE := regexp.MustCompile(`[ \t]+`)
	for _, line := range lines {
		line = strings.TrimSpace(spaceRE.ReplaceAllString(line, " "))
		if line == "" || keylolNoiseLine(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(collapseDuplicateLines(cleaned), "\n")
}

func keylolCompactBlocks(blocks []keylolBlock) []keylolBlock {
	out := make([]keylolBlock, 0, len(blocks))
	seenSteam := map[string]bool{}
	for _, block := range blocks {
		if block.Kind == "text" || block.Kind == "toolbar" || block.Kind == "heading1" || block.Kind == "heading2" || block.Kind == "collapse" {
			block.Text = strings.TrimSpace(block.Text)
			if block.Text == "" {
				continue
			}
			if block.Kind == "text" && len(out) > 0 && out[len(out)-1].Kind == "text" {
				out[len(out)-1].Text += "\n" + block.Text
				continue
			}
		}
		if (block.Kind == "image" || block.Kind == "inline_image" || block.Kind == "steam_card" || block.Kind == "video_embed") && block.URL == "" {
			continue
		}
		if block.Kind == "steam_card" {
			key := keylolSteamAppID(block.URL)
			if key != "" && seenSteam[key] {
				continue
			}
			seenSteam[key] = true
		}
		if block.Kind == "asf_link" && strings.TrimSpace(block.Title) == "" {
			continue
		}
		out = append(out, block)
	}
	return out
}

func keylolImageGroupsFromBlocks(blocks []keylolBlock) [][]string {
	out := [][]string{}
	for _, block := range blocks {
		if block.Kind == "image" && block.URL != "" {
			out = append(out, []string{block.URL})
		}
	}
	return dedupeMediaGroups(out)
}

func keylolDescFromBlocks(blocks []keylolBlock) string {
	parts := []string{}
	for _, block := range blocks {
		if (block.Kind == "text" || block.Kind == "heading1" || block.Kind == "heading2") && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func keylolUsableImage(raw string) bool {
	low := strings.ToLower(raw)
	if !keylolImageExt.MatchString(low) {
		return false
	}
	for _, bad := range []string{
		"/static/image/", "/uc_server/data/avatar/", "/common/usergroup/",
		"smilies/", "avatar_", "userinfo.gif", "qq_share.png",
		"fav.gif", "agree.gif", "collection.png", "rec_add.gif",
	} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	return true
}

func keylolCleanMessage(messageHTML string) string {
	s := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>|<script[^>]*>.*?</script>`).ReplaceAllString(messageHTML, "")
	s = regexp.MustCompile(`(?is)<img\b[^>]*>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = htmlUnescape(s)
	s = regexp.MustCompile(`(?i)\[/?(?:attach|img|url|quote|size|color|font|align|b|i|u|list|\*)(?:=[^\]]*)?\]`).ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	spaceRE := regexp.MustCompile(`[ \t]+`)
	for _, line := range lines {
		line = strings.TrimSpace(spaceRE.ReplaceAllString(line, " "))
		if line == "" || keylolNoiseLine(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(collapseDuplicateLines(cleaned), "\n")
}

func keylolNoiseLine(line string) bool {
	if keylolImageExt.MatchString(strings.ToLower(line)) {
		return true
	}
	for _, needle := range []string{
		"转载或引用本网站内容", "不代表本社区立场", "本网站保留追究",
		"下载附件", "点击文件名下载附件", "查看全部评分", "评分参与人数",
		"隐藏内容", "点击显示", "点击隐藏",
	} {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func collapseDuplicateLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	var last string
	for _, line := range lines {
		if line == last {
			continue
		}
		out = append(out, line)
		last = line
	}
	return out
}

func keylolTime(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(htmlUnescape(raw)))
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	raw = strings.Join(strings.Fields(raw), " ")
	if raw == "" {
		return ""
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 1000000000 {
		return time.Unix(sec, 0).Format("2006-01-02 15:04")
	}
	return raw
}

func keylolAvatarURL(authorID string) string {
	uid, err := strconv.Atoi(strings.TrimSpace(authorID))
	if err != nil || uid <= 0 {
		return ""
	}
	p := fmt.Sprintf("%09d", uid)
	return fmt.Sprintf("https://keylol.com/uc_server/data/avatar/%s/%s/%s/%s_avatar_middle.jpg", p[0:3], p[3:5], p[5:7], p[7:9])
}
