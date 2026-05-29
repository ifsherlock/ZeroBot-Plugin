package mediaparser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	kuaishouUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 14_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.0 Mobile/15E148 Safari/604.1"
	gifshowBase = "https://m.gifshow.com"
)

func parseKuaishou(cfg config, raw string) (mediaMeta, error) {
	html, finalURL, err := fetchKuaishouHTML(raw)
	if err != nil {
		return mediaMeta{}, err
	}
	title, author, userID := extractKuaishouMeta(html)
	if title == "" {
		title = "快手视频"
	}
	if len([]rune(title)) > 100 {
		title = string([]rune(title)[:100])
	}
	if userID != "" {
		if author != "" {
			author += "(uid:" + userID + ")"
		} else {
			author = "(uid:" + userID + ")"
		}
	}
	imageHeaders := buildHeaders(false, "https://www.kuaishou.com/", defaultUA)
	videoHeaders := buildHeaders(true, "https://www.kuaishou.com/", defaultUA)
	if ssr := parseKuaishouInitState(html); ssr != nil {
		avatar := firstNestedHTTPURLByKeys(ssr.Photo, 5, "avatar", "head", "profile")
		cover := firstNestedHTTPURLByKeys(ssr.Photo, 5, "cover", "poster", "thumbnail")
		if ssr.Type == "video" && ssr.VideoURL != "" {
			return mediaMeta{
				URL:        raw,
				SourceURL:  raw,
				Platform:   "kuaishou",
				Title:      title,
				Author:     author,
				Avatar:     avatar,
				Timestamp:  timestampFromPhoto(ssr.Photo, ssr.VideoURL),
				Cover:      cover,
				VideoURLs:  [][]string{{ssr.VideoURL}},
				ImageURLs:  nil,
				VideoHeads: videoHeaders,
				ImageHeads: imageHeaders,
			}, nil
		}
		if ssr.Type == "album" && len(ssr.ImageURLLists) > 0 {
			first := ""
			if len(ssr.ImageURLLists[0]) > 0 {
				first = ssr.ImageURLLists[0][0]
			}
			return mediaMeta{
				URL:        raw,
				SourceURL:  raw,
				Platform:   "kuaishou",
				Title:      firstNonEmpty(title, "快手图集"),
				Author:     author,
				Avatar:     avatar,
				Timestamp:  timestampFromPhoto(ssr.Photo, first),
				Cover:      firstNonEmpty(cover, first),
				VideoURLs:  nil,
				ImageURLs:  ssr.ImageURLLists,
				VideoHeads: videoHeaders,
				ImageHeads: imageHeaders,
			}, nil
		}
	}
	if v := parseKuaishouVideoRegex(html); v != "" {
		return mediaMeta{
			URL:        raw,
			SourceURL:  raw,
			Platform:   "kuaishou",
			Title:      title,
			Author:     author,
			Avatar:     firstNestedHTTPURLByKeys(getKuaishouState(html), 6, "avatar", "head", "profile"),
			Timestamp:  uploadTimeFromURL(v),
			Cover:      firstNestedHTTPURLByKeys(getKuaishouState(html), 6, "cover", "poster", "thumbnail"),
			VideoURLs:  [][]string{{v}},
			VideoHeads: videoHeaders,
			ImageHeads: imageHeaders,
		}, nil
	}
	if imgs := parseKuaishouAlbumRegex(html); len(imgs) > 0 {
		return mediaMeta{
			URL:        raw,
			SourceURL:  raw,
			Platform:   "kuaishou",
			Title:      firstNonEmpty(title, "快手图集"),
			Author:     author,
			Avatar:     firstNestedHTTPURLByKeys(getKuaishouState(html), 6, "avatar", "head", "profile"),
			Timestamp:  uploadTimeFromURL(imgs[0][0]),
			Cover:      imgs[0][0],
			ImageURLs:  imgs,
			VideoHeads: videoHeaders,
			ImageHeads: imageHeaders,
		}, nil
	}
	return mediaMeta{}, fmt.Errorf("快手未找到有效媒体: %s", finalURL)
}

