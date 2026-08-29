package mediaparser

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const xiaoheiheUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

type xiaoheiheBlock struct {
	Kind string
	Text string
	URL  string
}

func parseXiaoheihe(cfg config, raw string) (mediaMeta, error) {
	ctx, err := resolveXiaoheiheContext(raw)
	if err != nil {
		return mediaMeta{}, err
	}
	if ctx.LinkID != "" {
		if xiaoheiheMobileScreenshotEnabled() {
			meta, err := parseXiaoheiheBBS(ctx)
			if err != nil {
				logrusWarnXiaoheiheBBSAPI(ctx.LinkID, err)
				meta = xiaoheiheBrowserScreenshotMeta(ctx)
			}
			meta.XiaoheiheBrowserShot = true
			return meta, nil
		}
		return parseXiaoheiheBBS(ctx)
	}
	headers := map[string]string{
		"User-Agent":      xiaoheiheUA,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9",
		"Referer":         "https://www.xiaoheihe.cn/",
	}
	html, finalURL, status, err := fetchText(ctx.PageURL, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("xiaoheihe page HTTP %d", status)
	}
	if finalURL != "" {
		ctx.PageURL = finalURL
	}
	return buildXiaoheiheGameMeta(ctx, html)
}

func xiaoheiheBrowserScreenshotMeta(ctx xiaoheiheContext) mediaMeta {
	return mediaMeta{
		URL:         fmt.Sprintf("https://www.xiaoheihe.cn/app/bbs/link/%s", ctx.LinkID),
		SourceURL:   ctx.SourceURL,
		Platform:    "xiaoheihe",
		Title:       "小黑盒帖子",
		ArticleCard: true,
	}
}

type xiaoheiheContext struct {
	SourceURL string
	PageURL   string
	AppID     int
	GameType  string
	LinkID    string
}

func resolveXiaoheiheContext(raw string) (xiaoheiheContext, error) {
	if linkID := xiaoheiheBBSLinkID(raw); linkID != "" {
		return xiaoheiheContext{SourceURL: raw, PageURL: raw, LinkID: linkID}, nil
	}
	appid, gameType := xiaoheiheAppIDGameType(raw)
	if appid <= 0 || gameType == "" {
		return xiaoheiheContext{}, fmt.Errorf("xiaoheihe appid/game_type not found")
	}
	return xiaoheiheContext{
		SourceURL: raw,
		PageURL:   xiaoheiheCanonicalGameURL(appid, gameType),
		AppID:     appid,
		GameType:  gameType,
	}, nil
}

func parseXiaoheiheBBS(ctx xiaoheiheContext) (mediaMeta, error) {
	if meta, err := parseXiaoheiheBBSSignedAPI(ctx); err == nil {
		return meta, nil
	} else {
		logrusWarnXiaoheiheBBSAPI(ctx.LinkID, err)
	}
	headers := map[string]string{
		"User-Agent":      xiaoheiheUA,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9",
		"Referer":         "https://www.xiaoheihe.cn/",
	}
	share := xiaoheiheBBSRedirectData(ctx.PageURL, headers)
	pageURL := fmt.Sprintf("https://www.xiaoheihe.cn/app/bbs/link/%s", ctx.LinkID)
	html, finalURL, status, err := fetchText(ctx.PageURL, headers, true)
	if err != nil || status >= 400 {
		html, finalURL, status, err = fetchText(pageURL, headers, true)
	}
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("xiaoheihe BBS page HTTP %d", status)
	}
	if finalURL != "" {
		pageURL = finalURL
	}
	if share == nil {
		share = map[string]any{}
	}
	title := firstNonEmpty(getString(share, "link", "title"), og(html, "title"), xiaoheiheTitle(html), "小黑盒帖子")
	desc := cleanHTMLText(firstNonEmpty(getString(share, "link", "description"), og(html, "description"), xiaoheiheDesc(html)))
	cover := firstNonEmpty(getString(share, "link", "thumb"), og(html, "image"), firstNestedHTTPURLByKeys(html, 2, "thumb", "image", "cover"))
	images := [][]string{}
	if cover != "" {
		images = append([][]string{{ensureHTTPS(cover)}}, images...)
	}
	images = uniqueURLGroups(images)
	videos := xiaoheiheVideoGroups(html)
	if len(videos) == 0 && len(images) == 0 {
		return mediaMeta{}, fmt.Errorf("xiaoheihe BBS did not expose usable media")
	}
	return mediaMeta{
		URL:         pageURL,
		SourceURL:   ctx.SourceURL,
		Platform:    "xiaoheihe",
		Title:       title,
		Desc:        desc,
		Cover:       cover,
		VideoURLs:   videos,
		ImageURLs:   images,
		VideoHeads:  buildHeaders(true, "https://www.xiaoheihe.cn/", xiaoheiheUA),
		ImageHeads:  buildHeaders(false, "https://www.xiaoheihe.cn/", xiaoheiheUA),
		ForceLocal:  len(videos) > 0,
		ArticleCard: true,
	}, nil
}

