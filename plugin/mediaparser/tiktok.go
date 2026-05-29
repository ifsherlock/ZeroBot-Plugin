package mediaparser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	tiktokUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	tiktokReferer = "https://www.tiktok.com/"
	tiktokOrigin  = "https://www.tiktok.com"
)

func parseTikTok(cfg config, raw string) (mediaMeta, error) {
	headers := map[string]string{
		"User-Agent":      tiktokUA,
		"Referer":         tiktokReferer,
		"Accept-Language": "en-US,en;q=0.9",
	}
	pageURL, err := redirectURL(raw, headers)
	if err != nil {
		pageURL = raw
	}
	pageURL = stripQueryFragment(pageURL)
	html, finalURL, status, err := fetchText(pageURL, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("TikTok 页面 HTTP %d", status)
	}
	result, err := parseTikTokHTML(finalURL, html)
	if err != nil {
		return mediaMeta{}, err
	}
	result.SourceURL = raw
	result.Platform = "tiktok"
	result.VideoHeads = buildHeaders(true, tiktokReferer, tiktokUA)
	result.VideoHeads["Origin"] = tiktokOrigin
	result.ImageHeads = buildHeaders(false, tiktokReferer, tiktokUA)
	result.ImageHeads["Origin"] = tiktokOrigin
	return result, nil
}

func parseTikTokHTML(pageURL, html string) (mediaMeta, error) {
	itemID := ""
	if m := regexp.MustCompile(`/(?:video|photo)/(\d+)`).FindStringSubmatch(pageURL); len(m) > 1 {
		itemID = m[1]
	} else if m := regexp.MustCompile(`/v/(\d+)(?:\.html)?$`).FindStringSubmatch(pageURL); len(m) > 1 {
		itemID = m[1]
	}
	var item map[string]any
	var detail map[string]any
	for _, id := range []string{"__UNIVERSAL_DATA_FOR_REHYDRATION__", "SIGI_STATE"} {
		rawJSON := extractScriptJSON(html, id)
		if rawJSON == "" {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(rawJSON), &data) != nil {
			continue
		}
		if scope := getMap(data, "__DEFAULT_SCOPE__"); scope != nil {
			detail = getMap(scope, "webapp.video-detail")
			item = getMap(detail, "itemInfo", "itemStruct")
			if item != nil && (itemID == "" || getString(item, "id") == itemID) {
				break
			}
			userDetail := getMap(scope, "webapp.user-detail")
			for _, cand := range getSlice(userDetail, "itemList") {
				if m, ok := cand.(map[string]any); ok && (itemID == "" || getString(m, "id") == itemID) {
					item = m
					break
				}
			}
		}
		if item == nil {
			item = extractTikTokItem(data, itemID)
		}
		if item != nil {
			break
		}
	}
	if item == nil {
		if urls := extractTikTokVideoURLFromHTML(html); len(urls) > 0 {
			return mediaMeta{URL: pageURL, Title: "TikTok", VideoURLs: [][]string{urls}}, nil
		}
		return mediaMeta{}, fmt.Errorf("TikTok 页面未找到 itemStruct")
	}
	return buildTikTokMeta(item, detail, pageURL)
}

func extractTikTokItem(data map[string]any, itemID string) map[string]any {
	if mod := getMap(data, "ItemModule"); mod != nil {
		if itemID != "" {
			if x := getMap(mod, itemID); x != nil {
				return x
			}
		}
		for _, v := range mod {
			if m, ok := v.(map[string]any); ok {
				return m
			}
		}
	}
	var found map[string]any
	var walk func(any)
	walk = func(v any) {
		if found != nil {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if item := getMap(x, "itemStruct"); item != nil {
				if itemID == "" || getString(item, "id") == itemID {
					found = item
					return
				}
			}
			if getString(x, "id") != "" && (x["video"] != nil || x["imagePost"] != nil || x["imagePostInfo"] != nil) {
				if itemID == "" || getString(x, "id") == itemID {
					found = x
					return
				}
			}
			for _, vv := range x {
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	walk(data)
	return found
}

func buildTikTokMeta(item, detail map[string]any, pageURL string) (mediaMeta, error) {
	author := getMap(item, "author")
	uniqueID := firstNonEmpty(getString(author, "uniqueId"), getString(author, "unique_id"))
	nickname := getString(author, "nickname")
	authorText := nickname
	if uniqueID != "" {
		if authorText != "" {
			authorText += "(@" + strings.TrimPrefix(uniqueID, "@") + ")"
		} else {
			authorText = "@" + strings.TrimPrefix(uniqueID, "@")
		}
	}
	avatar := firstNonEmpty(
		firstNestedHTTPURL(getMap(author, "avatarThumb"), 4),
		firstNestedHTTPURL(getMap(author, "avatarMedium"), 4),
		firstNestedHTTPURL(getMap(author, "avatarLarger"), 4),
	)
	imageGroups := extractTikTokImageGroups(item)
	videoGroups := [][]string{}
	cover := firstNestedHTTPURL(getMap(item, "video", "cover"), 4)
	if len(imageGroups) == 0 {
		urls := nestedHTTPURLs(getMap(item, "video"), 6)
		if len(urls) > 0 {
			videoGroups = append(videoGroups, urls)
		}
	}
	if len(imageGroups)+len(videoGroups) == 0 {
		return mediaMeta{}, fmt.Errorf("TikTok 未找到媒体URL")
	}
	title := getString(item, "desc")
	if title == "" {
		title = firstNonEmpty(getString(detail, "shareMeta", "desc"), getString(detail, "shareMeta", "title"), "TikTok")
	}
	itemID := getString(item, "id")
	display := buildTikTokDisplayURL(pageURL, uniqueID, itemID, len(imageGroups) > 0)
	return mediaMeta{
		URL:       display,
		SourceURL: pageURL,
		Platform:  "tiktok",
		Title:     title,
		Author:    authorText,
		Avatar:    avatar,
		Timestamp: formatAnyTimestamp(getFloat(item, "createTime")),
		Cover:     cover,
		VideoURLs: videoGroups,
		ImageURLs: imageGroups,
	}, nil
}

func extractTikTokImageGroups(item map[string]any) [][]string {
	root := item["imagePostInfo"]
	if root == nil {
		root = item["imagePost"]
	}
	var list []any
	switch x := root.(type) {
	case []any:
		list = x
	case map[string]any:
		for _, key := range []string{"images", "imageList", "imagePostImages", "imagePostImageList"} {
			if arr, ok := x[key].([]any); ok {
				list = arr
				break
			}
		}
	}
	groups := [][]string{}
	for _, item := range list {
		urls := nestedHTTPURLs(item, 5)
		if len(urls) > 0 {
			groups = append(groups, urls)
		}
	}
	return groups
}

func extractTikTokVideoURLFromHTML(html string) []string {
	if m := regexp.MustCompile(`"playAddr":"([^"]+)"`).FindStringSubmatch(html); len(m) > 1 {
		decoded := strings.ReplaceAll(strings.ReplaceAll(m[1], `\u002F`, "/"), `\/`, "/")
		if strings.HasPrefix(decoded, "http") {
			return []string{decoded}
		}
	}
	return nil
}

func buildTikTokDisplayURL(pageURL, uniqueID, itemID string, isGallery bool) string {
	uniqueID = strings.TrimPrefix(uniqueID, "@")
	if uniqueID != "" && itemID != "" {
		kind := "video"
		if isGallery {
			kind = "photo"
		}
		return "https://www.tiktok.com/@" + uniqueID + "/" + kind + "/" + itemID
	}
	return pageURL
}

func stripQueryFragment(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
