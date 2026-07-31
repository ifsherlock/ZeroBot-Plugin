package mediaparser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	sourceURL := linuxdoMainPostURL(raw, topicID)
	postNumber := "1"
	if strings.TrimSpace(cfg.LinuxdoFlaresolverrURL) != "" {
		if meta, err := linuxdoParseWithFlaresolverr(cfg, sourceURL, topicID, postNumber); err == nil {
			return meta, nil
		} else {
			logDebug(cfg, "linuxdo_flaresolverr_failed url=%s error=%v", raw, err)
		}
	}
	api := linuxdoBase + "/t/" + topicID + ".json"
	headers := linuxdoHeaders(cfg, sourceURL)
	body, finalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", api, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		if htmlMeta, htmlErr := linuxdoParseHTMLFallback(cfg, sourceURL, topicID, postNumber); htmlErr == nil {
			return htmlMeta, nil
		} else {
			return mediaMeta{}, fmt.Errorf("linux.do API HTTP %d final=%s %s request=%s; html fallback: %v", status, finalURL, linuxdoErrorSummary(body), linuxdoRequestSummary(cfg), htmlErr)
		}
	}
	if postNumber != "" {
		if body, finalURL, err = linuxdoEnsurePostLoaded(cfg, sourceURL, body, postNumber, finalURL); err != nil {
			return mediaMeta{}, err
		}
	}
	meta, err := parseLinuxdoTopicJSON(sourceURL, finalURL, body)
	if err != nil {
		return mediaMeta{}, err
	}
	linuxdoApplyMetaHeaders(cfg, &meta)
	return meta, nil
}

func linuxdoApplyMetaHeaders(cfg config, meta *mediaMeta) {
	ua := linuxdoUserAgent(cfg)
	meta.VideoHeads = buildHeaders(true, linuxdoReferer, ua)
	meta.ImageHeads = buildHeaders(false, linuxdoReferer, ua)
	if cfg.LinuxdoCookie != "" {
		meta.VideoHeads["Cookie"] = cfg.LinuxdoCookie
		meta.ImageHeads["Cookie"] = cfg.LinuxdoCookie
	}
}

type linuxdoFlaresolverrRequest struct {
	Cmd        string                      `json:"cmd"`
	URL        string                      `json:"url"`
	MaxTimeout int                         `json:"maxTimeout"`
	Wait       int                         `json:"wait,omitempty"`
	Cookies    []linuxdoFlaresolverrCookie `json:"cookies,omitempty"`
	Proxy      *linuxdoFlaresolverrProxy   `json:"proxy,omitempty"`
}

type linuxdoFlaresolverrCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain,omitempty"`
	Path   string `json:"path,omitempty"`
}

type linuxdoFlaresolverrProxy struct {
	URL string `json:"url"`
}

type linuxdoFlaresolverrResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string `json:"url"`
		Status    int    `json:"status"`
		Response  string `json:"response"`
		UserAgent string `json:"userAgent"`
	} `json:"solution"`
}

