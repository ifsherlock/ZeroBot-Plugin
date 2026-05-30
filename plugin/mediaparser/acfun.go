package mediaparser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const acfunReferer = "https://www.acfun.cn/"

func parseAcfun(cfg config, raw string) (mediaMeta, error) {
	acid := acfunID(raw)
	if acid == "" {
		return mediaMeta{}, fmt.Errorf("无法提取 AcFun 视频 ID")
	}
	pageURL := "https://www.acfun.cn/v/ac" + acid
	apiURL := pageURL + "?quickViewId=videoInfo_new&ajaxpipe=1"
	headers := map[string]string{
		"User-Agent": defaultUA,
		"Referer":    acfunReferer,
	}
	body, _, status, err := fetchText(apiURL, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("AcFun HTTP %d", status)
	}
	if html := acfunHTMLFromAjax(body); html != "" {
		body = html
	}
	return parseAcfunVideoInfoHTML(cfg, pageURL, body)
}

func acfunID(raw string) string {
	if m := regexp.MustCompile(`(?i)(?:ac=|/ac)(\d+)`).FindStringSubmatch(raw); len(m) > 1 {
		return m[1]
	}
	return ""
}

func acfunHTMLFromAjax(raw string) string {
	var payload struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	return payload.HTML
}

func parseAcfunVideoInfoHTML(cfg config, sourceURL, html string) (mediaMeta, error) {
	info, err := extractAcfunVideoInfo(html)
	if err != nil {
		return mediaMeta{}, err
	}
	m3u8URL, err := acfunM3U8URL(cfg, acfunPlayData(info))
	if err != nil {
		return mediaMeta{}, err
	}
	createdMS := int64(getFloat(info, "createTimeMillis"))
	timestamp := ""
	if createdMS > 0 {
		timestamp = time.UnixMilli(createdMS).Format("2006-01-02")
	}
	duration := int64(getFloat(info, "currentVideoInfo", "durationMillis")) / 1000
	accessText := ""
	if duration > 0 {
		accessText = formatDurationSeconds(duration)
	}
	return mediaMeta{
		URL:        sourceURL,
		SourceURL:  sourceURL,
		Platform:   "acfun",
		Title:      getString(info, "title"),
		Author:     getString(info, "user", "name"),
		Avatar:     getString(info, "user", "headUrl"),
		Timestamp:  timestamp,
		Desc:       cleanHTMLText(getString(info, "description")),
		Cover:      firstNonEmpty(getString(info, "coverUrl"), firstNestedHTTPURLByKeys(info, 4, "coverUrl", "cover")),
		VideoURLs:  [][]string{{"m3u8:" + m3u8URL}},
		VideoHeads: buildHeaders(true, acfunReferer, defaultUA),
		ImageHeads: buildHeaders(false, acfunReferer, defaultUA),
		ForceLocal: true,
		AccessText: accessText,
	}, nil
}

func extractAcfunVideoInfo(html string) (map[string]any, error) {
	m := regexp.MustCompile(`(?is)window\.pageInfo\s*=\s*window\.videoInfo\s*=\s*(.*?)</script>|window\.videoInfo\s*=\s*(.*?)</script>`).FindStringSubmatch(html)
	if len(m) == 0 {
		return nil, fmt.Errorf("未找到 AcFun videoInfo")
	}
	raw := firstNonEmpty(m[1], m[2])
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ";"))
	if start := strings.Index(raw, "{"); start > 0 {
		raw = raw[start:]
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out, nil
	}
	cleaned := acfunUnescapeJSON(raw)
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func acfunUnescapeJSON(raw string) string {
	cleaned := regexp.MustCompile(`\\+"`).ReplaceAllString(raw, `"`)
	cleaned = strings.ReplaceAll(cleaned, `"{`, `{`)
	cleaned = strings.ReplaceAll(cleaned, `}"`, `}`)
	return cleaned
}

func acfunPlayData(info map[string]any) any {
	current := getMap(info, "currentVideoInfo")
	if current == nil {
		return nil
	}
	return current["ksPlayJson"]
}

func acfunM3U8URL(cfg config, raw any) (string, error) {
	if raw == nil {
		return "", fmt.Errorf("AcFun 视频缺少播放信息")
	}
	var play map[string]any
	switch x := raw.(type) {
	case map[string]any:
		play = x
	case string:
		if strings.TrimSpace(x) == "" {
			return "", fmt.Errorf("AcFun 视频缺少播放信息")
		}
		if err := json.Unmarshal([]byte(x), &play); err != nil {
			cleaned := acfunUnescapeJSON(x)
			cleaned = strings.ReplaceAll(cleaned, `\\n`, `\n`)
			if err2 := json.Unmarshal([]byte(cleaned), &play); err2 != nil {
				return "", err
			}
		}
	default:
		return "", fmt.Errorf("AcFun 播放信息格式异常")
	}
	reps := acfunRepresentations(play)
	if len(reps) == 0 {
		return "", fmt.Errorf("AcFun 视频没有可用清晰度")
	}
	best := ""
	bestHeight := -1
	for _, rep := range reps {
		u := getString(rep, "url")
		if u == "" {
			continue
		}
		height := acfunQualityHeight(firstNonEmpty(getString(rep, "qualityType"), getString(rep, "qualityLabel")))
		if cfg.VideoMaxResolution > 0 && height > cfg.VideoMaxResolution {
			continue
		}
		if height > bestHeight {
			best = u
			bestHeight = height
		}
	}
	if best == "" {
		best = getString(reps[0], "url")
	}
	if best == "" {
		return "", fmt.Errorf("AcFun 视频地址为空")
	}
	return ensureHTTPS(best), nil
}

func acfunRepresentations(play map[string]any) []any {
	for _, set := range getSlice(play, "adaptationSet") {
		if m, ok := set.(map[string]any); ok {
			if reps := getSlice(m, "representation"); len(reps) > 0 {
				return reps
			}
		}
	}
	return nil
}

func acfunQualityHeight(raw string) int {
	raw = strings.ToLower(raw)
	if m := regexp.MustCompile(`(\d{3,4})p`).FindStringSubmatch(raw); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func formatDurationSeconds(sec int64) string {
	if sec <= 0 {
		return ""
	}
	if sec < 60 {
		return fmt.Sprintf("%d秒", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%d分%02d秒", sec/60, sec%60)
	}
	return fmt.Sprintf("%d小时%02d分%02d秒", sec/3600, (sec%3600)/60, sec%60)
}
