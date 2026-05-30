package mediaparser

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	instagramAPIUA   = "Instagram 219.0.0.12.117 Android"
	instagramWebUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"
	instagramReferer = "https://www.instagram.com/"
	instagramBase64  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
)

func parseInstagram(cfg config, raw string) (mediaMeta, error) {
	shortcode := instagramShortcode(raw)
	if shortcode == "" {
		return mediaMeta{}, fmt.Errorf("未识别 Instagram shortcode")
	}
	pk := instagramShortcodePK(shortcode)
	if pk == "" {
		return mediaMeta{}, fmt.Errorf("无法转换 Instagram media id")
	}
	headers := map[string]string{
		"User-Agent":      instagramAPIUA,
		"Accept":          "application/json",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"X-IG-App-ID":     "936619743392459",
		"Referer":         instagramReferer,
	}
	if cfg.InstagramCookie != "" {
		headers["Cookie"] = cfg.InstagramCookie
	}
	body, _, status, err := fetchText("https://i.instagram.com/api/v1/media/"+pk+"/info/", headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("Instagram API HTTP %d", status)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return mediaMeta{}, err
	}
	items := getSlice(data, "items")
	if len(items) == 0 {
		return mediaMeta{}, fmt.Errorf("Instagram API 未返回媒体")
	}
	item, _ := items[0].(map[string]any)
	if item == nil {
		return mediaMeta{}, fmt.Errorf("Instagram 媒体数据为空")
	}
	meta := buildInstagramMeta(raw, shortcode, item)
	if len(meta.VideoURLs) == 0 && len(meta.ImageURLs) == 0 {
		return mediaMeta{}, fmt.Errorf("Instagram 未找到可发送媒体")
	}
	return meta, nil
}

func instagramShortcode(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if (p == "p" || p == "reel" || p == "tv") && i+1 < len(parts) {
			return strings.TrimSpace(parts[i+1])
		}
	}
	if m := regexp.MustCompile(`(?:/|^)([A-Za-z0-9_-]{5,})(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 1 {
		return m[1]
	}
	return ""
}

func instagramShortcodePK(shortcode string) string {
	n := big.NewInt(0)
	base := big.NewInt(64)
	for _, r := range strings.TrimSpace(shortcode) {
		idx := strings.IndexRune(instagramBase64, r)
		if idx < 0 {
			return ""
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}
	if n.Sign() <= 0 {
		return ""
	}
	return n.String()
}

func buildInstagramMeta(raw, shortcode string, item map[string]any) mediaMeta {
	user := getMap(item, "user")
	username := getString(user, "username")
	fullName := getString(user, "full_name")
	author := firstNonEmpty(fullName, username)
	caption := instagramCaptionText(item)
	title := firstNonEmpty(firstNonEmptyLine(caption), instagramFallbackTitle(item, author))
	meta := mediaMeta{
		URL:        firstNonEmpty(getString(item, "code"), shortcode),
		SourceURL:  raw,
		Platform:   "instagram",
		Title:      title,
		Author:     author,
		Avatar:     ensureHTTPS(firstNonEmpty(getString(user, "profile_pic_url_hd"), getString(user, "profile_pic_url"))),
		Timestamp:  instagramTakenAt(item),
		Desc:       caption,
		Cover:      ensureHTTPS(instagramBestImage(item)),
		VideoHeads: buildHeaders(true, instagramReferer, instagramWebUA),
		ImageHeads: buildHeaders(false, instagramReferer, instagramWebUA),
	}
	meta.URL = "https://www.instagram.com/p/" + shortcode + "/"
	if strings.Contains(strings.ToLower(raw), "/reel/") {
		meta.URL = "https://www.instagram.com/reel/" + shortcode + "/"
	}
	addInstagramMedia(&meta, item)
	return meta
}

func instagramCaptionText(item map[string]any) string {
	return firstNonEmpty(
		getString(item, "caption", "text"),
		getString(item, "accessibility_caption"),
	)
}

func instagramFallbackTitle(item map[string]any, author string) string {
	if len(getSlice(item, "carousel_media")) > 0 {
		return firstNonEmpty(author+" 的 Instagram 图文", "Instagram 图文")
	}
	if len(getSlice(item, "video_versions")) > 0 {
		return firstNonEmpty(author+" 的 Instagram 视频", "Instagram 视频")
	}
	return firstNonEmpty(author+" 的 Instagram 帖子", "Instagram 帖子")
}

func instagramTakenAt(item map[string]any) string {
	ts := int64(getFloat(item, "taken_at"))
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

func addInstagramMedia(meta *mediaMeta, item map[string]any) {
	carousel := getSlice(item, "carousel_media")
	if len(carousel) == 0 {
		addInstagramOneMedia(meta, item)
		return
	}
	for _, raw := range carousel {
		if m, _ := raw.(map[string]any); m != nil {
			addInstagramOneMedia(meta, m)
		}
	}
}

func addInstagramOneMedia(meta *mediaMeta, item map[string]any) {
	if video := instagramBestVideo(item); video != "" {
		idx := len(meta.VideoURLs)
		meta.VideoURLs = append(meta.VideoURLs, []string{ensureHTTPS(video)})
		meta.MediaItems = append(meta.MediaItems, mediaItem{Kind: "video", Index: idx})
		if img := instagramBestImage(item); img != "" {
			meta.ImageURLs = append(meta.ImageURLs, []string{ensureHTTPS(img)})
			if meta.Cover == "" {
				meta.Cover = ensureHTTPS(img)
			}
		}
		return
	}
	if img := instagramBestImage(item); img != "" {
		idx := len(meta.ImageURLs)
		meta.ImageURLs = append(meta.ImageURLs, []string{ensureHTTPS(img)})
		meta.MediaItems = append(meta.MediaItems, mediaItem{Kind: "image", Index: idx})
		if meta.Cover == "" {
			meta.Cover = ensureHTTPS(img)
		}
	}
}

func instagramBestVideo(item map[string]any) string {
	return instagramBestCandidate(getSlice(item, "video_versions"))
}

func instagramBestImage(item map[string]any) string {
	return instagramBestCandidate(getSlice(item, "image_versions2", "candidates"))
}

func instagramBestCandidate(candidates []any) string {
	bestURL := ""
	bestScore := -1.0
	for _, raw := range candidates {
		c, _ := raw.(map[string]any)
		if c == nil {
			continue
		}
		u := getString(c, "url")
		if u == "" {
			continue
		}
		score := getFloat(c, "width") * getFloat(c, "height")
		if score <= 0 {
			score = float64(len(u))
		}
		if score > bestScore {
			bestScore = score
			bestURL = u
		}
	}
	return bestURL
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len([]rune(line)) > 80 {
				return string([]rune(line)[:80])
			}
			return line
		}
	}
	return ""
}