func linuxdoParseWithFlaresolverr(cfg config, sourceURL, topicID, postNumber string) (mediaMeta, error) {
	pageURL := linuxdoTopicPageURL(sourceURL, topicID, postNumber)
	htmlBody, finalURL, err := linuxdoFetchWithFlaresolverr(cfg, pageURL)
	if err != nil {
		return mediaMeta{}, err
	}
	if topic := linuxdoExtractTopicJSONFromHTML(htmlBody); topic != nil {
		logDebug(cfg, "linuxdo_flaresolverr stage=html_preloaded final=%s body_len=%d markers=%s", finalURL, len(htmlBody), linuxdoHTMLMarkerSummary(htmlBody))
		meta, err := parseLinuxdoTopicJSON(sourceURL, finalURL, mustJSON(topic))
		if err != nil {
			return mediaMeta{}, err
		}
		linuxdoMergeRenderedHTML(&meta, htmlBody, finalURL)
		linuxdoApplyMetaHeaders(cfg, &meta)
		return meta, nil
	}
	desc := linuxdoHTMLBodyText(htmlBody)
	images := linuxdoExtractImages(htmlBody, finalURL)
	logDebug(cfg, "linuxdo_flaresolverr stage=html_shell final=%s body_len=%d desc_len=%d images=%d markers=%s", finalURL, len(htmlBody), len(desc), len(images), linuxdoHTMLMarkerSummary(htmlBody))
	if strings.TrimSpace(desc) == "" && len(images) == 0 {
		return mediaMeta{}, fmt.Errorf("linux.do flaresolverr got no body: final=%s body_len=%d markers=%s", finalURL, len(htmlBody), linuxdoHTMLMarkerSummary(htmlBody))
	}
	meta := mediaMeta{
		URL:         pageURL,
		SourceURL:   sourceURL,
		Platform:    "linuxdo",
		Title:       firstNonEmpty(linuxdoHTMLTitle(htmlBody), "Linux.do Topic "+topicID),
		Desc:        desc,
		Cover:       firstImageURL(images),
		ImageURLs:   images,
		LinuxdoHTML: linuxdoPrimaryContentHTML(htmlBody),
		ImageHeads:  buildHeaders(false, linuxdoReferer, linuxdoUserAgent(cfg)),
		VideoHeads:  buildHeaders(true, linuxdoReferer, linuxdoUserAgent(cfg)),
	}
	linuxdoApplyMetaHeaders(cfg, &meta)
	return meta, nil
}

func linuxdoFetchWithFlaresolverr(cfg config, targetURL string) (string, string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.LinuxdoFlaresolverrURL), "/")
	if endpoint == "" {
		return "", targetURL, fmt.Errorf("linux.do flaresolverr url is empty")
	}
	timeoutMS := cfg.LinuxdoFlaresolverrTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	waitSec := cfg.LinuxdoFlaresolverrWaitSec
	if waitSec <= 0 {
		waitSec = 2
	}
	reqBody := linuxdoFlaresolverrRequest{
		Cmd:        "request.get",
		URL:        targetURL,
		MaxTimeout: timeoutMS,
		Wait:       waitSec,
		Cookies:    linuxdoFlaresolverrCookies(cfg.LinuxdoCookie),
	}
	if cfg.LinuxdoFlaresolverrUseProxy {
		if proxyURL := proxyForPlatform(cfg, "linuxdo"); proxyURL != "" {
			reqBody.Proxy = &linuxdoFlaresolverrProxy{URL: proxyURL}
		}
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", targetURL, err
	}
	client := &http.Client{Timeout: time.Duration(timeoutMS+10000) * time.Millisecond}
	req, err := http.NewRequest(http.MethodPost, endpoint+"/v1", bytes.NewReader(payload))
	if err != nil {
		return "", targetURL, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", targetURL, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", targetURL, err
	}
	if resp.StatusCode >= 400 {
		return "", targetURL, fmt.Errorf("linux.do flaresolverr HTTP %d body_len=%d", resp.StatusCode, len(data))
	}
	var out linuxdoFlaresolverrResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", targetURL, fmt.Errorf("linux.do flaresolverr decode: %w body_len=%d", err, len(data))
	}
	if strings.ToLower(strings.TrimSpace(out.Status)) != "ok" {
		return "", firstNonEmpty(out.Solution.URL, targetURL), fmt.Errorf("linux.do flaresolverr status=%q message=%q", out.Status, truncate(out.Message, 160))
	}
	if strings.TrimSpace(out.Solution.Response) == "" {
		return "", firstNonEmpty(out.Solution.URL, targetURL), fmt.Errorf("linux.do flaresolverr empty response status=%d", out.Solution.Status)
	}
	logDebug(cfg, "linuxdo_flaresolverr_ok final=%s status=%d body_len=%d ua_len=%d", firstNonEmpty(out.Solution.URL, targetURL), out.Solution.Status, len(out.Solution.Response), len(out.Solution.UserAgent))
	return out.Solution.Response, firstNonEmpty(out.Solution.URL, targetURL), nil
}

