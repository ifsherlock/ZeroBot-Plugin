package mediaparser

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	linuxdoBase    = "https://linux.do"
	linuxdoReferer = "https://linux.do/"
	linuxdoUA      = "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 Mobile Safari/537.36"
)

func parseLinuxdo(cfg config, raw string) (mediaMeta, error) {
	topicID := linuxdoTopicID(raw)
	if topicID == "" {
		return mediaMeta{}, fmt.Errorf("linux.do topic id not found")
	}
	postNumber := linuxdoPostNumber(raw)
	api := linuxdoBase + "/t/" + topicID + ".json"
	headers := linuxdoHeaders(cfg, raw)
	body, finalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", api, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		if htmlMeta, htmlErr := linuxdoParseHTMLFallback(cfg, raw, topicID, postNumber); htmlErr == nil {
			return htmlMeta, nil
		}
		return mediaMeta{}, fmt.Errorf("linux.do API HTTP %d final=%s %s request=%s", status, finalURL, linuxdoErrorSummary(body), linuxdoRequestSummary(cfg))
	}
	if postNumber != "" {
		if body, finalURL, err = linuxdoEnsurePostLoaded(cfg, raw, body, postNumber, finalURL); err != nil {
			return mediaMeta{}, err
		}
	}
	meta, err := parseLinuxdoTopicJSON(raw, finalURL, body)
	if err != nil {
		return mediaMeta{}, err
	}
	ua := linuxdoUserAgent(cfg)
	meta.VideoHeads = buildHeaders(true, linuxdoReferer, ua)
	meta.ImageHeads = buildHeaders(false, linuxdoReferer, ua)
	if cfg.LinuxdoCookie != "" {
		meta.VideoHeads["Cookie"] = cfg.LinuxdoCookie
		meta.ImageHeads["Cookie"] = cfg.LinuxdoCookie
	}
	return meta, nil
}

func linuxdoParseHTMLFallback(cfg config, sourceURL, topicID, postNumber string) (mediaMeta, error) {
	pageURL := linuxdoTopicPageURL(sourceURL, topicID, postNumber)
	headers := linuxdoHeaders(cfg, sourceURL)
	htmlBody, finalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", pageURL, headers, true)
	htmlErr := err
	if err == nil && status >= 400 {
		htmlErr = fmt.Errorf("linux.do page HTTP %d final=%s %s request=%s", status, finalURL, linuxdoErrorSummary(htmlBody), linuxdoRequestSummary(cfg))
	}
	if htmlErr == nil {
		if topic := linuxdoExtractTopicJSONFromHTML(htmlBody); topic != nil {
			return parseLinuxdoTopicJSON(sourceURL, finalURL, mustJSON(topic))
		}
	}
	title := ""
	if htmlErr == nil {
		title = linuxdoHTMLTitle(htmlBody)
	}
	rawBody, rawFinalURL, rawErr := linuxdoFetchRawPost(cfg, sourceURL, topicID, postNumber)
	if rawErr == nil && strings.TrimSpace(rawBody) != "" {
		desc := linuxdoCleanRaw(rawBody)
		images := linuxdoExtractImagesFromText(rawBody, rawFinalURL)
		return mediaMeta{
			URL:        pageURL,
			SourceURL:  sourceURL,
			Platform:   "linuxdo",
			Title:      firstNonEmpty(title, "Linux.do Topic "+topicID),
			Desc:       desc,
			Cover:      firstImageURL(images),
			ImageURLs:  images,
			ImageHeads: buildHeaders(false, linuxdoReferer, linuxdoUserAgent(cfg)),
			VideoHeads: buildHeaders(true, linuxdoReferer, linuxdoUserAgent(cfg)),
		}, nil
	}
	if htmlErr != nil {
		return mediaMeta{}, htmlErr
	}
	if topic := linuxdoExtractTopicJSONFromHTML(htmlBody); topic != nil {
		return parseLinuxdoTopicJSON(sourceURL, finalURL, mustJSON(topic))
	}
	meta := mediaMeta{
		URL:        pageURL,
		SourceURL:  sourceURL,
		Platform:   "linuxdo",
		Title:      firstNonEmpty(linuxdoHTMLTitle(htmlBody), "Linux.do Topic "+topicID),
		Desc:       linuxdoHTMLBodyText(htmlBody),
		ImageHeads: buildHeaders(false, linuxdoReferer, linuxdoUserAgent(cfg)),
		VideoHeads: buildHeaders(true, linuxdoReferer, linuxdoUserAgent(cfg)),
	}
	return meta, nil
}