func parseXiaoheiheBBSSignedAPI(ctx xiaoheiheContext) (mediaMeta, error) {
	result, err := fetchXiaoheiheSignedAPI("/bbs/app/link/tree", map[string]string{
		"link_id":    ctx.LinkID,
		"owner_only": "1",
	})
	if err != nil {
		return mediaMeta{}, err
	}
	link := getMap(result, "link")
	if link == nil {
		return mediaMeta{}, fmt.Errorf("xiaoheihe BBS API missing link")
	}
	title := firstNonEmpty(getString(link, "title"), "小黑盒帖子")
	user := getMap(link, "user")
	if user == nil {
		user = getMap(link, "author")
	}
	nickname := firstNonEmpty(getString(user, "nickname"), getString(user, "username"))
	uid := firstNonEmpty(getString(user, "heybox_id"), getString(user, "userid"), getString(user, "uid"))
	author := nickname
	if nickname != "" && uid != "" {
		author = nickname + " (uid:" + uid + ")"
	}
	avatar := firstNonEmpty(getString(user, "avatar"), getString(user, "avartar"))
	timestamp := ""
	if ts := getFloat(link, "create_at"); ts > 0 {
		timestamp = time.Unix(int64(ts), 0).Format("2006-01-02")
	}
	desc, videos, images, blocks := extractXiaoheiheBBSTextAndMedia(link)
	if len(videos) == 0 && len(images) == 0 {
		return mediaMeta{}, fmt.Errorf("xiaoheihe BBS post has no media")
	}
	cover := ""
	if len(images) > 0 && len(images[0]) > 0 {
		cover = images[0][0]
	}
	if cover == "" {
		cover = firstNonEmpty(getString(link, "thumb"), getString(link, "cover"))
	}
	return mediaMeta{
		URL:             fmt.Sprintf("https://www.xiaoheihe.cn/app/bbs/link/%s", ctx.LinkID),
		SourceURL:       ctx.SourceURL,
		Platform:        "xiaoheihe",
		Title:           title,
		Author:          author,
		Avatar:          avatar,
		Desc:            desc,
		Timestamp:       timestamp,
		Cover:           cover,
		VideoURLs:       uniqueURLGroups(videos),
		ImageURLs:       uniqueURLGroups(images),
		VideoHeads:      buildHeaders(true, "https://www.xiaoheihe.cn/", xiaoheiheUA),
		ImageHeads:      buildHeaders(false, "https://www.xiaoheihe.cn/", xiaoheiheUA),
		ForceLocal:      len(videos) > 0,
		ArticleCard:     true,
		XiaoheiheBlocks: blocks,
	}, nil
}

func extractXiaoheiheBBSTextAndMedia(link map[string]any) (string, [][]string, [][]string, []xiaoheiheBlock) {
	text := getString(link, "text")
	blocks := extractXiaoheiheBBSBlocks(text)
	descParts := []string{}
	videos := [][]string{}
	images := [][]string{}
	if getFloat(link, "has_video") > 0 {
		if videoURL := getString(link, "video_url"); videoURL != "" {
			if strings.Contains(strings.ToLower(videoURL), ".m3u8") && !strings.HasPrefix(videoURL, "m3u8:") {
				videoURL = "m3u8:" + videoURL
			}
			videos = append(videos, []string{videoURL})
			blocks = append([]xiaoheiheBlock{{Kind: "video", URL: videoURL}}, blocks...)
		}
	}
	for _, block := range blocks {
		switch block.Kind {
		case "text":
			if part := strings.TrimSpace(block.Text); part != "" {
				descParts = append(descParts, part)
			}
		case "image":
			if block.URL != "" {
				images = append(images, []string{ensureHTTPS(block.URL)})
			}
		case "video":
			if block.URL != "" && !containsURLGroup(videos, block.URL) {
				videos = append(videos, []string{block.URL})
			}
		}
	}
	if len(blocks) == 0 && strings.TrimSpace(text) != "" {
		descParts = append(descParts, strings.TrimSpace(text))
	}
	return strings.TrimSpace(strings.Join(descParts, "\n")), videos, images, blocks
}