func fetchKuaishouHTML(raw string) (string, string, error) {
	headers := map[string]string{
		"User-Agent": kuaishouUA,
		"Accept":     "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
	}
	u, _ := url.Parse(raw)
	target := raw
	if strings.Contains(u.Host, "v.kuaishou.com") {
		resp, err := httpRequestNoRedirect(raw, headers)
		if err != nil {
			return "", raw, err
		}
		defer resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", raw, fmt.Errorf("快手短链未返回跳转")
		}
		target = loc
		if !strings.Contains(strings.ToLower(target), "kuaishou.com") {
			target = toGifshowURL(target)
		}
	} else if strings.Contains(strings.ToLower(u.Host), "chenzhongtech.com") {
		target = toGifshowURL(raw)
	}
	html, finalURL, status, err := fetchText(target, headers, true)
	if err != nil {
		return "", finalURL, err
	}
	if status >= 400 {
		return "", finalURL, fmt.Errorf("快手页面 HTTP %d", status)
	}
	return html, finalURL, nil
}

func toGifshowURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if m := regexp.MustCompile(`/fw/photo/([^/?]+)`).FindStringSubmatch(u.Path); len(m) > 1 {
		return gifshowBase + "/fw/photo/" + m[1]
	}
	return gifshowBase + u.Path
}

func extractKuaishouMeta(html string) (title, author, userID string) {
	if state := getKuaishouState(html); state != nil {
		for _, val := range state {
			m, ok := val.(map[string]any)
			if !ok {
				continue
			}
			photo := decodeMaybeJSONMap(m["photo"])
			if photo != nil {
				title = firstNonEmpty(title, getString(photo, "caption"))
				author = firstNonEmpty(author, getString(photo, "userName"))
				userID = firstNonEmpty(userID, getString(photo, "userId"))
			}
		}
	}
	if author == "" {
		if m := regexp.MustCompile(`"userName"\s*:\s*"([^"]+)"`).FindStringSubmatch(html); len(m) > 1 {
			author = htmlUnescape(m[1])
		}
	}
	if userID == "" {
		if m := regexp.MustCompile(`"userId"\s*:\s*["']?(\d+)["']?`).FindStringSubmatch(html); len(m) > 1 {
			userID = m[1]
		}
	}
	if title == "" {
		if m := regexp.MustCompile(`"caption"\s*:\s*"([^"]*(?:\\.[^"]*)*)"`).FindStringSubmatch(html); len(m) > 1 {
			title = strings.ReplaceAll(m[1], `\"`, `"`)
		}
	}
	if title == "" {
		title = titleTag(html)
	}
	return
}

type ksSSR struct {
	Type          string
	VideoURL      string
	ImageURLLists [][]string
	Photo         map[string]any
}

func parseKuaishouInitState(html string) *ksSSR {
	state := getKuaishouState(html)
	if state == nil {
		return nil
	}
	var photo, single map[string]any
	for _, val := range state {
		m, ok := val.(map[string]any)
		if !ok {
			continue
		}
		photo = decodeMaybeJSONMap(m["photo"])
		if photo != nil {
			single = decodeMaybeJSONMap(m["single"])
			break
		}
	}
	if photo == nil {
		return nil
	}
	mv := getSlice(photo, "mainMvUrls")
	for _, item := range mv {
		if m, ok := item.(map[string]any); ok {
			u := getString(m, "url")
			if strings.Contains(u, ".mp4") {
				return &ksSSR{Type: "video", VideoURL: minMP4(u), Photo: photo}
			}
		}
	}
	ext := decodeMaybeJSONMap(photo["ext_params"])
	atlas := decodeMaybeJSONMap(ext["atlas"])
	if getFloat(photo, "type") == 1 && atlas != nil {
		cdns := stringsFromMixed(atlas["cdn"])
		for _, item := range getSlice(atlas, "cdnList") {
			if m, ok := item.(map[string]any); ok && getString(m, "cdn") != "" {
				cdns = append(cdns, getString(m, "cdn"))
			}
		}
		paths := stringsFromMixed(atlas["list"])
		if album := buildKuaishouAlbum(cdns, paths); len(album) > 0 {
			return &ksSSR{Type: "album", ImageURLLists: album, Photo: photo}
		}
	}
	if single != nil {
		cdns := []string{}
		for _, item := range getSlice(single, "cdnList") {
			if m, ok := item.(map[string]any); ok && getString(m, "cdn") != "" {
				cdns = append(cdns, getString(m, "cdn"))
			}
		}
		_ = cdns
	}
	cover := [][]string{}
	for _, item := range getSlice(photo, "coverUrls") {
		if m, ok := item.(map[string]any); ok && getString(m, "url") != "" {
			cover = append(cover, []string{getString(m, "url")})
		}
	}
	if len(cover) > 0 && getFloat(photo, "type") != 1 {
		return &ksSSR{Type: "album", ImageURLLists: cover, Photo: photo}
	}
	return nil
}