func linuxdoFlaresolverrCookies(raw string) []linuxdoFlaresolverrCookie {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	cookies := []linuxdoFlaresolverrCookie{}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cookies = append(cookies, linuxdoFlaresolverrCookie{
			Name:   name,
			Value:  strings.TrimSpace(value),
			Domain: "linux.do",
			Path:   "/",
		})
	}
	return cookies
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
			logDebug(cfg, "linuxdo_fallback stage=html_preloaded final=%s body_len=%d", finalURL, len(htmlBody))
			meta, err := parseLinuxdoTopicJSON(sourceURL, finalURL, mustJSON(topic))
			if err != nil {
				return mediaMeta{}, err
			}
			linuxdoMergeRenderedHTML(&meta, htmlBody, finalURL)
			linuxdoApplyMetaHeaders(cfg, &meta)
			return meta, nil
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
		logDebug(cfg, "linuxdo_fallback stage=raw final=%s raw_len=%d desc_len=%d images=%d", rawFinalURL, len(rawBody), len(desc), len(images))
		meta := mediaMeta{
			URL:        pageURL,
			SourceURL:  sourceURL,
			Platform:   "linuxdo",
			Title:      firstNonEmpty(title, "Linux.do Topic "+topicID),
			Desc:       desc,
			Cover:      firstImageURL(images),
			ImageURLs:  images,
			ImageHeads: buildHeaders(false, linuxdoReferer, linuxdoUserAgent(cfg)),
			VideoHeads: buildHeaders(true, linuxdoReferer, linuxdoUserAgent(cfg)),
		}
		linuxdoApplyMetaHeaders(cfg, &meta)
		return meta, nil
	}
	if htmlErr != nil {
		return mediaMeta{}, htmlErr
	}
	if topic := linuxdoExtractTopicJSONFromHTML(htmlBody); topic != nil {
		logDebug(cfg, "linuxdo_fallback stage=html_preloaded_retry final=%s body_len=%d", finalURL, len(htmlBody))
		meta, err := parseLinuxdoTopicJSON(sourceURL, finalURL, mustJSON(topic))
		if err != nil {
			return mediaMeta{}, err
		}
		linuxdoMergeRenderedHTML(&meta, htmlBody, finalURL)
		linuxdoApplyMetaHeaders(cfg, &meta)
		return meta, nil
	}
	desc := linuxdoHTMLBodyText(htmlBody)
	images := linuxdoExtractImages(htmlBody, finalURL)
	logDebug(cfg, "linuxdo_fallback stage=html_shell final=%s body_len=%d title_len=%d desc_len=%d images=%d markers=%s raw_error=%v", finalURL, len(htmlBody), len(title), len(desc), len(images), linuxdoHTMLMarkerSummary(htmlBody), rawErr)
	if strings.TrimSpace(desc) == "" && len(images) == 0 {
		return mediaMeta{}, fmt.Errorf("linux.do fallback got title only: final=%s body_len=%d markers=%s raw_error=%v request=%s", finalURL, len(htmlBody), linuxdoHTMLMarkerSummary(htmlBody), rawErr, linuxdoRequestSummary(cfg))
	}
	meta := mediaMeta{
		URL:         pageURL,
		SourceURL:   sourceURL,
		Platform:    "linuxdo",
		Title:       firstNonEmpty(linuxdoHTMLTitle(htmlBody), "Linux.do Topic "+topicID),
		Desc:        desc,
		Cover:       firstImageURL(images),
		ImageURLs:   images,
		LinuxdoHTML: linuxdoPrimaryContentHTML(htmlBody),
		ImageHeads:  buildHeaders(false, linuxdoReferer, linuxdoUserAgent(cfg)),
		VideoHeads:  buildHeaders(true, linuxdoReferer, linuxdoUserAgent(cfg)),
	}
	linuxdoApplyMetaHeaders(cfg, &meta)
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