func linuxdoFetchRawPost(cfg config, sourceURL, topicID, postNumber string) (string, string, error) {
	if postNumber == "" || postNumber == "0" {
		postNumber = "1"
	}
	rawURL := linuxdoBase + "/raw/" + topicID + "/" + postNumber
	headers := linuxdoHeaders(cfg, sourceURL)
	headers["Accept"] = "text/plain,*/*"
	body, finalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", rawURL, headers, true)
	if err != nil {
		return "", finalURL, err
	}
	if status >= 400 {
		return "", finalURL, fmt.Errorf("linux.do raw HTTP %d final=%s %s request=%s", status, finalURL, linuxdoErrorSummary(body), linuxdoRequestSummary(cfg))
	}
	return body, finalURL, nil
}

func linuxdoTopicPageURL(sourceURL, topicID, postNumber string) string {
	if sourceURL != "" && !strings.HasSuffix(strings.ToLower(sourceURL), ".json") {
		return sourceURL
	}
	if postNumber == "" || postNumber == "0" {
		postNumber = "1"
	}
	return linuxdoBase + "/t/" + topicID + "/" + postNumber
}

func linuxdoExtractTopicJSONFromHTML(htmlBody string) map[string]any {
	for _, marker := range []string{
		"data-preloaded",
		"window.__PRELOADED_STATE__",
		"window.__data",
		"Discourse._preloadedState",
	} {
		if root, err := extractAssignedJSONObject(htmlBody, marker); err == nil {
			if topic := linuxdoFindTopicMap(root); topic != nil {
				return topic
			}
		}
	}
	return nil
}

func linuxdoFindTopicMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["post_stream"]; ok {
			return x
		}
		for _, item := range x {
			if topic := linuxdoFindTopicMap(item); topic != nil {
				return topic
			}
		}
	case []any:
		for _, item := range x {
			if topic := linuxdoFindTopicMap(item); topic != nil {
				return topic
			}
		}
	}
	return nil
}

func linuxdoHTMLTitle(body string) string {
	if title := titleTag(body); title != "" {
		return title
	}
	if m := regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`).FindStringSubmatch(body); len(m) > 1 {
		return htmlUnescape(m[1])
	}
	return ""
}

func linuxdoHTMLBodyText(body string) string {
	parts := []string{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`),
		regexp.MustCompile(`(?is)<main\b[^>]*>(.*?)</main>`),
		regexp.MustCompile(`(?is)<div\b[^>]+class=["'][^"']*cooked[^"']*["'][^>]*>(.*?)</div>`),
	} {
		if m := re.FindStringSubmatch(body); len(m) > 1 {
			text := linuxdoCleanCooked(m[1])
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return firstNonEmpty(parts...)
}

func linuxdoCleanRaw(raw string) string {
	s := strings.TrimSpace(raw)
	s = regexp.MustCompile(`(?m)^!\[[^\]]*\]\([^)]+\)\s*$`).ReplaceAllString(s, "[图片]")
	s = regexp.MustCompile(`(?m)^\s*https?://\S+\.(?:png|jpe?g|webp|gif)(?:\?\S*)?\s*$`).ReplaceAllString(s, "[图片]")
	s = html.UnescapeString(htmlUnescape(s))
	s = regexp.MustCompile(`[ \t\r\f\v]+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	s = linuxdoStripPromotionDeclarations(s)
	return strings.TrimSpace(s)
}

func linuxdoExtractImagesFromText(raw, base string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	add := func(u string) {
		u = strings.Trim(strings.TrimSpace(html.UnescapeString(htmlUnescape(u))), ` "'`)
		if u == "" {
			return
		}
		u = absolutize(base, ensureHTTPS(u))
		if !linuxdoUsableImage(u) || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, []string{u})
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`),
		regexp.MustCompile(`https?://\S+\.(?:png|jpe?g|webp|gif)(?:\?\S*)?`),
		regexp.MustCompile(`(?is)<img\b[^>]+src=["']([^"']+)["']`),
	} {
		for _, m := range re.FindAllStringSubmatch(raw, -1) {
			add(m[1])
		}
	}
	return dedupeMediaGroups(out)
}

