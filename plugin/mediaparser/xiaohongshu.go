package mediaparser

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	xhsAndroidUA = "Mozilla/5.0 (Linux; Android 6.0; Nexus 5 Build/MRA58N) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36 Edg/142.0.0.0"
	xhsPCUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0"
)

func parseXiaohongshu(cfg config, raw string) (mediaMeta, error) {
	fullURL := raw
	if strings.Contains(strings.ToLower(raw), "xhslink.com") {
		headers := xhsPageHeaders(cfg, raw)
		reqURL, err := xhsRedirect(raw, headers)
		if err != nil {
			return mediaMeta{}, err
		}
		fullURL = reqURL
	}
	fullURL = xhsCleanShareURL(fullURL)
	fetchURL := firstNonEmpty(xhsPreferPCURL(fullURL), fullURL)
	headers := xhsPageHeaders(cfg, fetchURL)
	html, finalURL, status, err := fetchText(fetchURL, headers, true)
	if err != nil {
		if fetchURL != fullURL {
			headers = xhsPageHeaders(cfg, fullURL)
			html, finalURL, status, err = fetchText(fullURL, headers, true)
		}
		if err != nil {
			return mediaMeta{}, err
		}
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("小红书页面 HTTP %d", status)
	}
	state, err := extractAssignedJSONObject(html, "window.__INITIAL_STATE__")
	if err != nil {
		return mediaMeta{}, err
	}
	note, err := xhsFindNote(state)
	if err != nil {
		return mediaMeta{}, err
	}
	user := getMap(note, "user")
	authorName := firstNonEmpty(getString(user, "nickName"), getString(user, "nickname"))
	authorID := getString(user, "userId")
	author := authorName
	if authorID != "" {
		if author != "" {
			author += "(主页id:" + authorID + ")"
		} else {
			author = "(主页id:" + authorID + ")"
		}
	}
	author = cardDisplayAuthor(author)
	noteType := getString(note, "type")
	title := getString(note, "title")
	desc := getString(note, "desc")
	publish := ""
	if ts := getFloat(note, "time"); ts > 0 {
		publish = time.Unix(int64(ts/1000), 0).Format("2006-01-02")
	}
	avatar := firstNestedHTTPURL(user, 5)
	imageGroups := xhsExtractImageGroups(note)
	cover := xhsVideoCover(note, imageGroups)
	videoGroups := [][]string{}
	if noteType == "video" {
		videoURL := xhsPickVideoURL(cfg, note)
		if videoURL == "" {
			videoURL = firstNonEmpty(nestedHTTPURLs(getMap(note, "video"), 6)...)
		}
		if videoURL == "" {
			return mediaMeta{}, fmt.Errorf("小红书未找到视频URL")
		}
		videoGroups = append(videoGroups, []string{ensureHTTPS(videoURL)})
	} else {
		if len(imageGroups) == 0 {
			return mediaMeta{}, fmt.Errorf("小红书未找到图片URL")
		}
	}
	imageGroups = dedupeMediaGroups(imageGroups)
	if cover == "" && len(imageGroups) > 0 && len(imageGroups[0]) > 0 {
		cover = imageGroups[0][0]
	}
	if noteType == "video" {
		imageGroups = nil
	}
	ua := xhsAndroidUA
	if xhsIsPCURL(finalURL) {
		ua = xhsPCUA
	}
	return mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "xiaohongshu",
		Title:      title,
		Author:     author,
		Avatar:     avatar,
		Desc:       desc,
		Timestamp:  publish,
		Cover:      cover,
		VideoURLs:  videoGroups,
		ImageURLs:  imageGroups,
		VideoHeads: buildHeaders(true, finalURL, ua),
		ImageHeads: buildHeaders(false, finalURL, ua),
	}, nil
}

type xhsVideoCandidate struct {
	URL     string
	Height  int
	Bitrate int
}