func linuxdoMainPostURL(raw, topicID string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasSuffix(strings.ToLower(raw), ".json") {
		return raw
	}
	postNumber := linuxdoPostNumber(raw)
	if postNumber == "" || postNumber == "1" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "t" {
		return raw
	}
	for i := len(parts) - 1; i >= 1; i-- {
		if parts[i] == postNumber {
			parts[i] = "1"
			u.Path = "/" + strings.Join(parts, "/")
			return u.String()
		}
	}
	if topicID != "" {
		return linuxdoBase + "/t/" + topicID + "/1"
	}
	return raw
}

func linuxdoExtractTopicJSONFromHTML(htmlBody string) map[string]any {
	if topic := linuxdoExtractDiscoursePreloadedTopic(htmlBody); topic != nil {
		return topic
	}
	if topic := linuxdoExtractDataPreloadedTopic(htmlBody); topic != nil {
		return topic
	}
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

func linuxdoExtractDataPreloadedTopic(htmlBody string) map[string]any {
	for _, m := range regexp.MustCompile(`(?is)\bdata-preloaded="([^"]*)"|\bdata-preloaded='([^']*)'`).FindAllStringSubmatch(htmlBody, -1) {
		rawAttr := firstNonEmpty(m[1], m[2])
		raw := strings.TrimSpace(html.UnescapeString(htmlUnescape(rawAttr)))
		if raw == "" {
			continue
		}
		var root any
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			continue
		}
		if topic := linuxdoFindTopicMapDeep(root); topic != nil {
			return topic
		}
	}
	return nil
}

func linuxdoExtractDiscoursePreloadedTopic(htmlBody string) map[string]any {
	for _, m := range regexp.MustCompile(`(?is)<script\b[^>]*\bdata-discourse-preloaded=["'][^"']*(?:topic|post_stream)[^"']*["'][^>]*>(.*?)</script>`).FindAllStringSubmatch(htmlBody, -1) {
		if len(m) < 2 {
			continue
		}
		raw := strings.TrimSpace(html.UnescapeString(htmlUnescape(m[1])))
		if raw == "" {
			continue
		}
		var root any
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			continue
		}
		if topic := linuxdoFindTopicMap(root); topic != nil {
			return topic
		}
	}
	return nil
}