func firstImageURL(groups [][]string) string {
	if len(groups) == 0 || len(groups[0]) == 0 {
		return ""
	}
	return groups[0][0]
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func linuxdoEnsurePostLoaded(cfg config, referer, topicBody, postNumber, finalURL string) (string, string, error) {
	postID, hasPost := linuxdoPostIDForNumber(topicBody, postNumber)
	if hasPost || postID == "" {
		return topicBody, finalURL, nil
	}
	headers := linuxdoHeaders(cfg, referer)
	api := linuxdoBase + "/posts/" + postID + ".json"
	postBody, postFinalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", api, headers, true)
	if err != nil {
		return "", "", err
	}
	if status >= 400 {
		return "", "", fmt.Errorf("linux.do post API HTTP %d final=%s %s request=%s", status, postFinalURL, linuxdoErrorSummary(postBody), linuxdoRequestSummary(cfg))
	}
	merged, err := linuxdoMergePostIntoTopic(topicBody, postBody)
	if err != nil {
		return "", "", err
	}
	return merged, postFinalURL, nil
}

func linuxdoHeaders(cfg config, referer string) map[string]string {
	headers := buildHeaders(false, firstNonEmpty(referer, linuxdoReferer), linuxdoUserAgent(cfg))
	headers["Accept"] = "application/json,text/plain,*/*"
	headers["Origin"] = linuxdoBase
	if cfg.LinuxdoCookie != "" {
		headers["Cookie"] = cfg.LinuxdoCookie
	}
	return headers
}

func linuxdoUserAgent(cfg config) string {
	return firstNonEmpty(strings.TrimSpace(cfg.LinuxdoUA), linuxdoUA)
}

func linuxdoRequestSummary(cfg config) string {
	cookie := strings.TrimSpace(cfg.LinuxdoCookie)
	ua := linuxdoUserAgent(cfg)
	return fmt.Sprintf(
		"cookie_set=%t cf_clearance_set=%t ua_len=%d proxy_set=%t",
		cookie != "",
		strings.Contains(strings.ToLower(cookie), "cf_clearance="),
		len(ua),
		proxyForPlatform(cfg, "linuxdo") != "",
	)
}

func linuxdoErrorSummary(body string) string {
	clean := strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(body, " "))
	title := ""
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(body); len(m) > 1 {
		title = strings.TrimSpace(html.UnescapeString(htmlUnescape(m[1])))
	}
	signals := []string{}
	lower := strings.ToLower(body)
	hasSignal := func(label string) bool {
		for _, signal := range signals {
			if signal == label {
				return true
			}
		}
		return false
	}
	for _, item := range []struct {
		key   string
		label string
	}{
		{"just a moment", "cloudflare_challenge"},
		{"cf-browser-verification", "cloudflare_challenge"},
		{"challenge-platform", "cloudflare_challenge"},
		{"cf-mitigated", "cloudflare_mitigated"},
		{"login", "login_hint"},
	} {
		if strings.Contains(lower, item.key) && !hasSignal(item.label) {
			signals = append(signals, item.label)
		}
	}
	parts := []string{fmt.Sprintf("body_len=%d", len(body))}
	if title != "" {
		parts = append(parts, fmt.Sprintf("title=%q", title))
	}
	if len(signals) > 0 {
		parts = append(parts, "signals="+strings.Join(signals, ","))
	}
	if clean != "" {
		parts = append(parts, "snippet="+strconv.Quote(truncate(clean, 320)))
	}
	return strings.Join(parts, " ")
}