func extractXiaoheiheBBSBlocks(raw string) []xiaoheiheBlock {
	var items []map[string]any
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &items) != nil {
		if text := strings.TrimSpace(raw); text != "" {
			return []xiaoheiheBlock{{Kind: "text", Text: cleanHTMLText(text)}}
		}
		return nil
	}
	blocks := []xiaoheiheBlock{}
	for _, item := range items {
		switch getString(item, "type") {
		case "html":
			if text := cleanHTMLText(getString(item, "text")); text != "" {
				blocks = append(blocks, xiaoheiheBlock{Kind: "text", Text: text})
			}
		case "text":
			if text := strings.TrimSpace(getString(item, "text")); text != "" {
				blocks = append(blocks, xiaoheiheBlock{Kind: "text", Text: text})
			}
		case "img":
			if rawURL := getString(item, "url"); rawURL != "" {
				blocks = append(blocks, xiaoheiheBlock{Kind: "image", URL: ensureHTTPS(rawURL)})
			}
		case "video", "gif":
			mediaURL := firstNonEmpty(getString(item, "url"), getString(item, "video_url"))
			if mediaURL == "" {
				continue
			}
			if strings.Contains(strings.ToLower(mediaURL), ".gif") {
				blocks = append(blocks, xiaoheiheBlock{Kind: "image", URL: ensureHTTPS(mediaURL)})
				continue
			}
			if strings.Contains(strings.ToLower(mediaURL), ".m3u8") && !strings.HasPrefix(mediaURL, "m3u8:") {
				mediaURL = "m3u8:" + mediaURL
			}
			blocks = append(blocks, xiaoheiheBlock{Kind: "video", URL: mediaURL})
		}
	}
	return compactXiaoheiheBlocks(blocks)
}

func compactXiaoheiheBlocks(blocks []xiaoheiheBlock) []xiaoheiheBlock {
	out := make([]xiaoheiheBlock, 0, len(blocks))
	for _, block := range blocks {
		block.Text = strings.TrimSpace(block.Text)
		block.URL = strings.TrimSpace(block.URL)
		if (block.Kind == "text" && block.Text == "") || (block.Kind != "text" && block.URL == "") {
			continue
		}
		if len(out) > 0 && block.Kind == "text" && out[len(out)-1].Kind == "text" {
			out[len(out)-1].Text = strings.TrimSpace(out[len(out)-1].Text + "\n" + block.Text)
			continue
		}
		out = append(out, block)
	}
	return out
}

func containsURLGroup(groups [][]string, target string) bool {
	for _, group := range groups {
		for _, raw := range group {
			if raw == target {
				return true
			}
		}
	}
	return false
}

func fetchXiaoheiheSignedAPI(path string, params map[string]string) (map[string]any, error) {
	base := map[string]string{
		"os_type":       "web",
		"app":           "heybox",
		"client_type":   "web",
		"version":       "999.0.4",
		"web_version":   "2.5",
		"x_client_type": "web",
		"x_app":         "heybox_website",
		"heybox_id":     "",
		"x_os_type":     "Windows",
		"device_info":   "Chrome",
	}
	q := url.Values{}
	for k, v := range base {
		q.Set(k, v)
	}
	for k, v := range params {
		q.Set(k, v)
	}
	for k, v := range xiaoheiheSign(path) {
		q.Set(k, v)
	}
	endpoint := "https://api.xiaoheihe.cn" + path + "?" + q.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", xiaoheiheUA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", "x_xhh_tokenid=")
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("xiaoheihe API HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if getString(data, "status") != "ok" {
		return nil, fmt.Errorf("xiaoheihe API returned %s %s", getString(data, "status"), getString(data, "msg"))
	}
	result := getMap(data, "result")
	if result == nil {
		return nil, fmt.Errorf("xiaoheihe API missing result")
	}
	return result, nil
}

const xiaoheiheSignTable = "AB45STUVWZEFGJ6CH01D237IXYPQRKLMN89"

func xiaoheiheSign(path string) map[string]string {
	now := time.Now().Unix()
	seed := make([]byte, 16)
	_, _ = rand.Read(seed)
	nonceSum := md5.Sum([]byte(strconv.FormatInt(now, 10) + hex.EncodeToString(seed)))
	nonce := strings.ToUpper(hex.EncodeToString(nonceSum[:]))
	return map[string]string{
		"hkey":  xiaoheiheOV(path, now+1, nonce),
		"_time": strconv.FormatInt(now, 10),
		"nonce": nonce,
	}
}