func linuxdoFindTopicMapDeep(v any) map[string]any {
	if topic := linuxdoFindTopicMap(v); topic != nil {
		return topic
	}
	switch x := v.(type) {
	case map[string]any:
		for _, item := range x {
			if topic := linuxdoFindTopicMapDeep(item); topic != nil {
				return topic
			}
		}
	case []any:
		for _, item := range x {
			if topic := linuxdoFindTopicMapDeep(item); topic != nil {
				return topic
			}
		}
	case string:
		raw := strings.TrimSpace(html.UnescapeString(htmlUnescape(x)))
		if raw == "" || (!strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[")) {
			return nil
		}
		var nested any
		if err := json.Unmarshal([]byte(raw), &nested); err != nil {
			return nil
		}
		return linuxdoFindTopicMapDeep(nested)
	}
	return nil
}

func linuxdoHTMLMarkerSummary(htmlBody string) string {
	items := []string{
		fmt.Sprintf("data_discourse=%d", strings.Count(htmlBody, "data-discourse-preloaded")),
		fmt.Sprintf("data_preloaded=%d", strings.Count(htmlBody, "data-preloaded")),
		fmt.Sprintf("application_json=%d", strings.Count(htmlBody, "application/json")),
		fmt.Sprintf("post_stream=%d", strings.Count(htmlBody, "post_stream")),
		fmt.Sprintf("cooked=%d", strings.Count(htmlBody, "cooked")),
		fmt.Sprintf("topic_body=%d", strings.Count(htmlBody, "topic-body")),
		fmt.Sprintf("crawler_post=%d", strings.Count(htmlBody, "crawler-post")),
	}
	return strings.Join(items, ",")
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

func linuxdoMergeRenderedHTML(meta *mediaMeta, htmlBody, finalURL string) {
	if meta == nil || strings.TrimSpace(htmlBody) == "" {
		return
	}
	renderedDesc := linuxdoHTMLBodyText(htmlBody)
	preferRendered := linuxdoPreferRenderedDesc(meta.Desc, renderedDesc)
	if preferRendered {
		meta.Desc = renderedDesc
	}
	if renderedHTML := linuxdoPrimaryContentHTML(htmlBody); renderedHTML != "" && (strings.TrimSpace(meta.LinuxdoHTML) == "" || preferRendered) {
		meta.LinuxdoHTML = renderedHTML
	}
	images := linuxdoExtractImages(htmlBody, finalURL)
	if len(images) > 0 {
		meta.ImageURLs = linuxdoMergeImageURLs(meta.ImageURLs, images)
		meta.Cover = firstImageURL(meta.ImageURLs)
	}
}

func linuxdoMergeImageURLs(current, rendered [][]string) [][]string {
	merged := make([][]string, 0, len(current)+len(rendered))
	seen := map[string]bool{}
	addGroup := func(group []string) {
		if len(group) == 0 {
			return
		}
		cleaned := make([]string, 0, len(group))
		uniqueInGroup := map[string]bool{}
		hasNew := false
		for _, raw := range group {
			u := strings.TrimSpace(raw)
			if u == "" {
				continue
			}
			key := linuxdoImageDedupeKey(u)
			if uniqueInGroup[key] {
				continue
			}
			uniqueInGroup[key] = true
			cleaned = append(cleaned, u)
			if !seen[key] {
				hasNew = true
			}
		}
		if !hasNew || len(cleaned) == 0 {
			return
		}
		for _, u := range cleaned {
			seen[linuxdoImageDedupeKey(u)] = true
		}
		merged = append(merged, cleaned)
	}
	for _, group := range current {
		addGroup(group)
	}
	for _, group := range rendered {
		addGroup(group)
	}
	return dedupeMediaGroups(merged)
}

func linuxdoImageDedupeKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	p := u.EscapedPath()
	p = strings.Replace(p, "/optimized/", "/original/", 1)
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		dir, file := p[:idx+1], p[idx+1:]
		if dot := strings.LastIndex(file, "."); dot > 0 {
			stem, ext := file[:dot], file[dot:]
			stem = regexp.MustCompile(`_[0-9]+_[0-9]+x[0-9]+$`).ReplaceAllString(stem, "")
			p = dir + stem + strings.ToLower(ext)
		}
	}
	u.Path = p
	return strings.ToLower(u.Host) + u.EscapedPath()
}

func linuxdoPreferRenderedDesc(current, rendered string) bool {
	current = strings.TrimSpace(current)
	rendered = strings.TrimSpace(rendered)
	if rendered == "" {
		return false
	}
	if current == "" {
		return true
	}
	currentHasPoll := strings.Contains(current, "投票结果")
	renderedHasPoll := strings.Contains(rendered, "投票结果")
	if renderedHasPoll && !currentHasPoll {
		return true
	}
	return renderedHasPoll && len(rendered) > len(current)
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
		URL:         link,
		SourceURL:   firstNonEmpty(sourceURL, link),
		Platform:    "linuxdo",
		Title:       title,
		Author:      author,
		Avatar:      avatar,
		Timestamp:   linuxdoTime(getString(post, "created_at")),
		Desc:        desc,
		Cover:       cover,
		ImageURLs:   images,
		LinuxdoHTML: cooked,
		ImageHeads:  buildHeaders(false, linuxdoReferer, linuxdoUA),
		VideoHeads:  buildHeaders(true, linuxdoReferer, linuxdoUA),
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
	fragments := linuxdoImageSourceFragments(cooked)
	if len(fragments) == 0 {
		fragments = []string{cooked}
	}
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
	for _, fragment := range fragments {
		for _, tag := range regexp.MustCompile(`(?is)<img\b[^>]+>`).FindAllString(fragment, -1) {
			if linuxdoImageTagLooksNonContent(tag) {
				continue
			}
			add(linuxdoTagAttr(tag, "src"))
		}
		for _, m := range regexp.MustCompile(`(?is)<a\b[^>]+href=["']([^"']+\.(?:png|jpe?g|webp|gif)(?:\?[^"']*)?)["']`).FindAllStringSubmatch(fragment, -1) {
			add(m[1])
		}
	}
	return dedupeMediaGroups(out)
}

