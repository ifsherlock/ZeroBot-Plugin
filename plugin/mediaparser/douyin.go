package mediaparser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	douyinUA      = "Mozilla/5.0 (Linux; Android 8.0.0; SM-G955U Build/R16NW) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/116.0.0.0 Mobile Safari/537.36"
	douyinReferer = "https://www.douyin.com/"
)

func parseDouyin(cfg config, raw string) (mediaMeta, error) {
	headers := map[string]string{
		"User-Agent": douyinUA,
		"Referer":    "https://www.douyin.com/?is_from_mobile_home=1&recommend=1",
	}
	redirected, err := redirectURL(raw, headers)
	if err != nil {
		redirected = raw
	}
	isNote := strings.Contains(redirected, "/note/") || strings.Contains(raw, "/note/")
	id := ""
	if isNote {
		if m := regexp.MustCompile(`/note/(\d+)`).FindStringSubmatch(redirected + " " + raw); len(m) > 1 {
			id = m[1]
		}
	} else if m := regexp.MustCompile(`/video/(\d+)`).FindStringSubmatch(redirected); len(m) > 1 {
		id = m[1]
	} else if m := regexp.MustCompile(`\d{19}`).FindStringSubmatch(redirected + " " + raw); len(m) > 0 {
		id = m[0]
	}
	if id == "" {
		return mediaMeta{}, fmt.Errorf("无法解析抖音作品ID: %s", raw)
	}
	shareURL := "https://www.iesdouyin.com/share/video/" + id + "/"
	displayURL := firstNonEmpty(redirected, raw)
	if isNote {
		shareURL = "https://www.iesdouyin.com/share/note/" + id + "/"
		displayURL = "https://www.douyin.com/note/" + id
	}
	html, _, status, err := fetchText(shareURL, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("抖音页面 HTTP %d", status)
	}
	router, err := extractAssignedJSONObject(html, "window._ROUTER_DATA")
	if err != nil {
		return mediaMeta{}, err
	}
	loader := getMap(router, "loaderData")
	var videoInfo map[string]any
	for _, v := range loader {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if x, ok := m["videoInfoRes"].(map[string]any); ok {
			videoInfo = x
			break
		}
		if x, ok := m["noteDetailRes"].(map[string]any); ok {
			videoInfo = x
			break
		}
	}
	items, _ := videoInfo["item_list"].([]any)
	if len(items) == 0 {
		return mediaMeta{}, fmt.Errorf("抖音作品数据为空")
	}
	item, _ := items[0].(map[string]any)
	author := getMap(item, "author")
	nickname := getString(author)
	if nickname == "" {
		nickname = getString(item, "author", "nickname")
	}
	uniqueID := getString(item, "author", "unique_id")
	authorText := nickname
	if uniqueID != "" {
		if authorText != "" {
			authorText += "(uid:" + uniqueID + ")"
		} else {
			authorText = "(uid:" + uniqueID + ")"
		}
	}
	avatar := firstNonEmpty(
		firstNestedHTTPURL(getMap(author, "avatar_thumb"), 4),
		firstNestedHTTPURL(getMap(author, "avatar_medium"), 4),
		firstNestedHTTPURL(getMap(author, "avatar_larger"), 4),
	)
	cover := firstNonEmpty(
		firstNestedHTTPURL(getMap(item, "video", "cover"), 4),
		firstNestedHTTPURL(getMap(item, "video", "origin_cover"), 4),
		firstNestedHTTPURL(getMap(item, "video", "dynamic_cover"), 4),
	)
	imageGroups := [][]string{}
	for _, img := range getSlice(item, "images") {
		urls := nestedHTTPURLs(img, 5)
		if len(urls) > 0 {
			imageGroups = append(imageGroups, urls)
		}
	}
	videoGroups := [][]string{}
	if len(imageGroups) == 0 {
		play := getMap(item, "video", "play_addr")
		uri := getString(play, "uri")
		urls := nestedHTTPURLs(play, 5)
		if len(urls) == 0 && uri != "" {
			if strings.HasPrefix(uri, "https://") {
				urls = []string{uri}
			} else if !strings.HasSuffix(uri, ".mp3") {
				urls = []string{"https://www.douyin.com/aweme/v1/play/?video_id=" + uri}
			}
		}
		if len(urls) > 0 {
			videoGroups = append(videoGroups, urls)
		}
	}
	meta := mediaMeta{
		URL:        displayURL,
		SourceURL:  raw,
		Platform:   "douyin",
		Title:      getString(item, "desc"),
		Author:     authorText,
		Avatar:     avatar,
		Timestamp:  formatAnyTimestamp(getFloat(item, "create_time")),
		Cover:      cover,
		VideoURLs:  videoGroups,
		ImageURLs:  imageGroups,
		VideoHeads: buildHeaders(true, douyinReferer, douyinUA),
		ImageHeads: buildHeaders(false, douyinReferer, douyinUA),
	}
	if len(meta.VideoURLs)+len(meta.ImageURLs) == 0 {
		b, _ := json.Marshal(item)
		return mediaMeta{}, fmt.Errorf("抖音未找到媒体URL: %s", truncate(string(b), 200))
	}
	return meta, nil
}

func formatAnyTimestamp(ts float64) string {
	if ts <= 0 {
		return ""
	}
	if ts > 1e12 {
		ts = ts / 1000
	}
	return time.Unix(int64(ts), 0).Format("2006-01-02")
}