func linuxdoTopicID(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if regexp.MustCompile(`^\d+$`).MatchString(part) {
				return part
			}
			if strings.HasSuffix(part, ".json") {
				base := strings.TrimSuffix(part, ".json")
				if regexp.MustCompile(`^\d+$`).MatchString(base) {
					return base
				}
			}
		}
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)/t/(?:[^/\s]+/)?(\d+)(?:[/.?#]|$)`),
		regexp.MustCompile(`(?i)/t/(\d+)\.json(?:[?#]|$)`),
	} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func parseLinuxdoTopicJSON(sourceURL, finalURL, body string) (mediaMeta, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return mediaMeta{}, err
	}
	topicID := getString(data, "id")
	if topicID == "" {
		return mediaMeta{}, fmt.Errorf("linux.do topic id missing")
	}
	posts := getSlice(data, "post_stream", "posts")
	post := linuxdoSelectPost(posts, linuxdoPostNumber(sourceURL))
	cooked := getString(post, "cooked")
	desc := linuxdoCleanCooked(firstNonEmpty(cooked, getString(data, "excerpt")))
	images := linuxdoExtractImages(cooked, finalURL)
	cover := ""
	if len(images) > 0 && len(images[0]) > 0 {
		cover = images[0][0]
	}
	postNumber := getString(post, "post_number")
	if postNumber == "" {
		postNumber = "1"
	}
	slug := getString(data, "slug")
	link := linuxdoShareURL(topicID, slug, postNumber)
	title := strings.TrimSpace(getString(data, "title"))
	if title == "" {
		title = "Linux.do Topic " + topicID
	}
	author := firstNonEmpty(getString(post, "name"), getString(post, "username"))
	if author == "" {
		author = linuxdoFirstPoster(data)
	}
	avatar := linuxdoAvatarURL(getString(post, "avatar_template"), 120)
	return mediaMeta{
		URL:        link,
		SourceURL:  firstNonEmpty(sourceURL, link),
		Platform:   "linuxdo",
		Title:      title,
		Author:     author,
		Avatar:     avatar,
		Timestamp:  linuxdoTime(getString(post, "created_at")),
		Desc:       desc,
		Cover:      cover,
		ImageURLs:  images,
		ImageHeads: buildHeaders(false, linuxdoReferer, linuxdoUA),
		VideoHeads: buildHeaders(true, linuxdoReferer, linuxdoUA),
	}, nil
}

func linuxdoSelectPost(posts []any, postNumber string) map[string]any {
	if len(posts) == 0 {
		return nil
	}
	if postNumber != "" {
		for _, item := range posts {
			post, _ := item.(map[string]any)
			if getString(post, "post_number") == postNumber {
				return post
			}
		}
	}
	post, _ := posts[0].(map[string]any)
	return post
}

func linuxdoPostIDForNumber(body, postNumber string) (string, bool) {
	postNumber = strings.TrimSpace(postNumber)
	if postNumber == "" {
		return "", true
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return "", false
	}
	for _, item := range getSlice(data, "post_stream", "posts") {
		post, _ := item.(map[string]any)
		if getString(post, "post_number") == postNumber {
			return getString(post, "id"), true
		}
	}
	stream := getSlice(data, "post_stream", "stream")
	idx, err := strconv.Atoi(postNumber)
	if err != nil || idx <= 0 || idx > len(stream) {
		return "", false
	}
	return getString(stream[idx-1]), false
}

