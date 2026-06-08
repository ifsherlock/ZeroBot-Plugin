package mediaparser

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func parseTwitter(cfg config, raw string) (mediaMeta, error) {
	m := regexp.MustCompile(`/status/(\d+)`).FindStringSubmatch(raw)
	if len(m) < 2 {
		return mediaMeta{}, fmt.Errorf("无法解析 Twitter/X 推文ID: %s", raw)
	}
	tweetID := m[1]
	info, err := fetchTwitterInfo(cfg, tweetID)
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
	videoThumbs := []string{}
	for _, video := range info.Videos {
		if video.URL != "" {
			videos = append(videos, []string{"range:" + video.URL})
		}
		if video.Thumb != "" {
			videoThumbs = append(videoThumbs, video.Thumb)
		}
	}
	if len(images) == 0 && len(videos) == 0 && strings.TrimSpace(info.Text) == "" {
		return mediaMeta{}, fmt.Errorf("推文不包含文本、图片或视频")
	}
	items := mediaItemsFor(videos, images)
	if len(videos) > 0 && len(images) > 0 {
		for _, thumb := range videoThumbs {
			if !hasURLGroup(images, thumb) {
				images = append(images, []string{thumb})
			}
		}
	}
	return mediaMeta{
		URL:            raw,
		SourceURL:      raw,
		Platform:       "twitter",
		Title:          firstNonEmpty(info.Title, "Twitter 推文"),
		Author:         info.Author,
		Avatar:         info.Avatar,
		Desc:           info.Text,
		Timestamp:      info.Timestamp,
		Cover:          info.Cover,
		VideoURLs:      videos,
		ImageURLs:      images,
		MediaItems:     items,
		VideoHeads:     buildHeaders(true, "", defaultUA),
		ImageHeads:     buildHeaders(false, "", defaultUA),
		ForceLocal:     len(videos) > 0,
		AccessText:     info.SafetyText,
		KeylolBlocks:   info.KeylolBlocks,
		KeylolCategory: info.KeylolCategory,
		ArticleCard:    info.ArticleCard,
	}, nil
}

type twitterInfo struct {
	Title          string
	Text           string
	Author         string
	Avatar         string
	Timestamp      string
	Cover          string
	Images         []string
	Videos         []twitterVideo
	SafetyText     string
	KeylolBlocks   []keylolBlock
	KeylolCategory string
	ArticleCard    bool
}

type twitterVideo struct {
	URL      string
	Thumb    string
	Duration any
}

func fetchTwitterInfo(cfg config, tweetID string) (twitterInfo, error) {
	info, fxErr := fetchFxTwitter(cfg, tweetID)
	if fxErr == nil {
		return info, nil
	}
	info, vxErr := fetchVxTwitter(cfg, tweetID)
	if vxErr == nil {
		return info, nil
	}
	return twitterInfo{}, fmt.Errorf("X parse failed: FxTwitter: %v; VxTwitter: %v", fxErr, vxErr)
}

func fetchFxTwitter(cfg config, tweetID string) (twitterInfo, error) {
	api := "https://api.fxtwitter.com/status/" + tweetID
	httpClient := httpClientForPlatform(cfg, "twitter", 45*time.Second, true)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, api, nil)
		req.Header.Set("User-Agent", defaultUA)
		resp, err := httpClient.Do(req)
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

func fetchVxTwitter(cfg config, tweetID string) (twitterInfo, error) {
	api := "https://api.vxtwitter.com/Twitter/status/" + tweetID
	httpClient := httpClientForPlatform(cfg, "twitter", 45*time.Second, true)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, api, nil)
		req.Header.Set("User-Agent", defaultUA)
		resp, err := httpClient.Do(req)
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
			return twitterInfo{}, fmt.Errorf("VxTwitter tweet unavailable HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			lastErr = fmt.Errorf("VxTwitter HTTP %d", resp.StatusCode)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return twitterInfo{}, fmt.Errorf("VxTwitter HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
		}
		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			return twitterInfo{}, err
		}
		return parseVxTwitterResponse(data)
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
	if article := parseTwitterArticle(tweet, author, timestamp); article.Text != "" || len(article.Images) > 0 {
		info.Title = article.Title
		info.Text = article.Text
		info.Cover = article.Cover
		info.Images = article.Images
		info.KeylolBlocks = article.KeylolBlocks
		info.KeylolCategory = article.KeylolCategory
		info.ArticleCard = true
		if article.Timestamp != "" {
			info.Timestamp = article.Timestamp
		}
	}
	if anyBool(tweet["possibly_sensitive"]) {
		info.SafetyText = safetyMarkerTwitterSensitive
	}
	if !info.ArticleCard {
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
	}
	if info.Cover == "" && len(info.Images) > 0 {
		info.Cover = info.Images[0]
	}
	return info, nil
}