func linuxdoImageSourceFragments(body string) []string {
	for _, className := range []string{"cooked", "topic-body", "crawler-post"} {
		if fragments := linuxdoClassDivFragments(body, className); len(fragments) > 0 {
			return fragments
		}
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`),
		regexp.MustCompile(`(?is)<main\b[^>]*>(.*?)</main>`),
	} {
		matches := re.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			continue
		}
		fragments := make([]string, 0, len(matches))
		for _, m := range matches {
			if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
				fragments = append(fragments, m[1])
			}
		}
		if len(fragments) > 0 {
			return fragments
		}
	}
	return nil
}

func linuxdoClassDivFragments(body, className string) []string {
	className = strings.TrimSpace(className)
	if className == "" {
		return nil
	}
	startRe := regexp.MustCompile(`(?is)<div\b[^>]*\bclass\s*=\s*(?:"[^"]*\b` + regexp.QuoteMeta(className) + `\b[^"]*"|'[^']*\b` + regexp.QuoteMeta(className) + `\b[^']*')[^>]*>`)
	fragments := []string{}
	pos := 0
	for {
		loc := startRe.FindStringIndex(body[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[0]
		end, ok := linuxdoBalancedDivEnd(body, start)
		if !ok {
			break
		}
		fragments = append(fragments, body[start:end])
		pos = end
	}
	return fragments
}

func linuxdoImageTagLooksNonContent(tag string) bool {
	if linuxdoImageTagLooksEmoji(tag) {
		return true
	}
	for _, name := range []string{"class", "id"} {
		text := strings.ToLower(strings.TrimSpace(html.UnescapeString(htmlUnescape(linuxdoTagAttr(tag, name)))))
		if text == "" {
			continue
		}
		if strings.Contains(text, "avatar") || strings.Contains(text, "user-avatar") ||
			strings.Contains(text, "site-logo") || strings.Contains(text, "logo") {
			return true
		}
	}
	for _, name := range []string{"alt", "title", "aria-label"} {
		text := strings.ToLower(strings.TrimSpace(html.UnescapeString(htmlUnescape(linuxdoTagAttr(tag, name)))))
		if text == "" {
			continue
		}
		if strings.Contains(text, "linux do") || strings.Contains(text, "linux.do") {
			return true
		}
	}
	return false
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
	s = linuxdoReplacePolls(s)
	s = regexp.MustCompile(`(?is)<pre\b[^>]*>.*?</pre>`).ReplaceAllString(s, "\n[代码块]\n")
	s = regexp.MustCompile(`(?is)<blockquote\b[^>]*>`).ReplaceAllString(s, "\n引用：")
	s = regexp.MustCompile(`(?is)</(?:p|div|li|blockquote|h[1-6])>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = linuxdoReplaceEmojiImages(s)
	s = regexp.MustCompile(`(?is)<img\b[^>]*>`).ReplaceAllString(s, "\n[图片]\n")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = html.UnescapeString(htmlUnescape(s))
	s = regexp.MustCompile(`[ \t\r\f\v]+`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	s = linuxdoStripPromotionDeclarations(s)
	return strings.TrimSpace(s)
}

func linuxdoReplacePolls(s string) string {
	startRe := regexp.MustCompile(`(?is)<div\b[^>]*\bdata-poll-name\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)[^>]*>`)
	var b strings.Builder
	pos := 0
	for {
		loc := startRe.FindStringIndex(s[pos:])
		if loc == nil {
			b.WriteString(s[pos:])
			break
		}
		start := pos + loc[0]
		end, ok := linuxdoBalancedDivEnd(s, start)
		if !ok {
			b.WriteString(s[pos:])
			break
		}
		b.WriteString(s[pos:start])
		block := s[start:end]
		if summary := linuxdoPollSummary(block); summary != "" {
			b.WriteString("\n")
			b.WriteString(summary)
			b.WriteString("\n")
		} else {
			b.WriteString(block)
		}
		pos = end
	}
	return b.String()
}

func linuxdoBalancedDivEnd(s string, start int) (int, bool) {
	if start < 0 || start >= len(s) {
		return 0, false
	}
	tagRe := regexp.MustCompile(`(?is)</?div\b[^>]*>`)
	depth := 0
	for _, loc := range tagRe.FindAllStringIndex(s[start:], -1) {
		tag := s[start+loc[0] : start+loc[1]]
		if strings.HasPrefix(strings.ToLower(tag), "</div") {
			depth--
		} else {
			depth++
		}
		if depth == 0 {
			return start + loc[1], true
		}
	}
	return 0, false
}

func linuxdoPollSummary(block string) string {
	optionRe := regexp.MustCompile(`(?is)<span\b[^>]*class\s*=\s*(?:"[^"]*\bpercentage\b[^"]*"|'[^']*\bpercentage\b[^']*')[^>]*>(.*?)</span>\s*<span\b[^>]*class\s*=\s*(?:"[^"]*\boption-text\b[^"]*"|'[^']*\boption-text\b[^']*')[^>]*>(.*?)</span>`)
	var lines []string
	for _, m := range optionRe.FindAllStringSubmatch(block, -1) {
		percent := linuxdoCleanInlineText(m[1])
		option := linuxdoCleanInlineText(m[2])
		if percent == "" || option == "" {
			continue
		}
		lines = append(lines, percent+" "+option)
	}
	if len(lines) == 0 {
		return ""
	}
	countRe := regexp.MustCompile(`(?is)<span\b[^>]*class\s*=\s*(?:"[^"]*\binfo-number\b[^"]*"|'[^']*\binfo-number\b[^']*')[^>]*>(.*?)</span>\s*<span\b[^>]*class\s*=\s*(?:"[^"]*\binfo-label\b[^"]*"|'[^']*\binfo-label\b[^']*')[^>]*>(.*?)</span>`)
	var counts []string
	for _, m := range countRe.FindAllStringSubmatch(block, -1) {
		number := linuxdoCleanInlineText(m[1])
		label := linuxdoCleanInlineText(m[2])
		if number != "" && label != "" {
			counts = append(counts, number+" "+label)
		}
	}
	out := []string{"投票结果："}
	out = append(out, lines...)
	if len(counts) > 0 {
		out = append(out, strings.Join(counts, " / "))
	}
	return strings.Join(out, "\n")
}

func linuxdoCleanInlineText(s string) string {
	s = linuxdoReplaceEmojiImages(s)
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = html.UnescapeString(htmlUnescape(s))
	s = strings.ReplaceAll(s, "\u200b", "")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func linuxdoReplaceEmojiImages(s string) string {
	return regexp.MustCompile(`(?is)<img\b[^>]*>`).ReplaceAllStringFunc(s, func(tag string) string {
		if !linuxdoImageTagLooksEmoji(tag) {
			return tag
		}
		text := linuxdoEmojiTextFromTag(tag)
		if text == "" {
			return ""
		}
		return " " + text + " "
	})
}

func linuxdoImageTagLooksEmoji(tag string) bool {
	lower := strings.ToLower(tag)
	if strings.Contains(lower, "twemoji") || strings.Contains(lower, "/emoji/") || strings.Contains(lower, "/images/emoji/") {
		return true
	}
	class := strings.ToLower(linuxdoTagAttr(tag, "class"))
	if regexp.MustCompile(`(^|\s)emoji(\s|$)`).MatchString(class) {
		return true
	}
	return linuxdoTagAttr(tag, "data-emoji-name") != ""
}

func linuxdoEmojiTextFromTag(tag string) string {
	for _, name := range []string{"alt", "title", "data-emoji-name"} {
		text := strings.TrimSpace(html.UnescapeString(htmlUnescape(linuxdoTagAttr(tag, name))))
		if text == "" {
			continue
		}
		return linuxdoNormalizeEmojiText(text)
	}
	return ""
}

func linuxdoNormalizeEmojiText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if emoji, ok := linuxdoEmojiShortcodeToUnicode(text); ok {
		return emoji
	}
	if strings.HasPrefix(text, ":") && strings.HasSuffix(text, ":") {
		return text
	}
	if regexp.MustCompile(`^[A-Za-z0-9_+\-]+$`).MatchString(text) {
		return ":" + text + ":"
	}
	return text
}

func linuxdoEmojiShortcodeToUnicode(text string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(text))
	key = strings.Trim(key, ":")
	key = strings.ReplaceAll(key, "-", "_")
	if key == "" {
		return "", false
	}
	emoji, ok := linuxdoEmojiShortcodes[key]
	return emoji, ok
}

var linuxdoEmojiShortcodes = map[string]string{
	"+1":                            "\U0001F44D",
	"-1":                            "\U0001F44E",
	"100":                           "\U0001F4AF",
	"angry":                         "\U0001F620",
	"astonished":                    "\U0001F632",
	"blush":                         "\U0001F60A",
	"clap":                          "\U0001F44F",
	"confused":                      "\U0001F615",
	"cry":                           "\U0001F622",
	"disappointed":                  "\U0001F61E",
	"expressionless":                "\U0001F611",
	"eyes":                          "\U0001F440",
	"facepalm":                      "\U0001F926",
	"fire":                          "\U0001F525",
	"grin":                          "\U0001F601",
	"grinning":                      "\U0001F600",
	"heart":                         "\u2764\uFE0F",
	"heart_eyes":                    "\U0001F60D",
	"hugging":                       "\U0001F917",
	"hugs":                          "\U0001F917",
	"joy":                           "\U0001F602",
	"laughing":                      "\U0001F606",
	"neutral_face":                  "\U0001F610",
	"ok_hand":                       "\U0001F44C",
	"open_mouth":                    "\U0001F62E",
	"partying_face":                 "\U0001F973",
	"pray":                          "\U0001F64F",
	"rage":                          "\U0001F621",
	"raised_hands":                  "\U0001F64C",
	"rofl":                          "\U0001F923",
	"rolling_on_the_floor_laughing": "\U0001F923",
	"scream":                        "\U0001F631",
	"slight_smile":                  "\U0001F642",
	"slightly_smiling_face":         "\U0001F642",
	"smile":                         "\U0001F604",
	"smiley":                        "\U0001F603",
	"smiling_face_with_3_hearts":    "\U0001F970",
	"sob":                           "\U0001F62D",
	"sweat":                         "\U0001F613",
	"sweat_smile":                   "\U0001F605",
	"thinking":                      "\U0001F914",
	"thinking_face":                 "\U0001F914",
	"thumbsdown":                    "\U0001F44E",
	"thumbsup":                      "\U0001F44D",
	"tada":                          "\U0001F389",
	"wink":                          "\U0001F609",
}

func linuxdoTagAttr(tag, name string) string {
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(name) + `\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	m := re.FindStringSubmatch(tag)
	if len(m) == 0 {
		return ""
	}
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
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