func linuxdoMergePostIntoTopic(topicBody, postBody string) (string, error) {
	var topic map[string]any
	if err := json.Unmarshal([]byte(topicBody), &topic); err != nil {
		return "", err
	}
	var post map[string]any
	if err := json.Unmarshal([]byte(postBody), &post); err != nil {
		return "", err
	}
	if post == nil {
		return topicBody, nil
	}
	stream := getMap(topic, "post_stream")
	if stream == nil {
		stream = map[string]any{}
		topic["post_stream"] = stream
	}
	posts := getSlice(stream, "posts")
	targetNumber := getString(post, "post_number")
	replaced := false
	for i, item := range posts {
		old, _ := item.(map[string]any)
		if getString(old, "post_number") == targetNumber {
			posts[i] = post
			replaced = true
			break
		}
	}
	if !replaced {
		posts = append(posts, post)
	}
	stream["posts"] = posts
	out, err := json.Marshal(topic)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func linuxdoPostNumber(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "t" {
		return ""
	}
	numeric := []string{}
	for _, part := range parts[1:] {
		if regexp.MustCompile(`^\d+$`).MatchString(part) {
			numeric = append(numeric, part)
		}
	}
	if len(numeric) >= 2 {
		return numeric[1]
	}
	return ""
}

func linuxdoShareURL(topicID, slug, postNumber string) string {
	if postNumber == "" || postNumber == "0" {
		postNumber = "1"
	}
	slug = strings.Trim(strings.TrimSpace(slug), "/")
	if slug == "" {
		return linuxdoBase + "/t/" + topicID + "/" + postNumber
	}
	return linuxdoBase + "/t/" + slug + "/" + topicID + "/" + postNumber
}

func linuxdoFirstPoster(data map[string]any) string {
	posters := getSlice(data, "posters")
	users := getSlice(data, "details", "participants")
	if len(posters) == 0 || len(users) == 0 {
		return ""
	}
	userID := getString(posters[0], "user_id")
	for _, item := range users {
		if getString(item, "id") == userID {
			return firstNonEmpty(getString(item, "name"), getString(item, "username"))
		}
	}
	return ""
}

func linuxdoAvatarURL(template string, size int) string {
	template = strings.TrimSpace(template)
	if template == "" {
		return ""
	}
	if size <= 0 {
		size = 120
	}
	template = strings.ReplaceAll(template, "{size}", fmt.Sprintf("%d", size))
	return absolutize(linuxdoBase, ensureHTTPS(template))
}

func linuxdoExtractImages(cooked, base string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	add := func(raw string) {
		raw = strings.Trim(strings.TrimSpace(html.UnescapeString(htmlUnescape(raw))), ` "'`)
		if raw == "" {
			return
		}
		raw = absolutize(base, ensureHTTPS(raw))
		if !linuxdoUsableImage(raw) || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, []string{raw})
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)<img\b[^>]+src=["']([^"']+)["']`),
		regexp.MustCompile(`(?is)<a\b[^>]+href=["']([^"']+\.(?:png|jpe?g|webp|gif)(?:\?[^"']*)?)["']`),
	} {
		for _, m := range re.FindAllStringSubmatch(cooked, -1) {
			add(m[1])
		}
	}
	return dedupeMediaGroups(out)
}

func linuxdoUsableImage(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if strings.Contains(u.Path, "/user_avatar/") {
		return false
	}
	if strings.Contains(u.Path, "/emoji/") || strings.Contains(u.Path, "/images/emoji/") || strings.Contains(u.Host, "twemoji") {
		return false
	}
	return regexp.MustCompile(`(?i)\.(?:png|jpe?g|webp|gif)$`).MatchString(u.Path)
}

func linuxdoCleanCooked(cooked string) string {
	s := cooked
	s = regexp.MustCompile(`(?is)<script\b.*?</script>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<style\b.*?</style>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre>`).ReplaceAllString(s, "\n[代码块]\n")
	s = regexp.MustCompile(`(?is)<blockquote\b[^>]*>`).ReplaceAllString(s, "\n引用：")
	s = regexp.MustCompile(`(?is)</(?:p|div|li|blockquote|h[1-6])>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<img\b[^>]*>`).ReplaceAllString(s, "\n[图片]\n")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = html.UnescapeString(htmlUnescape(s))
	s = regexp.MustCompile(`[ \t\r\f\v]+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	s = linuxdoStripPromotionDeclarations(s)
	return strings.TrimSpace(s)
}

func linuxdoStripPromotionDeclarations(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	seenIntro := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if linuxdoPromotionDeclarationStart(trimmed) {
			skipping = true
			seenIntro = false
			continue
		}
		if skipping {
			if strings.Contains(trimmed, "以下为项目介绍正文内容") {
				seenIntro = true
				continue
			}
			if seenIntro && !linuxdoPromotionDeclarationLine(trimmed) {
				skipping = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	cleaned := strings.Join(out, "\n")
	cleaned = regexp.MustCompile(`\n{3,}`).ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}

func linuxdoPromotionDeclarationStart(s string) bool {
	return strings.Contains(s, "本帖使用社区开源推广") ||
		strings.Contains(s, "本帖使用社区公益推广")
}

func linuxdoPromotionDeclarationLine(s string) bool {
	if s == "" {
		return true
	}
	keys := []string{
		"我的帖子已经打上",
		"我的开源项目",
		"我的项目",
		"我帖子内的项目介绍",
		"我的站点存在登录",
		"以上选择我承诺",
		"以下为项目介绍正文内容",
		"AI生成、润色内容",
	}
	for _, key := range keys {
		if strings.Contains(s, key) {
			return true
		}
	}
	return regexp.MustCompile(`^(?:是|否|是 / 否)\s*$`).MatchString(s)
}

func linuxdoTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 19 {
		return strings.ReplaceAll(raw[:19], "T", " ")
	}
	return raw
}