func parseVxTwitterResponse(data map[string]any) (twitterInfo, error) {
	text := strings.TrimSpace(getString(data, "text"))
	author := fxTwitterAuthor(map[string]any{
		"name":        getString(data, "user_name"),
		"screen_name": getString(data, "user_screen_name"),
	})
	timestamp := parseTwitterDate(getString(data, "date"))
	info := twitterInfo{
		Title: firstNonEmpty(func() string {
			if author != "" {
				return author + " 的推文"
			}
			return ""
		}(), "Twitter 推文"),
		Text:      text,
		Author:    author,
		Avatar:    getString(data, "user_profile_image_url"),
		Timestamp: timestamp,
	}
	if anyBool(data["possibly_sensitive"]) {
		info.SafetyText = safetyMarkerTwitterSensitive
	}
	for _, media := range getSlice(data, "media_extended") {
		m, ok := media.(map[string]any)
		if !ok {
			continue
		}
		url := getString(m, "url")
		switch strings.ToLower(strings.TrimSpace(getString(m, "type"))) {
		case "video", "animated_gif", "gif":
			if url != "" {
				info.Videos = append(info.Videos, twitterVideo{
					URL:      url,
					Thumb:    getString(m, "thumbnail_url"),
					Duration: m["duration_millis"],
				})
				if info.Cover == "" {
					info.Cover = getString(m, "thumbnail_url")
				}
			}
		default:
			if url != "" {
				info.Images = append(info.Images, url)
			}
		}
	}
	if len(info.Images) == 0 && len(info.Videos) == 0 {
		for _, mediaURL := range getSlice(data, "mediaURLs") {
			if url, ok := mediaURL.(string); ok && strings.TrimSpace(url) != "" {
				info.Images = append(info.Images, strings.TrimSpace(url))
			}
		}
	}
	if info.Cover == "" && len(info.Images) > 0 {
		info.Cover = info.Images[0]
	}
	if len(info.Images) == 0 && len(info.Videos) == 0 && text == "" {
		return twitterInfo{}, fmt.Errorf("VxTwitter response has no text or media")
	}
	return info, nil
}

func parseTwitterArticle(tweet map[string]any, author, fallbackTime string) twitterInfo {
	article := getMap(tweet, "article")
	if article == nil {
		return twitterInfo{}
	}
	title := strings.TrimSpace(getString(article, "title"))
	blocks := twitterArticleBlocks(article)
	desc := twitterArticleDesc(blocks)
	if desc == "" {
		desc = strings.TrimSpace(getString(article, "preview_text"))
		if desc != "" {
			blocks = append([]keylolBlock{{Kind: "text", Text: desc}}, blocks...)
		}
	}
	images := twitterArticleImages(article)
	cover := twitterArticleImageURL(getMap(article, "cover_media"))
	if cover == "" && len(images) > 0 {
		cover = images[0]
	}
	if title == "" && desc == "" && len(images) == 0 {
		return twitterInfo{}
	}
	if cover != "" && !twitterArticleHasImage(blocks, cover) {
		blocks = append([]keylolBlock{{Kind: "image", URL: cover}}, blocks...)
	}
	for _, raw := range images {
		if raw == "" || twitterArticleHasImage(blocks, raw) {
			continue
		}
		blocks = append(blocks, keylolBlock{Kind: "image", URL: raw})
	}
	return twitterInfo{
		Title:          firstNonEmpty(title, "X Article"),
		Text:           desc,
		Author:         author,
		Timestamp:      firstNonEmpty(parseTwitterArticleTime(getString(article, "created_at")), fallbackTime),
		Cover:          cover,
		Images:         images,
		KeylolBlocks:   blocks,
		KeylolCategory: "X Article",
		ArticleCard:    true,
	}
}

