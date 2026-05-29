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

func parseTwitter(cfg config, raw string) (mediaMeta, error) {
	m := regexp.MustCompile(`/status/(\d+)`).FindStringSubmatch(raw)
	if len(m) < 2 {
		return mediaMeta{}, fmt.Errorf("无法解析 Twitter/X 推文ID: %s", raw)
	}
	tweetID := m[1]
	info, err := fetchFxTwitter(tweetID)
	if err != nil {
		return mediaMeta{}, err
	}
	images := [][]string{}
	for _, img := range info.Images {
		if img != "" {
			images = append(images, []string{img})
		}
	}
	videos := [][]string{}
	for _, video := range info.Videos {
		if video.URL != "" {
			videos = append(videos, []string{"range:" + video.URL})
		}
	}
	if len(images) == 0 && len(videos) == 0 && strings.TrimSpace(info.Text) == "" {
		return mediaMeta{}, fmt.Errorf("推文不包含文本、图片或视频")
	}
	return mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "twitter",
		Title:      firstNonEmpty(info.Title, "Twitter 推文"),
		Author:     info.Author,
		Avatar:     info.Avatar,
		Desc:       info.Text,
		Timestamp:  info.Timestamp,
		Cover:      info.Cover,
		VideoURLs:  videos,
		ImageURLs:  images,
		VideoHeads: buildHeaders(true, "", defaultUA),
		ImageHeads: buildHeaders(false, "", defaultUA),
		ForceLocal: len(videos) > 0,
	}, nil
}

type twitterInfo struct {
	Title     string
	Text      string
	Author    string
	Avatar    string
	Timestamp string
	Cover     string
	Images    []string
	Videos    []twitterVideo
}

type twitterVideo struct {
	URL      string
	Thumb    string
	Duration any
}

func fetchFxTwitter(tweetID string) (twitterInfo, error) {
	api := "https://api.fxtwitter.com/status/" + tweetID
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, api, nil)
		req.Header.Set("User-Agent", defaultUA)
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == 404 || resp.StatusCode == 403 {
			return twitterInfo{}, fmt.Errorf("FxTwitter 推文不可用 HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			lastErr = fmt.Errorf("FxTwitter HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return twitterInfo{}, fmt.Errorf("FxTwitter HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
		}
		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			return twitterInfo{}, err
		}
		return parseFxTwitterResponse(data)
	}
	return twitterInfo{}, lastErr
}

func parseFxTwitterResponse(data map[string]any) (twitterInfo, error) {
	tweet := getMap(data, "tweet")
	if tweet == nil {
		return twitterInfo{}, fmt.Errorf("FxTwitter响应缺少tweet字段")
	}
	text := twitterText(tweet)
	quote := extractFxQuote(tweet["quote"])
	desc := buildTweetDesc(text, quote)
	authorMap := getMap(tweet, "author")
	author := fxTwitterAuthor(authorMap)
	timestamp := parseTwitterDate(getString(tweet, "created_at"))
	info := twitterInfo{
		Title: firstNonEmpty(func() string {
			if author != "" {
				return author + " 的推文"
			}
			return ""
		}(), "Twitter 推文"),
		Text:      desc,
		Author:    combineParenthetical(author, quote["author"]),
		Avatar:    firstNestedHTTPURLByKeys(authorMap, 5, "avatar", "profile_image"),
		Timestamp: combineParenthetical(timestamp, quote["timestamp"]),
	}
	media := getMap(tweet, "media")
	for _, photo := range getSlice(media, "photos") {
		if m, ok := photo.(map[string]any); ok && getString(m, "url") != "" {
			info.Images = append(info.Images, getString(m, "url"))
		}
	}
	for _, video := range getSlice(media, "videos") {
		if m, ok := video.(map[string]any); ok && getString(m, "url") != "" {
			info.Videos = append(info.Videos, twitterVideo{
				URL:      getString(m, "url"),
				Thumb:    getString(m, "thumbnail_url"),
				Duration: m["duration"],
			})
			if info.Cover == "" {
				info.Cover = getString(m, "thumbnail_url")
			}
		}
	}
	if info.Cover == "" && len(info.Images) > 0 {
		info.Cover = info.Images[0]
	}
	return info, nil
}

func twitterText(tweet map[string]any) string {
	if raw, ok := tweet["raw_text"].(map[string]any); ok {
		text := getString(raw, "text")
		if text != "" {
			return applyDisplayRange(text, raw["display_text_range"])
		}
	}
	return getString(tweet, "text")
}

func fxTwitterAuthor(author map[string]any) string {
	if author == nil {
		return ""
	}
	name := getString(author, "name")
	screen := getString(author, "screen_name")
	if name != "" && screen != "" {
		return name + "(@" + screen + ")"
	}
	return firstNonEmpty(name, screen)
}

func applyDisplayRange(text string, r any) string {
	arr, ok := r.([]any)
	if !ok || len(arr) != 2 {
		return text
	}
	start := int(anyFloat(arr[0]))
	end := int(anyFloat(arr[1]))
	rs := []rune(text)
	if start < 0 || end < start || start >= len(rs) {
		return text
	}
	if end > len(rs) {
		end = len(rs)
	}
	return strings.TrimSpace(string(rs[start:end]))
}

func parseTwitterDate(raw string) string {
	if raw == "" {
		return ""
	}
	if t, err := time.Parse("Mon Jan 02 15:04:05 -0700 2006", raw); err == nil {
		return t.Format("2006-01-02")
	}
	return raw
}

func extractFxQuote(v any) map[string]string {
	quote, ok := v.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	text := twitterText(quote)
	if text == "" {
		return map[string]string{}
	}
	return map[string]string{
		"text":      text,
		"author":    fxTwitterAuthor(getMap(quote, "author")),
		"timestamp": parseTwitterDate(getString(quote, "created_at")),
		"reply_to":  strings.TrimSpace(getString(quote, "replying_to")),
	}
}

func buildTweetDesc(text string, quote map[string]string) string {
	desc := strings.TrimSpace(text)
	if quote == nil || quote["text"] == "" {
		return desc
	}
	parts := []string{"引用推文："}
	if quote["author"] != "" {
		parts = append(parts, quote["author"])
	}
	if quote["reply_to"] != "" {
		parts = append(parts, "回复 @"+quote["reply_to"])
	}
	parts = append(parts, quote["text"])
	q := strings.Join(parts, "\n")
	if desc != "" {
		return desc + "\n\n" + q
	}
	return q
}

func combineParenthetical(primary, secondary string) string {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	if primary != "" && secondary != "" {
		return primary + "（" + secondary + "）"
	}
	return firstNonEmpty(primary, secondary)
}

func anyFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
