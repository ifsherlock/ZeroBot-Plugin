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

const keylolUA = "Mozilla/5.0 (Linux; Android 10) AppleWebKit/537.36 Mobile"

var (
	keylolAPIBase  = "https://keylol.com/api/mobile/index.php"
	keylolImageExt = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|webp|gif)(?:\?|$)`)
)

func parseKeylol(cfg config, raw string) (mediaMeta, error) {
	tid := keylolThreadID(raw)
	if tid == "" {
		return mediaMeta{}, fmt.Errorf("keylol thread id not found")
	}
	api, err := url.Parse(keylolAPIBase)
	if err != nil {
		return mediaMeta{}, err
	}
	q := api.Query()
	q.Set("module", "viewthread")
	q.Set("tid", tid)
	q.Set("page", "1")
	q.Set("version", "5")
	api.RawQuery = q.Encode()

	headers := keylolHeaders(cfg, raw)
	body, finalURL, status, err := fetchText(api.String(), headers, true)
	if err != nil {
		return mediaMeta{}, err
	}
	if status >= 400 {
		return mediaMeta{}, fmt.Errorf("keylol API HTTP %d: %s", status, truncate(body, 180))
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return mediaMeta{}, err
	}
	vars := getMap(data, "Variables")
	thread := getMap(vars, "thread")
	posts := getSlice(vars, "postlist")
	if len(posts) == 0 {
		msg := firstNonEmpty(getString(data, "Message", "messagestr"), getString(data, "Message", "messageval"))
		if strings.TrimSpace(cfg.KeylolCookie) == "" {
			return mediaMeta{}, fmt.Errorf("keylol requires cookie: %s", firstNonEmpty(msg, "postlist empty"))
		}
		return mediaMeta{}, fmt.Errorf("keylol postlist empty: %s", firstNonEmpty(msg, "no first post"))
	}
	post, _ := posts[0].(map[string]any)
	if post == nil {
		return mediaMeta{}, fmt.Errorf("keylol first post invalid")
	}

	messageHTML := firstNonEmpty(getString(post, "message"), getString(post, "message_html"))
	images := keylolExtractImages(messageHTML, getMap(post, "attachments"), finalURL)
	desc := keylolCleanMessage(messageHTML)
	title := firstNonEmpty(getString(thread, "subject"), getString(post, "subject"))
	author := cardDisplayAuthor(firstNonEmpty(getString(post, "author"), getString(thread, "author")))
	authorID := firstNonEmpty(getString(post, "authorid"), getString(thread, "authorid"))
	timestamp := keylolTime(firstNonEmpty(getString(post, "dateline"), getString(thread, "dateline")))
	avatar := keylolAvatarURL(authorID)

	return mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "keylol",
		Title:      title,
		Author:     author,
		Avatar:     avatar,
		Timestamp:  timestamp,
		Desc:       desc,
		ImageURLs:  images,
		ImageHeads: keylolHeaders(cfg, raw),
	}, nil
}

func keylolHeaders(cfg config, referer string) map[string]string {
	headers := buildHeaders(false, firstNonEmpty(referer, "https://keylol.com/"), keylolUA)
	headers["Accept"] = "application/json,text/plain,*/*"
	if cfg.KeylolCookie != "" {
		headers["Cookie"] = cfg.KeylolCookie
	}
	return headers
}

func keylolThreadID(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		for _, key := range []string{"tid", "ptid"} {
			if v := u.Query().Get(key); regexp.MustCompile(`^\d+$`).MatchString(v) {
				return v
			}
		}
	}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)/(?:t|thread-)(\d+)(?:[-/.]|$)`),
		regexp.MustCompile(`(?i)[?&]tid=(\d+)`),
	} {
		if m := re.FindStringSubmatch(raw); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func keylolExtractImages(messageHTML string, attachments map[string]any, base string) [][]string {
	seen := map[string]bool{}
	out := [][]string{}
	add := func(raw string) {
		raw = strings.TrimSpace(htmlUnescape(raw))
		raw = strings.Trim(raw, ` "'`)
		if raw == "" {
			return
		}
		raw = absolutize(base, ensureHTTPS(raw))
		if !keylolUsableImage(raw) || seen[raw] {
			return
		}
		seen[raw] = true
		out = append(out, []string{raw})
	}
	imgRe := regexp.MustCompile(`(?is)<img\b[^>]*(?:file|zoomfile|src)=["']([^"']+)["'][^>]*>`)
	for _, m := range imgRe.FindAllStringSubmatch(messageHTML, -1) {
		add(m[1])
	}
	for _, raw := range nestedHTTPURLs(attachments, 6) {
		add(raw)
	}
	return dedupeMediaGroups(out)
}

func keylolUsableImage(raw string) bool {
	low := strings.ToLower(raw)
	if !keylolImageExt.MatchString(low) {
		return false
	}
	for _, bad := range []string{
		"/static/image/", "/uc_server/data/avatar/", "/common/usergroup/",
		"smilies/", "avatar_", "userinfo.gif", "qq_share.png",
		"fav.gif", "agree.gif", "collection.png", "rec_add.gif",
	} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	return true
}

func keylolCleanMessage(messageHTML string) string {
	s := regexp.MustCompile(`(?is)<style[^>]*>.*?</style>|<script[^>]*>.*?</script>`).ReplaceAllString(messageHTML, "")
	s = regexp.MustCompile(`(?is)<img\b[^>]*>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(s, "")
	s = htmlUnescape(s)
	s = regexp.MustCompile(`(?i)\[/?(?:attach|img|url|quote|size|color|font|align|b|i|u|list|\*)(?:=[^\]]*)?\]`).ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	cleaned := make([]string, 0, len(lines))
	spaceRE := regexp.MustCompile(`[ \t]+`)
	for _, line := range lines {
		line = strings.TrimSpace(spaceRE.ReplaceAllString(line, " "))
		if line == "" || keylolNoiseLine(line) {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(collapseDuplicateLines(cleaned), "\n")
}

func keylolNoiseLine(line string) bool {
	if keylolImageExt.MatchString(strings.ToLower(line)) {
		return true
	}
	for _, needle := range []string{
		"转载或引用本网站内容", "不代表本社区立场", "本网站保留追究",
		"下载附件", "点击文件名下载附件", "查看全部评分", "评分参与人数",
	} {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func collapseDuplicateLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	var last string
	for _, line := range lines {
		if line == last {
			continue
		}
		out = append(out, line)
		last = line
	}
	return out
}

func keylolTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 1000000000 {
		return time.Unix(sec, 0).Format("2006-01-02 15:04")
	}
	return raw
}

func keylolAvatarURL(authorID string) string {
	uid, err := strconv.Atoi(strings.TrimSpace(authorID))
	if err != nil || uid <= 0 {
		return ""
	}
	p := fmt.Sprintf("%09d", uid)
	return fmt.Sprintf("https://keylol.com/uc_server/data/avatar/%s/%s/%s/%s_avatar_middle.jpg", p[0:3], p[3:5], p[5:7], p[7:9])
}