func xhsPickVideoURL(cfg config, note map[string]any) string {
	stream := getMap(note, "video", "media", "stream")
	candidates := []xhsVideoCandidate{}
	for _, key := range []string{"h264", "h264_mp4"} {
		for _, item := range getSlice(stream, key) {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			raw := firstNonEmpty(
				getString(m, "masterUrl"),
				getString(m, "backupUrl"),
				getString(m, "url"),
			)
			if raw == "" {
				raw = firstNestedHTTPURL(m, 4)
			}
			if raw == "" {
				continue
			}
			height := int(firstPositiveFloat(
				getFloat(m, "height"),
				getFloat(m, "videoHeight"),
				getFloat(m, "quality", "height"),
			))
			bitrate := int(firstPositiveFloat(
				getFloat(m, "avgBitrate"),
				getFloat(m, "bitrate"),
			))
			candidates = append(candidates, xhsVideoCandidate{URL: ensureHTTPS(raw), Height: height, Bitrate: bitrate})
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if xhsVideoBetter(c, best, cfg.VideoMaxResolution) {
			best = c
		}
	}
	return best.URL
}

func xhsVideoBetter(a, b xhsVideoCandidate, maxHeight int) bool {
	if maxHeight > 0 {
		aOK := a.Height > 0 && a.Height <= maxHeight
		bOK := b.Height > 0 && b.Height <= maxHeight
		if aOK != bOK {
			return aOK
		}
		if aOK && bOK && a.Height != b.Height {
			return a.Height > b.Height
		}
		if !aOK && !bOK && a.Height > 0 && b.Height > 0 && a.Height != b.Height {
			return a.Height < b.Height
		}
	}
	if a.Height != b.Height {
		return a.Height > b.Height
	}
	return a.Bitrate > b.Bitrate
}

func xhsVideoCover(note map[string]any, imageGroups [][]string) string {
	video := getMap(note, "video")
	for _, raw := range []string{
		firstNestedHTTPURL(getMap(video, "image"), 5),
		firstNestedHTTPURLByKeys(video, 6, "cover", "image", "first", "thumbnail", "poster"),
	} {
		if raw = ensureHTTPS(raw); raw != "" && !xhsBadImageURL(raw) {
			return raw
		}
	}
	if len(imageGroups) > 0 && len(imageGroups[0]) > 0 {
		return imageGroups[0][0]
	}
	return ""
}

func xhsExtractImageGroups(note map[string]any) [][]string {
	groups := [][]string{}
	seen := map[string]bool{}
	for _, item := range getSlice(note, "imageList") {
		img, ok := item.(map[string]any)
		if !ok {
			continue
		}
		candidates := xhsImageURLCandidates(img)
		if len(candidates) == 0 || seen[candidates[0]] {
			continue
		}
		seen[candidates[0]] = true
		groups = append(groups, candidates)
	}
	return groups
}

func xhsImageURLCandidates(img map[string]any) []string {
	candidates := []string{}
	add := func(raw string) {
		raw = ensureHTTPS(raw)
		if raw == "" || xhsBadImageURL(raw) {
			return
		}
		for _, existing := range candidates {
			if existing == raw {
				return
			}
		}
		candidates = append(candidates, raw)
	}
	for _, scene := range []string{"WB_DFT", "CRD_WM_WEBP", "CRD_PRV_WEBP"} {
		for _, info := range getSlice(img, "infoList") {
			m, ok := info.(map[string]any)
			if ok && getString(m, "imageScene") == scene {
				add(getString(m, "url"))
			}
		}
	}
	add(getString(img, "urlDefault"))
	add(getString(img, "url"))
	for _, raw := range nestedHTTPURLs(img, 4) {
		add(raw)
	}
	return candidates
}

func xhsBadImageURL(raw string) bool {
	low := strings.ToLower(raw)
	return strings.Contains(low, "picasso-static") || strings.Contains(low, "fe-platform")
}

func dedupeMediaGroups(groups [][]string) [][]string {
	seen := map[string]bool{}
	out := make([][]string, 0, len(groups))
	for _, group := range groups {
		if len(group) == 0 || group[0] == "" || seen[group[0]] {
			continue
		}
		group = dedupeMediaGroup(group)
		if len(group) == 0 {
			continue
		}
		seen[group[0]] = true
		out = append(out, group)
	}
	return out
}

func dedupeMediaGroup(group []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(group))
	for _, raw := range group {
		raw = ensureHTTPS(raw)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, raw)
	}
	return out
}

func firstPositiveFloat(values ...float64) float64 {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func xhsFindNote(state map[string]any) (map[string]any, error) {
	if note := getMap(state, "noteData", "data", "noteData"); note != nil {
		return note, nil
	}
	detailMap := getMap(state, "note", "noteDetailMap")
	for _, item := range detailMap {
		if detail, ok := item.(map[string]any); ok {
			if note := getMap(detail, "note"); note != nil {
				return note, nil
			}
		}
	}
	return nil, fmt.Errorf("无法找到小红书笔记数据")
}

func xhsRedirect(raw string, headers map[string]string) (string, error) {
	req, err := httpRequestNoRedirect(raw, headers)
	if err != nil {
		return "", err
	}
	defer req.Body.Close()
	if loc := req.Header.Get("Location"); loc != "" {
		return url.QueryUnescape(loc)
	}
	if req.Request != nil && req.Request.URL != nil {
		return req.Request.URL.String(), nil
	}
	return "", fmt.Errorf("无法获取小红书短链跳转")
}

func httpRequestNoRedirect(raw string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c.Do(req)
}

func xhsPageHeaders(cfg config, raw string) map[string]string {
	ua := xhsAndroidUA
	if xhsIsPCURL(raw) {
		ua = xhsPCUA
	}
	headers := map[string]string{
		"User-Agent":      ua,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9",
	}
	if cfg.XiaohongshuCookie != "" {
		headers["Cookie"] = cfg.XiaohongshuCookie
	}
	return headers
}

func xhsIsPCURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "/explore/") || strings.Contains(lower, "xsec_source=pc")
}

func xhsCleanShareURL(raw string) string {
	if xhsIsPCURL(raw) || !strings.Contains(raw, "discovery/item") {
		return raw
	}
	return cleanURLQuery(raw, "source", "xhsshare")
}

func xhsPreferPCURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || xhsIsPCURL(raw) {
		return ""
	}
	m := regexp.MustCompile(`(?i)/discovery/item/([^/?#]+)`).FindStringSubmatch(u.Path)
	if len(m) < 2 || u.Query().Get("xsec_token") == "" {
		return ""
	}
	q := url.Values{}
	q.Set("xsec_token", u.Query().Get("xsec_token"))
	q.Set("xsec_source", "pc_share")
	return "https://www.xiaohongshu.com/explore/" + m[1] + "?" + q.Encode()
}

func cleanXHSTopicTags(s string) string {
	// Close enough to the original: "#标签[话题]#" -> "#标签".
	re := regexp.MustCompile(`#([^#\[]+)\[话题\]#`)
	return re.ReplaceAllString(s, "#$1")
}