func getKuaishouState(html string) map[string]any {
	for _, marker := range []string{"window.INIT_STATE", "window.__APOLLO_STATE__"} {
		state, err := extractAssignedJSONObject(html, marker)
		if err == nil {
			return state
		}
	}
	return nil
}

func decodeMaybeJSONMap(v any) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return x
	case string:
		var out map[string]any
		if json.Unmarshal([]byte(x), &out) == nil {
			return out
		}
	}
	return nil
}

func parseKuaishouVideoRegex(html string) string {
	patterns := []string{
		`"(?:url|srcNoMark|photoUrl|videoUrl)"\s*:\s*"(https?://[^"]+?\.mp4[^"]*)"`,
		`"url"\s*:\s*"(https?://[^"]+?\.mp4[^"]*)"`,
	}
	for _, p := range patterns {
		if m := regexp.MustCompile(p).FindStringSubmatch(html); len(m) > 1 {
			return minMP4(strings.ReplaceAll(m[1], `\/`, `/`))
		}
	}
	return ""
}

func parseKuaishouAlbumRegex(html string) [][]string {
	cdns := []string{}
	for _, p := range []string{`"cdnList"\s*:\s*\[.*?"cdn"\s*:\s*"([^"]+)"`, `"cdn"\s*:\s*\["([^"]+)"`, `"cdn"\s*:\s*"([^"]+)"`} {
		for _, m := range regexp.MustCompile(p).FindAllStringSubmatch(html, -1) {
			if len(m) > 1 {
				cdns = append(cdns, m[1])
			}
		}
	}
	paths := []string{}
	for _, m := range regexp.MustCompile(`"/ufile/atlas/[^"]+?\.jpg"`).FindAllString(html, -1) {
		paths = append(paths, strings.Trim(m, `"`))
	}
	return buildKuaishouAlbum(cdns, paths)
}

func buildKuaishouAlbum(cdns, paths []string) [][]string {
	out := [][]string{}
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		group := []string{}
		for _, cdn := range cdns {
			cdn = regexp.MustCompile(`^https?://`).ReplaceAllString(cdn, "")
			if cdn != "" {
				group = append(group, "https://"+cdn+path)
			}
		}
		if len(group) > 0 && !seen[group[0]] {
			seen[group[0]] = true
			out = append(out, group)
		}
	}
	return out
}

func stringsFromMixed(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := []string{}
		for _, item := range x {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func minMP4(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return raw
	}
	file := strings.Split(parts[len(parts)-1], "?")[0]
	dir := strings.Join(parts[:len(parts)-1], "/")
	return "https://" + u.Host + "/" + dir + "/" + file
}

func timestampFromPhoto(photo map[string]any, fallback string) string {
	if ts := getFloat(photo, "timestamp"); ts > 0 {
		if ts > 1e12 {
			ts = ts / 1000
		}
		return time.Unix(int64(ts), 0).Format("2006-01-02")
	}
	return uploadTimeFromURL(fallback)
}

func uploadTimeFromURL(raw string) string {
	if m := regexp.MustCompile(`/(\d{4})/(\d{2})/(\d{2})/`).FindStringSubmatch(raw); len(m) > 3 {
		return m[1] + "-" + m[2] + "-" + m[3]
	}
	if m := regexp.MustCompile(`_(\d{11,13})_`).FindStringSubmatch(raw); len(m) > 1 {
		ts, _ := strconv.ParseInt(m[1], 10, 64)
		if len(m[1]) == 13 {
			ts /= 1000
		}
		if ts > 0 {
			return time.Unix(ts, 0).Format("2006-01-02")
		}
	}
	return ""
}