func xiaoheiheOV(path string, timestamp int64, nonce string) string {
	path = "/" + strings.Join(strings.FieldsFunc(path, func(r rune) bool { return r == '/' }), "/") + "/"
	mapped := []string{
		xiaoheiheAV(strconv.FormatInt(timestamp, 10), -2),
		xiaoheiheSV(path),
		xiaoheiheSV(nonce),
	}
	interleaved := xiaoheiheInterleave(mapped)
	if len(interleaved) > 20 {
		interleaved = interleaved[:20]
	}
	md5Hex := fmt.Sprintf("%x", md5.Sum([]byte(interleaved)))
	tail := []int{}
	for _, ch := range md5Hex[len(md5Hex)-6:] {
		tail = append(tail, int(ch))
	}
	mixed := xiaoheiheMixColumns(tail)
	total := 0
	for _, v := range mixed {
		total += v
	}
	return xiaoheiheAV(md5Hex[:5], -4) + fmt.Sprintf("%02d", total%100)
}

func xiaoheiheAV(text string, cut int) string {
	table := xiaoheiheSignTable
	if cut < 0 {
		table = table[:len(table)+cut]
	} else {
		table = table[:cut]
	}
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		out = append(out, table[int(text[i])%len(table)])
	}
	return string(out)
}

func xiaoheiheSV(text string) string {
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		out = append(out, xiaoheiheSignTable[int(text[i])%len(xiaoheiheSignTable)])
	}
	return string(out)
}

func xiaoheiheInterleave(values []string) string {
	maxLen := 0
	for _, value := range values {
		if len(value) > maxLen {
			maxLen = len(value)
		}
	}
	var b strings.Builder
	for i := 0; i < maxLen; i++ {
		for _, value := range values {
			if i < len(value) {
				b.WriteByte(value[i])
			}
		}
	}
	return b.String()
}

func xiaoheiheXTime(value int) int {
	if value&128 != 0 {
		return ((value << 1) ^ 27) & 0xff
	}
	return value << 1
}

func xiaoheiheMul3(value int) int  { return xiaoheiheXTime(value) ^ value }
func xiaoheiheMul6(value int) int  { return xiaoheiheMul3(xiaoheiheXTime(value)) }
func xiaoheiheMul12(value int) int { return xiaoheiheMul6(xiaoheiheMul3(xiaoheiheXTime(value))) }
func xiaoheiheMul14(value int) int {
	return xiaoheiheMul12(value) ^ xiaoheiheMul6(value) ^ xiaoheiheMul3(value)
}

func xiaoheiheMixColumns(col []int) []int {
	for len(col) < 4 {
		col = append(col, 0)
	}
	out := []int{
		xiaoheiheMul14(col[0]) ^ xiaoheiheMul12(col[1]) ^ xiaoheiheMul6(col[2]) ^ xiaoheiheMul3(col[3]),
		xiaoheiheMul3(col[0]) ^ xiaoheiheMul14(col[1]) ^ xiaoheiheMul12(col[2]) ^ xiaoheiheMul6(col[3]),
		xiaoheiheMul6(col[0]) ^ xiaoheiheMul3(col[1]) ^ xiaoheiheMul14(col[2]) ^ xiaoheiheMul12(col[3]),
		xiaoheiheMul12(col[0]) ^ xiaoheiheMul6(col[1]) ^ xiaoheiheMul3(col[2]) ^ xiaoheiheMul14(col[3]),
	}
	return append(out, col[4:]...)
}

func logrusWarnXiaoheiheBBSAPI(linkID string, err error) {
	logrus.Warnf("[mediaparser] xiaoheihe_bbs_api_failed link_id=%s error=%v; falling back to share metadata only", linkID, err)
}

func xiaoheiheBBSRedirectData(raw string, headers map[string]string) map[string]any {
	resp, err := httpRequestNoRedirect(raw, headers)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil
	}
	u, err := url.Parse(loc)
	if err != nil {
		return nil
	}
	data := u.Query().Get("redirect_data")
	if data == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(data), &out); err == nil {
		return out
	}
	if decoded, err := url.QueryUnescape(data); err == nil {
		_ = json.Unmarshal([]byte(decoded), &out)
	}
	return out
}

