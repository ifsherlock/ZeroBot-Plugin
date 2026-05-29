package mediaparser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	toutiaoUA  = "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Mobile Safari/537.36"
	vodAPIBase = "https://vod.bytedanceapi.com/"
)

func parseToutiao(cfg config, raw string) (mediaMeta, error) {
	ctx, err := resolveToutiaoContext(raw)
	if err != nil {
		return mediaMeta{}, err
	}
	headers := map[string]string{"User-Agent": toutiaoUA, "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"}
	html, _, status, err := fetchText(ctx.PageURL, headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("今日头条页面 HTTP %d", status)
	}
	stateText, err := extractToutiaoState(html)
	if err != nil {
		return mediaMeta{}, err
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(stateText), &state); err != nil {
		return mediaMeta{}, err
	}
	article := getMap(state, "articleInfo")
	contentType := ctx.ContentType
	if contentType == "w" {
		if getString(article, "playAuthTokenV2") != "" {
			contentType = "video"
		} else {
			contentType = "article"
		}
	}
	if contentType == "video" {
		token := getString(article, "playAuthTokenV2")
		if token == "" {
			return mediaMeta{}, fmt.Errorf("今日头条视频缺少 playAuthTokenV2")
		}
		query, err := toutiaoVodQuery(token)
		if err != nil {
			return mediaMeta{}, err
		}
		vod, err := fetchToutiaoVod(query, ctx.PageURL)
		if err != nil {
			return mediaMeta{}, err
		}
		return buildToutiaoVideoMeta(ctx, state, vod)
	}
	return buildToutiaoArticleMeta(ctx, state), nil
}

type toutiaoContext struct {
	SourceURL   string
	ContentType string
	ContentID   string
	PageURL     string
}

func resolveToutiaoContext(raw string) (toutiaoContext, error) {
	if typ, id := toutiaoIdentity(raw); typ != "" && id != "" {
		return toutiaoContext{SourceURL: raw, ContentType: typ, ContentID: id, PageURL: toutiaoCanonicalURL(typ, id)}, nil
	}
	if regexp.MustCompile(`m\.toutiao\.com/is/`).MatchString(raw) {
		headers := map[string]string{"User-Agent": toutiaoUA}
		html, finalURL, _, err := fetchText(raw, headers, true)
		if err != nil {
			return toutiaoContext{}, err
		}
		typ, id := toutiaoIdentity(finalURL)
		if typ == "" || id == "" {
			if m := regexp.MustCompile(`https?://m\.toutiao\.com/(article|video|w)/(\d+)/?`).FindStringSubmatch(html); len(m) > 2 {
				typ, id = strings.ToLower(m[1]), m[2]
			}
		}
		if typ != "" && id != "" {
			return toutiaoContext{SourceURL: raw, ContentType: typ, ContentID: id, PageURL: toutiaoCanonicalURL(typ, id)}, nil
		}
	}
	return toutiaoContext{}, fmt.Errorf("不是支持的今日头条链接")
}

func toutiaoIdentity(raw string) (string, string) {
	if m := regexp.MustCompile(`/(article|video|w)/(\d+)`).FindStringSubmatch(raw); len(m) > 2 {
		return strings.ToLower(m[1]), m[2]
	}
	return "", ""
}

func toutiaoCanonicalURL(typ, id string) string {
	return "https://m.toutiao.com/" + typ + "/" + id + "/"
}

func extractToutiaoState(html string) (string, error) {
	re := regexp.MustCompile(`(?is)<script[^>]*>\s*(%7B.*?%7D)\s*</script>`)
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		decoded, err := url.QueryUnescape(strings.TrimSpace(m[1]))
		if err == nil && strings.Contains(decoded, `"articleInfo"`) {
			var probe map[string]any
			if json.Unmarshal([]byte(decoded), &probe) == nil {
				return decoded, nil
			}
		}
	}
	if m := regexp.MustCompile(`(?is)(%7B%22sessionConfig%22.*?%7D)`).FindStringSubmatch(html); len(m) > 1 {
		decoded, err := url.QueryUnescape(m[1])
		if err == nil && strings.Contains(decoded, `"articleInfo"`) {
			return decoded, nil
		}
	}
	return "", fmt.Errorf("无法从今日头条页面中提取状态数据")
}