func twitterArticleBlocks(article map[string]any) []keylolBlock {
	content := getMap(article, "content")
	entityMap := twitterArticleEntityMap(content["entityMap"])
	out := []keylolBlock{}
	for _, item := range getSlice(content, "blocks") {
		block, _ := item.(map[string]any)
		text := strings.TrimSpace(getString(block, "text"))
		kind := strings.ToLower(strings.TrimSpace(getString(block, "type")))
		if media := twitterArticleBlockMedia(block, entityMap); media != "" {
			out = append(out, keylolBlock{Kind: "image", URL: media})
			continue
		}
		if text == "" {
			continue
		}
		switch kind {
		case "header-one":
			out = append(out, keylolBlock{Kind: "heading1", Text: text})
		case "header-two", "header-three":
			out = append(out, keylolBlock{Kind: "heading2", Text: text})
		case "blockquote":
			out = append(out, keylolBlock{Kind: "spoiler", Text: text})
		case "code-block":
			out = append(out, keylolBlock{Kind: "code", Text: text})
		default:
			out = append(out, keylolBlock{Kind: "text", Text: text})
		}
	}
	return out
}

func twitterArticleEntityMap(v any) map[string]map[string]any {
	out := map[string]map[string]any{}
	switch x := v.(type) {
	case map[string]any:
		for k, raw := range x {
			if m, ok := raw.(map[string]any); ok {
				out[k] = m
			}
		}
	case []any:
		for i, raw := range x {
			if m, ok := raw.(map[string]any); ok {
				out[strconv.Itoa(i)] = m
			}
		}
	}
	return out
}

func twitterArticleBlockMedia(block map[string]any, entityMap map[string]map[string]any) string {
	data := getMap(block, "data")
	for _, candidate := range []map[string]any{data, getMap(data, "media"), getMap(data, "mediaEntity")} {
		if raw := twitterArticleImageURL(candidate); raw != "" {
			return raw
		}
	}
	for _, rng := range getSlice(block, "entityRanges") {
		m, _ := rng.(map[string]any)
		key := getString(m, "key")
		entity := entityMap[key]
		for _, candidate := range []map[string]any{entity, getMap(entity, "data"), getMap(entity, "data", "media"), getMap(entity, "data", "mediaEntity")} {
			if raw := twitterArticleImageURL(candidate); raw != "" {
				return raw
			}
		}
	}
	return ""
}

func twitterArticleImages(article map[string]any) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, raw)
	}
	if cover := twitterArticleImageURL(getMap(article, "cover_media")); cover != "" {
		add(cover)
	}
	for _, item := range getSlice(article, "media_entities") {
		if raw := twitterArticleImageURL(getMap(item)); raw != "" {
			add(raw)
		}
	}
	return out
}

func twitterArticleImageURL(m map[string]any) string {
	if m == nil {
		return ""
	}
	for _, keys := range [][]string{
		{"media_info", "original_img_url"},
		{"media_info", "original_img_url_https"},
		{"media_info", "url"},
		{"original_img_url"},
		{"original_img_url_https"},
		{"url"},
		{"media_url_https"},
		{"media_url"},
		{"src"},
	} {
		if raw := getString(m, keys...); strings.HasPrefix(raw, "http") {
			return ensureHTTPS(raw)
		}
	}
	return ""
}

func twitterArticleDesc(blocks []keylolBlock) string {
	parts := []string{}
	for _, block := range blocks {
		if (block.Kind == "text" || block.Kind == "heading1" || block.Kind == "heading2") && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func twitterArticleHasImage(blocks []keylolBlock, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, block := range blocks {
		if block.Kind == "image" && strings.TrimSpace(block.URL) == raw {
			return true
		}
	}
	return false
}

func parseTwitterArticleTime(raw string) string {
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("2006-01-02")
	}
	return parseTwitterDate(raw)
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

func anyBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	default:
		return false
	}
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