func xiaoheiheBBSImageGroups(html string) [][]string {
	raws := regexp.MustCompile(`https?://[^"'\s<>\\]+\.(?:jpg|jpeg|png|webp)(?:\?[^"'\s<>\\]*)?`).FindAllString(html, -1)
	groups := [][]string{}
	for _, raw := range raws {
		u := htmlUnescape(strings.ReplaceAll(strings.ReplaceAll(raw, `\u002F`, "/"), `\/`, "/"))
		low := strings.ToLower(u)
		if strings.Contains(low, "imgheybox") || strings.Contains(low, "max-c.com") {
			groups = append(groups, []string{ensureHTTPS(u)})
		}
	}
	return uniqueURLGroups(groups)
}

func uniqueURLGroups(in [][]string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	for _, group := range in {
		clean := []string{}
		for _, raw := range group {
			raw = strings.TrimSpace(raw)
			if raw == "" || seen[raw] {
				continue
			}
			seen[raw] = true
			clean = append(clean, raw)
		}
		if len(clean) > 0 {
			out = append(out, clean)
		}
	}
	return out
}

func xiaoheiheAppIDGameType(raw string) (int, string) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "api.xiaoheihe.cn") && strings.Contains(u.Path, "/game/share_game_detail") {
		gameType := firstNonEmpty(u.Query().Get("game_type"), "pc")
		appid, _ := strconv.Atoi(u.Query().Get("appid"))
		return appid, strings.ToLower(gameType)
	}
	if strings.Contains(host, "xiaoheihe.cn") || strings.Contains(host, "heybox.cn") {
		if m := regexp.MustCompile(`(?i)/app/topic/game/([^/]+)/(\d+)`).FindStringSubmatch(u.Path); len(m) > 2 {
			appid, _ := strconv.Atoi(m[2])
			return appid, strings.ToLower(m[1])
		}
	}
	return 0, ""
}