func buildToutiaoArticleMeta(ctx toutiaoContext, state map[string]any) mediaMeta {
	article := getMap(state, "articleInfo")
	thread := getMap(article, "thread", "threadBase")
	seo := getMap(state, "seoTDK")
	title := firstNonEmpty(getString(article, "title"), getString(thread, "title"), getString(seo, "title"))
	content := firstNonEmpty(getString(article, "content"), getString(thread, "richContent"), getString(thread, "content"))
	images := [][]string{}
	seen := map[string]bool{}
	for _, u := range regexp.MustCompile(`(?is)<img[^>]+src=["']([^"']+)["']`).FindAllStringSubmatch(content, -1) {
		if len(u) > 1 {
			img := htmlUnescape(u[1])
			if img != "" && !seen[img] {
				seen[img] = true
				images = append(images, []string{img})
			}
		}
	}
	for _, group := range toutiaoThreadImages(thread) {
		if len(group) > 0 && !seen[group[0]] {
			seen[group[0]] = true
			images = append(images, group)
		}
	}
	avatar := firstNonEmpty(
		firstNestedHTTPURLByKeys(getMap(article, "mediaUser"), 5, "avatar", "image", "logo"),
		firstNestedHTTPURLByKeys(getMap(article, "thread", "threadBase", "user", "info"), 5, "avatar", "image", "logo"),
	)
	cover := ""
	if len(images) > 0 && len(images[0]) > 0 {
		cover = images[0][0]
	}
	if cover == "" {
		cover = firstNestedHTTPURLByKeys(article, 6, "cover", "thumb", "image")
	}
	return mediaMeta{
		URL:        ctx.SourceURL,
		SourceURL:  ctx.SourceURL,
		Platform:   "toutiao",
		Title:      title,
		Author:     toutiaoAuthor(article),
		Avatar:     avatar,
		Desc:       cleanHTMLText(content),
		Timestamp:  formatAnyTimestamp(getFloat(article, "publishTime")),
		Cover:      cover,
		ImageURLs:  images,
		VideoHeads: buildHeaders(true, ctx.PageURL, toutiaoUA),
		ImageHeads: buildHeaders(false, ctx.PageURL, toutiaoUA),
	}
}

func buildToutiaoVideoMeta(ctx toutiaoContext, state, vod map[string]any) (mediaMeta, error) {
	article := getMap(state, "articleInfo")
	title := getString(article, "title")
	if title == "" {
		return mediaMeta{}, fmt.Errorf("今日头条视频缺少标题")
	}
	urls := toutiaoVideoURLs(vod)
	if len(urls) == 0 {
		return mediaMeta{}, fmt.Errorf("今日头条 VOD 未返回播放地址")
	}
	avatar := firstNonEmpty(
		firstNestedHTTPURLByKeys(getMap(article, "mediaUser"), 5, "avatar", "image", "logo"),
		firstNestedHTTPURLByKeys(getMap(article, "thread", "threadBase", "user", "info"), 5, "avatar", "image", "logo"),
	)
	return mediaMeta{
		URL:        ctx.SourceURL,
		SourceURL:  ctx.SourceURL,
		Platform:   "toutiao",
		Title:      title,
		Author:     toutiaoAuthor(article),
		Avatar:     avatar,
		Desc:       cleanHTMLText(getString(article, "content")),
		Timestamp:  formatAnyTimestamp(getFloat(article, "publishTime")),
		Cover:      firstNestedHTTPURLByKeys(article, 6, "cover", "thumb", "image"),
		VideoURLs:  [][]string{urls},
		VideoHeads: buildHeaders(true, ctx.PageURL, toutiaoUA),
		ImageHeads: buildHeaders(false, ctx.PageURL, toutiaoUA),
	}, nil
}

func toutiaoVodQuery(token string) (string, error) {
	token = strings.TrimSpace(token)
	token += strings.Repeat("=", (4-len(token)%4)%4)
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("今日头条视频播放令牌解码失败: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(decoded, &obj); err != nil {
		return "", err
	}
	query := getString(obj, "GetPlayInfoToken")
	if query == "" {
		return "", fmt.Errorf("今日头条视频播放令牌中缺少 GetPlayInfoToken")
	}
	return strings.ReplaceAll(query, `\u0026`, "&"), nil
}

func fetchToutiaoVod(query, referer string) (map[string]any, error) {
	headers := map[string]string{"User-Agent": toutiaoUA, "Accept": "application/json,text/plain,*/*", "Referer": referer}
	body, _, status, err := fetchText(vodAPIBase+"?"+query, headers, true)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("获取今日头条视频信息失败 HTTP %d", status)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toutiaoVideoURLs(vod map[string]any) []string {
	list := getSlice(vod, "Result", "Data", "PlayInfoList")
	type ranked struct {
		b int
		u string
	}
	items := []ranked{}
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			u := getString(m, "MainPlayUrl")
			if u != "" {
				items = append(items, ranked{b: int(getFloat(m, "Bitrate")), u: u})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].b > items[j].b })
	out := []string{}
	for _, it := range items {
		out = append(out, it.u)
	}
	return out
}

func toutiaoThreadImages(thread map[string]any) [][]string {
	out := [][]string{}
	for _, key := range []string{"largeImageList", "originImageList", "ugcCutImageList", "thumbImageList"} {
		for _, item := range getSlice(thread, key) {
			if m, ok := item.(map[string]any); ok {
				group := []string{}
				for _, k := range []string{"url", "webUrl"} {
					if u := getString(m, k); u != "" {
						group = append(group, u)
					}
				}
				for _, nested := range getSlice(m, "urlList") {
					if nm, ok := nested.(map[string]any); ok && getString(nm, "url") != "" {
						group = append(group, getString(nm, "url"))
					}
				}
				if len(group) > 0 {
					out = append(out, uniqueStrings(group))
				}
			}
		}
	}
	return out
}

func toutiaoAuthor(article map[string]any) string {
	media := getMap(article, "mediaUser")
	threadUser := getMap(article, "thread", "threadBase", "user", "info")
	name := firstNonEmpty(getString(media, "screenName"), getString(article, "detailSource"), getString(threadUser, "name"))
	uid := firstNonEmpty(getString(media, "id"), getString(article, "creatorUid"), getString(threadUser, "userId"))
	if name != "" && uid != "" {
		return name + "(uid:" + uid + ")"
	}
	return firstNonEmpty(name, uid)
}
