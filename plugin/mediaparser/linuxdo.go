package mediaparser

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
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
	api := linuxdoBase + "/t/" + topicID + ".json"
	headers := linuxdoHeaders(raw)
	body, finalURL, status, err := fetchTextWithPlatform(cfg, "linuxdo", api, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("linux.do API HTTP %d: %s", status, truncate(body, 180))
	}
	meta, err := parseLinuxdoTopicJSON(raw, finalURL, body)
	if err != nil {
		return mediaMeta{}, err
	}
	meta.VideoHeads = buildHeaders(true, linuxdoReferer, linuxdoUA)
	meta.ImageHeads = buildHeaders(false, linuxdoReferer, linuxdoUA)
	return meta, nil
}

func linuxdoHeaders(referer string) map[string]string {
	headers := buildHeaders(false, firstNonEmpty(referer, linuxdoReferer), linuxdoUA)
	headers["Accept"] = "application/json,text/plain,*/*"
	return headers
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

func linuxdoPostNumber(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "t" && regexp.MustCompile(`^\d+$`).MatchString(parts[3]) {
		return parts[3]
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
	return strings.TrimSpace(s)
}

func linuxdoTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 19 {
		return strings.ReplaceAll(raw[:19], "T", " ")
	}
	return raw
}