func xiaoheiheBBSLinkID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"link_id", "linkid", "id"} {
		if value := q.Get(key); value != "" {
			return value
		}
	}
	if strings.Contains(strings.ToLower(u.Hostname()), "xiaoheihe.cn") {
		if m := regexp.MustCompile(`(?i)/bbs/link/([^/?#]+)`).FindStringSubmatch(u.Path); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func xiaoheiheCanonicalGameURL(appid int, gameType string) string {
	gameType = strings.TrimSpace(strings.ToLower(gameType))
	if gameType == "" {
		gameType = "pc"
	}
	return fmt.Sprintf("https://www.xiaoheihe.cn/app/topic/game/%s/%d", gameType, appid)
}

func buildXiaoheiheGameMeta(ctx xiaoheiheContext, html string) (mediaMeta, error) {
	title := xiaoheiheTitle(html)
	desc := xiaoheiheDesc(html)
	videos := xiaoheiheVideoGroups(html)
	images := xiaoheiheImageGroups(html)
	types := xiaoheiheTypes(html)
	if types != "" {
		desc = strings.TrimSpace(strings.Join(uniqueStrings([]string{types, desc}), "\n"))
	}
	if title == "" && len(videos) == 0 && len(images) == 0 {
		return mediaMeta{}, fmt.Errorf("xiaoheihe page did not expose usable media")
	}
	referer := "https://store.steampowered.com/"
	meta := mediaMeta{
		URL:        ctx.PageURL,
		SourceURL:  ctx.SourceURL,
		Platform:   "xiaoheihe",
		Title:      title,
		Desc:       desc,
		Cover:      firstNonEmpty(og(html, "image"), firstNestedHTTPURLByKeys(html, 2, "cover", "thumb", "image")),
		VideoURLs:  videos,
		ImageURLs:  images,
		VideoHeads: buildHeaders(true, referer, xiaoheiheUA),
		ImageHeads: buildHeaders(false, referer, xiaoheiheUA),
		ForceLocal: len(videos) > 0,
	}
	if len(videos) > 0 || len(images) > 0 {
		meta.HasValidMedia = true
	}
	return meta, nil
}

func xiaoheiheTitle(html string) string {
	fromJSON := firstStringByJSONKey(html, "name", "name_en", "title")
	title := firstNonEmpty(og(html, "title"), titleTag(html), fromJSON)
	title = strings.TrimSpace(strings.TrimSuffix(title, "- 小黑盒"))
	title = strings.TrimSpace(strings.TrimSuffix(title, "_小黑盒"))
	return title
}

func xiaoheiheDesc(html string) string {
	desc := firstNonEmpty(metaName(html, "description"), og(html, "description"))
	if desc == "" {
		desc = firstStringByJSONKey(html, "about_the_game", "desc", "description", "summary")
	}
	desc = cleanHTMLText(strings.ReplaceAll(desc, `\n`, "\n"))
	if len([]rune(desc)) > 900 {
		desc = string([]rune(desc)[:900]) + "..."
	}
	return desc
}

func xiaoheiheVideoGroups(html string) [][]string {
	seen := map[string]bool{}
	groups := [][]string{}
	for _, m := range regexp.MustCompile(`https?://[^"'\s<>\\]+\.m3u8(?:\?[^"'\s<>\\]*)?`).FindAllString(html, -1) {
		u := strings.ReplaceAll(htmlUnescape(m), `\u002F`, "/")
		if !seen[u] {
			seen[u] = true
			groups = append(groups, []string{"m3u8:" + u})
		}
	}
	for _, m := range regexp.MustCompile(`https?://[^"'\s<>\\]+\.mp4(?:\?[^"'\s<>\\]*)?`).FindAllString(html, -1) {
		u := strings.ReplaceAll(htmlUnescape(m), `\u002F`, "/")
		if !seen[u] {
			seen[u] = true
			groups = append(groups, []string{u})
		}
	}
	return groups
}

func xiaoheiheImageGroups(html string) [][]string {
	raws := regexp.MustCompile(`https?://[^"'\s<>\\]+\.(?:jpg|jpeg|png|webp)(?:\?[^"'\s<>\\]*)?`).FindAllString(html, -1)
	groups := [][]string{}
	seen := map[string]bool{}
	for _, raw := range raws {
		u := htmlUnescape(strings.ReplaceAll(strings.ReplaceAll(raw, `\u002F`, "/"), `\/`, "/"))
		low := strings.ToLower(u)
		if seen[u] || strings.Contains(low, "/thumbnail/") {
			continue
		}
		if !strings.Contains(low, "gameimg") &&
			!strings.Contains(low, "steam_item_assets") &&
			!strings.Contains(low, "screenshot") &&
			!strings.Contains(low, "game") {
			continue
		}
		seen[u] = true
		groups = append(groups, []string{u})
	}
	return groups
}

func xiaoheiheTypes(html string) string {
	m := regexp.MustCompile(`(?is)<div class="row-2">.*?<div class="tags">(.*?)</div>\s*</div>`).FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	tagsHTML := m[1]
	parts := []string{}
	if common := regexp.MustCompile(`(?is)<div class="tag common"[^>]*>(.*?)</div>`).FindStringSubmatch(tagsHTML); len(common) > 1 {
		tokens := []string{}
		for _, span := range regexp.MustCompile(`(?is)<span[^>]*>(.*?)</span>`).FindAllStringSubmatch(common[1], -1) {
			if len(span) > 1 {
				t := regexp.MustCompile(`[^\p{Han}A-Za-z0-9]+`).ReplaceAllString(cleanHTMLText(span[1]), "")
				if t != "" {
					tokens = append(tokens, t)
				}
			}
		}
		if len(tokens) > 0 {
			parts = append(parts, "[ "+strings.Join(tokens, " ")+" ]")
		}
	}
	tagTexts := []string{}
	for _, tag := range regexp.MustCompile(`(?is)<p class="tag"[^>]*>(.*?)</p>`).FindAllStringSubmatch(tagsHTML, -1) {
		if len(tag) > 1 {
			if t := cleanHTMLText(tag[1]); t != "" {
				tagTexts = append(tagTexts, t)
			}
		}
	}
	if len(tagTexts) > 0 {
		parts = append(parts, "[ "+strings.Join(uniqueStrings(tagTexts), " ")+" ]")
	}
	return strings.Join(parts, " ")
}

func firstStringByJSONKey(html string, keys ...string) string {
	candidates := []string{html}
	if data := extractScriptJSON(html, "__NUXT_DATA__"); data != "" {
		candidates = append([]string{data}, candidates...)
	}
	for _, src := range candidates {
		for _, key := range keys {
			pattern := `"` + regexp.QuoteMeta(key) + `"\s*:\s*"((?:\\.|[^"\\])*)"`
			for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(src, -1) {
				if len(m) < 2 {
					continue
				}
				value := decodeJSONString(m[1])
				value = cleanHTMLText(value)
				if value != "" && !strings.HasPrefix(value, "http") {
					return value
				}
			}
		}
	}
	return ""
}

func decodeJSONString(raw string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &out); err == nil {
		return out
	}
	return raw
}
