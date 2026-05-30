package mediaparser

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const weiboUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0"

func parseWeibo(cfg config, raw string) (mediaMeta, error) {
	cookies := getWeiboVisitorCookies()
	switch {
	case regexp.MustCompile(`m\.weibo\.cn/(?:status|detail|\d+/)[0-9A-Za-z]+`).MatchString(raw):
		return parseMobileWeibo(raw)
	case regexp.MustCompile(`weibo\.com/\d+/[A-Za-z0-9]+`).MatchString(raw) || regexp.MustCompile(`weibo\.cn/status/\d+`).MatchString(raw):
		return parseWebWeibo(raw, cookies)
	default:
		return parseWeiboPageFallback(raw, cookies)
	}
}

func getWeiboVisitorCookies() string {
	req, _ := http.NewRequest(http.MethodPost, "https://visitor.passport.weibo.cn/visitor/genvisitor2", strings.NewReader("cb=visitor_gray_callback"))
	req.Header.Set("User-Agent", weiboUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	cookies := []string{}
	for _, c := range resp.Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	if len(cookies) == 0 {
		return ""
	}
	return strings.Join(cookies, "; ")
}

func parseWebWeibo(raw, cookies string) (mediaMeta, error) {
	id := ""
	if m := regexp.MustCompile(`/([A-Za-z0-9]+)$`).FindStringSubmatch(strings.TrimRight(raw, "/")); len(m) > 1 {
		id = m[1]
	}
	if id == "" {
		return mediaMeta{}, fmt.Errorf("无法提取微博ID")
	}
	if meta, err := parseWeiboIDMobile(raw, id); err == nil {
		return meta, nil
	}
	api := "https://weibo.com/ajax/statuses/show?id=" + id + "&locale=zh-CN&isGetLongText=true"
	var data map[string]any
	if err := weiboJSON(api, raw, cookies, &data); err != nil {
		return mediaMeta{}, err
	}
	if d := getMap(data, "data"); d != nil {
		data = d
	}
	return buildWeiboMeta(raw, data), nil
}

func parseMobileWeibo(raw string) (mediaMeta, error) {
	id := ""
	if m := regexp.MustCompile(`/(?:status|detail|\d+/)([0-9A-Za-z]+)`).FindStringSubmatch(raw); len(m) > 1 {
		id = m[1]
	}
	if id == "" {
		return mediaMeta{}, fmt.Errorf("无法提取 m.weibo.cn ID")
	}
	return parseWeiboIDMobile(raw, id)
}

func parseWeiboIDMobile(raw, id string) (mediaMeta, error) {
	api := "https://m.weibo.cn/statuses/show?id=" + id + "&_=" + timestampMS()
	var data map[string]any
	if err := weiboJSONWithHeaders(api, weiboMobileHeaders(raw), &data); err != nil {
		return mediaMeta{}, err
	}
	if d := getMap(data, "data"); d != nil {
		data = d
	}
	return buildWeiboMeta(raw, data), nil
}

func parseWeiboPageFallback(raw, cookies string) (mediaMeta, error) {
	headers := weiboHeaders(raw, cookies)
	html, finalURL, status, err := fetchText(raw, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("微博页面 HTTP %d", status)
	}
	videoGroups := [][]string{}
	imageGroups := [][]string{}
	for _, u := range regexp.MustCompile(`https?://[^"']+?\.mp4[^"']*`).FindAllString(html, -1) {
		videoGroups = append(videoGroups, []string{strings.ReplaceAll(u, `\/`, `/`)})
	}
	for _, u := range regexp.MustCompile(`https?://[^"']+?\.(?:jpg|jpeg|png|webp)[^"']*`).FindAllString(html, -1) {
		imageGroups = append(imageGroups, []string{strings.ReplaceAll(u, `\/`, `/`)})
	}
	if len(videoGroups)+len(imageGroups) == 0 {
		return mediaMeta{}, fmt.Errorf("微博页面未找到媒体")
	}
	return mediaMeta{
		URL:        finalURL,
		SourceURL:  raw,
		Platform:   "weibo",
		Title:      "",
		Desc:       "",
		VideoURLs:  videoGroups,
		ImageURLs:  imageGroups,
		VideoHeads: buildHeaders(true, "https://weibo.com/", weiboUA),
		ImageHeads: buildHeaders(false, "https://weibo.com/", weiboUA),
		ForceLocal: len(videoGroups) > 0,
	}, nil
}

func weiboJSON(api, referer, cookies string, out any) error {
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	for k, v := range weiboHeaders(referer, cookies) {
		req.Header.Set(k, v)
	}
	return doWeiboJSON(req, out)
}

func weiboJSONWithHeaders(api string, headers map[string]string, out any) error {
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doWeiboJSON(req, out)
}

func doWeiboJSON(req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("微博 API HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

func weiboMobileHeaders(referer string) map[string]string {
	return map[string]string{
		"User-Agent":       weiboUA,
		"Referer":          firstNonEmpty(referer, "https://m.weibo.cn/"),
		"Origin":           "https://m.weibo.cn",
		"Accept":           "application/json, text/plain, */*",
		"X-Requested-With": "XMLHttpRequest",
		"MWeibo-Pwa":       "1",
		"Sec-Fetch-Site":   "same-origin",
		"Sec-Fetch-Mode":   "cors",
		"Sec-Fetch-Dest":   "empty",
		"Accept-Language":  "zh-CN,zh;q=0.9",
	}
}

func weiboHeaders(referer, cookies string) map[string]string {
	h := map[string]string{
		"User-Agent":       weiboUA,
		"Referer":          referer,
		"Accept":           "application/json, text/plain, */*",
		"X-Requested-With": "XMLHttpRequest",
		"Accept-Language":  "zh-CN,zh;q=0.9",
	}
	if cookies != "" {
		h["Cookie"] = cookies
	}
	if m := regexp.MustCompile(`(?:^|;\s*)XSRF-TOKEN=([^;]+)`).FindStringSubmatch(cookies); len(m) > 1 {
		h["X-XSRF-TOKEN"] = m[1]
	}
	return h
}

func buildWeiboMeta(raw string, data map[string]any) mediaMeta {
	user := getMap(data, "user")
	author := firstNonEmpty(getString(user, "screen_name"), getString(user, "name"))
	if uid := getString(user, "id"); author != "" && uid != "" {
		author += "(uid:" + uid + ")"
	}
	desc := cleanHTMLText(firstNonEmpty(getString(data, "text_raw"), getString(data, "text")))
	videoGroups, imageGroups := extractWeiboMedia(data)
	cover := ""
	if len(imageGroups) > 0 && len(imageGroups[0]) > 0 {
		cover = imageGroups[0][0]
	}
	if cover == "" {
		cover = firstNestedHTTPURLByKeys(data, 5, "cover", "thumbnail", "page_pic", "pic")
	}
	items := mediaItemsFor(videoGroups, imageGroups)
	if len(videoGroups) > 0 && len(imageGroups) > 0 && isWeiboContentImageURL(strings.ToLower(cover)) && !hasURLGroup(imageGroups, cover) {
		imageGroups = append(imageGroups, []string{cover})
	}
	return mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "weibo",
		Title:      "",
		Author:     author,
		Avatar:     firstNestedHTTPURLByKeys(user, 4, "avatar", "profile_image", "face"),
		Desc:       desc,
		Timestamp:  parseWeiboTime(getString(data, "created_at")),
		Cover:      cover,
		VideoURLs:  videoGroups,
		ImageURLs:  imageGroups,
		MediaItems: items,
		VideoHeads: buildHeaders(true, "https://weibo.com/", weiboUA),
		ImageHeads: buildHeaders(false, "https://weibo.com/", weiboUA),
		ForceLocal: len(videoGroups) > 0,
	}
}

func extractWeiboMedia(data map[string]any) ([][]string, [][]string) {
	videos := [][]string{}
	images := [][]string{}
	if u := videoURLFromMediaInfo(getMap(data, "page_info", "media_info")); u != "" {
		videos = append(videos, []string{ensureHTTPS(u)})
	}
	if u := videoURLFromMediaInfo(getMap(data, "media_info")); u != "" {
		videos = append(videos, []string{ensureHTTPS(u)})
	}
	for _, pic := range getSlice(data, "pics") {
		if m, ok := pic.(map[string]any); ok {
			if u := weiboPicURL(m); u != "" {
				images = append(images, []string{ensureHTTPS(u)})
			}
		}
	}
	if len(images) == 0 {
		infos := getMap(data, "pic_infos")
		for _, id := range getStringList(data, "pic_ids") {
			if m := getMap(infos, id); m != nil {
				if u := weiboPicURL(m); u != "" {
					images = append(images, []string{ensureHTTPS(u)})
				}
			}
		}
	}
	if len(images) == 0 {
		for _, u := range nestedHTTPURLs(data, 5) {
			l := strings.ToLower(u)
			switch {
			case strings.Contains(l, ".mp4") || strings.Contains(l, "stream"):
				videos = append(videos, []string{ensureHTTPS(u)})
			case isWeiboContentImageURL(l):
				images = append(images, []string{ensureHTTPS(u)})
			}
		}
	}
	return uniqueGroups(videos), uniqueGroups(images)
}

func videoURLFromMediaInfo(m map[string]any) string {
	if m == nil {
		return ""
	}
	return firstNonEmpty(
		getString(m, "hd_url"),
		getString(m, "stream_url_hd"),
		getString(m, "stream_url"),
		getString(m, "mp4_hd_url"),
		getString(m, "mp4_sd_url"),
	)
}

func weiboPicURL(m map[string]any) string {
	for _, key := range []string{"largest", "original", "large", "mw2000", "mw1024", "bmiddle"} {
		if u := getString(m, key, "url"); u != "" {
			return u
		}
	}
	return getString(m, "url")
}

func getStringList(v any, keys ...string) []string {
	raw := getSlice(v, keys...)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		switch x := item.(type) {
		case string:
			if x != "" {
				out = append(out, x)
			}
		case float64:
			out = append(out, fmt.Sprintf("%.0f", x))
		case json.Number:
			out = append(out, x.String())
		}
	}
	return out
}

func isWeiboContentImageURL(lowerURL string) bool {
	if !(strings.Contains(lowerURL, ".jpg") || strings.Contains(lowerURL, ".jpeg") || strings.Contains(lowerURL, ".png") || strings.Contains(lowerURL, ".webp")) {
		return false
	}
	if strings.Contains(lowerURL, "avatar") || strings.Contains(lowerURL, "profile") || strings.Contains(lowerURL, "face") || strings.Contains(lowerURL, "logo") || strings.Contains(lowerURL, "icon") {
		return false
	}
	return strings.Contains(lowerURL, "sinaimg.cn/large/") ||
		strings.Contains(lowerURL, "sinaimg.cn/mw2000/") ||
		strings.Contains(lowerURL, "sinaimg.cn/mw1024/") ||
		strings.Contains(lowerURL, "sinaimg.cn/orj")
}

func uniqueGroups(groups [][]string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	for _, group := range groups {
		if len(group) == 0 || group[0] == "" || seen[group[0]] {
			continue
		}
		seen[group[0]] = true
		out = append(out, group)
	}
	return out
}

func hasURLGroup(groups [][]string, raw string) bool {
	raw = ensureHTTPS(raw)
	for _, group := range groups {
		if len(group) > 0 && ensureHTTPS(group[0]) == raw {
			return true
		}
	}
	return false
}

func cleanHTMLText(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	return strings.TrimSpace(htmlUnescape(s))
}

func parseWeiboTime(raw string) string {
	if raw == "" {
		return ""
	}
	if t, err := time.Parse("Mon Jan 02 15:04:05 -0700 2006", raw); err == nil {
		return t.Format("2006-01-02")
	}
	return raw
}
