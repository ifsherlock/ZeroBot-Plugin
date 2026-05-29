package mediaparser

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	bvRE        = regexp.MustCompile(`(?i)BV[0-9A-Za-z]{10,}`)
	avRE        = regexp.MustCompile(`(?i)av(\d+)`)
	epRE        = regexp.MustCompile(`(?i)/bangumi/play/ep(\d+)`)
	ssRE        = regexp.MustCompile(`(?i)/bangumi/play/ss(\d+)`)
	opusRE      = regexp.MustCompile(`(?i)/(?:opus|dynamic)/(\d+)`)
	tBiliRE     = regexp.MustCompile(`(?i)t\.bilibili\.com/(\d+)`)
	wbiMixTab   = []int{46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52}
	biliQuality = map[string]int{
		"不限制":     0,
		"4K":      120,
		"1080P60": 116,
		"1080P+":  112,
		"1080P":   80,
		"720P":    64,
		"480P":    32,
		"360P":    16,
	}
)

type biliResponse[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
	Result  T      `json:"result"`
}

type biliView struct {
	AID      int64  `json:"aid"`
	BVID     string `json:"bvid"`
	CID      int64  `json:"cid"`
	Title    string `json:"title"`
	Desc     string `json:"desc"`
	Pic      string `json:"pic"`
	Duration int64  `json:"duration"`
	PubDate  int64  `json:"pubdate"`
	Owner    struct {
		Name string `json:"name"`
		MID  int64  `json:"mid"`
		Face string `json:"face"`
	} `json:"owner"`
	Pages []struct {
		CID      int64  `json:"cid"`
		Page     int    `json:"page"`
		Part     string `json:"part"`
		Duration int64  `json:"duration"`
	} `json:"pages"`
}

type biliPlay struct {
	Quality       int       `json:"quality"`
	AcceptQuality []int     `json:"accept_quality"`
	VideoInfo     *biliPlay `json:"video_info"`
	Durl          []struct {
		URL       string   `json:"url"`
		Size      int64    `json:"size"`
		BackupURL []string `json:"backup_url"`
	} `json:"durl"`
	Dash struct {
		Video []biliDashMedia `json:"video"`
		Audio []biliDashMedia `json:"audio"`
	} `json:"dash"`
}

type biliDashMedia struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	BaseURL2  string   `json:"base_url"`
	BackupURL []string `json:"backupUrl"`
	Bandwidth int      `json:"bandwidth"`
	Codecs    string   `json:"codecs"`
}

type biliPGCSeason struct {
	Title       string `json:"title"`
	SeasonTitle string `json:"season_title"`
	Evaluate    string `json:"evaluate"`
	Summary     string `json:"summary"`
	Cover       string `json:"cover"`
	UpInfo      struct {
		Name string `json:"name"`
		MID  int64  `json:"mid"`
		UID  int64  `json:"uid"`
	} `json:"up_info"`
	Publisher struct {
		Name string `json:"name"`
		MID  int64  `json:"mid"`
	} `json:"publisher"`
	Episodes []biliPGCEpisode `json:"episodes"`
}

type biliPGCEpisode struct {
	EPID      int64  `json:"ep_id"`
	AID       int64  `json:"aid"`
	CID       int64  `json:"cid"`
	Title     string `json:"title"`
	LongTitle string `json:"long_title"`
	ShareCopy string `json:"share_copy"`
	Cover     string `json:"cover"`
	PubTime   int64  `json:"pub_time"`
}

func parseBilibili(cfg config, raw string) (mediaMeta, error) {
	expanded, _ := expandBiliURL(raw)
	if expanded == "" {
		expanded = raw
	}
	u, _ := url.Parse(expanded)
	if u == nil {
		return mediaMeta{}, errors.New("B站链接解析失败")
	}
	if epRE.MatchString(u.Path) || ssRE.MatchString(u.Path) {
		return parseBilibiliPGCNative(cfg, expanded)
	}
	if opusID := extractBiliOpusID(expanded); opusID != "" {
		return parseBilibiliOpus(cfg, expanded, raw, opusID)
	}
	view, err := fetchBiliView(cfg, expanded)
	if err != nil {
		return mediaMeta{}, err
	}
	if view.CID == 0 && len(view.Pages) > 0 {
		view.CID = view.Pages[0].CID
	}
	if view.CID == 0 || (view.AID == 0 && view.BVID == "") {
		return mediaMeta{}, errors.New("B站视频缺少 aid/bvid/cid")
	}
	play, err := fetchBiliPlay(cfg, view.AID, view.BVID, view.CID, "")
	if err != nil {
		return mediaMeta{}, err
	}
	videoURL := buildBiliVideoURL(cfg, play)
	if videoURL == "" {
		return mediaMeta{}, errors.New("B站播放地址为空")
	}
	headers := biliHeaders(expanded, cfg)
	meta := mediaMeta{
		URL:        expanded,
		SourceURL:  raw,
		Platform:   "bilibili",
		Title:      view.Title,
		Author:     view.Owner.Name,
		Avatar:     ensureHTTPS(view.Owner.Face),
		Timestamp:  formatUnix(view.PubDate),
		Desc:       view.Desc,
		Cover:      view.Pic,
		VideoURLs:  [][]string{{videoURL}},
		VideoHeads: headers,
		ImageHeads: headers,
		ForceLocal: strings.HasPrefix(videoURL, "dash:"),
	}
	logDebug(cfg, "bilibili metadata title=%q author=%q cid=%d q=%d video=%s", meta.Title, meta.Author, view.CID, play.Quality, truncate(videoURL, 120))
	return meta, nil
}

func parseBilibiliPGC(cfg config, raw string) (mediaMeta, error) {
	return mediaMeta{}, fmt.Errorf("B站番剧/PGC Go 原生解析还在补齐中: %s", raw)
}

func parseBilibiliOpus(cfg config, pageURL, sourceURL, opusID string) (mediaMeta, error) {
	api := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=" + url.QueryEscape(opusID)
	var resp biliResponse[map[string]any]
	if err := biliJSON(cfg, api, pageURL, &resp); err != nil {
		return mediaMeta{}, err
	}
	if resp.Code != 0 {
		return mediaMeta{}, fmt.Errorf("B站动态 API: %d %s", resp.Code, resp.Message)
	}
	item := getMap(resp.Data, "item")
	if item == nil {
		return mediaMeta{}, fmt.Errorf("B站动态缺少 item: %s", opusID)
	}
	modules := biliModules(item)
	authorObj := getMap(modules, "module_author")
	dynamic := getMap(modules, "module_dynamic")
	major := getMap(dynamic, "major")
	desc := biliDynamicDesc(dynamic["desc"])
	pageDesc, pageImages := "", [][]string(nil)
	if desc == "" {
		pageDesc, pageImages = fetchBiliOpusPageContent(cfg, pageURL)
		desc = pageDesc
	}
	title := firstNonEmpty(
		getString(modules, "module_title", "text"),
		biliMajorTitle(major),
		"B站图文动态",
	)
	images := biliDynamicImages(major)
	if len(images) == 0 && len(pageImages) > 0 {
		images = pageImages
	}
	if pageTitle, pageBody := splitBiliOpusText(desc); pageTitle != "" && title == "B站图文动态" {
		title = pageTitle
		desc = pageBody
	}
	videos := [][]string{}
	if len(images) == 0 && len(videos) == 0 {
		return mediaMeta{}, fmt.Errorf("B站动态未找到图片或视频: %s", opusID)
	}
	avatar := firstNonEmpty(
		getString(authorObj, "face"),
		firstNestedHTTPURLByKeys(authorObj, 6, "avatar", "image_src", "url"),
	)
	headers := biliHeaders(pageURL, cfg)
	return mediaMeta{
		URL:        pageURL,
		SourceURL:  sourceURL,
		Platform:   "bilibili",
		Title:      title,
		Author:     getString(authorObj, "name"),
		Avatar:     ensureHTTPS(avatar),
		Timestamp:  biliDynamicTime(authorObj),
		Desc:       desc,
		Cover:      firstURLGroup(images),
		VideoURLs:  videos,
		ImageURLs:  images,
		VideoHeads: headers,
		ImageHeads: headers,
	}, nil
}

func fetchBiliOpusPageContent(cfg config, pageURL string) (string, [][]string) {
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return "", nil
	}
	for k, v := range biliHeaders(pageURL, cfg) {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148")
	resp, err := client.Do(req)
	if err != nil {
		logDebug(cfg, "bilibili opus page fallback failed url=%s err=%v", pageURL, err)
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		logDebug(cfg, "bilibili opus page fallback status=%d url=%s", resp.StatusCode, pageURL)
		return "", nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil
	}
	text := string(body)
	marker := "window.__INITIAL_STATE__="
	start := strings.Index(text, marker)
	if start < 0 {
		return "", nil
	}
	start += len(marker)
	end := strings.Index(text[start:], ";(function")
	if end < 0 {
		return "", nil
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(text[start:start+end]), &state); err != nil {
		logDebug(cfg, "bilibili opus page state decode failed err=%v", err)
		return "", nil
	}
	detail := getMap(state, "opus", "detail")
	parts := []string{}
	images := [][]string{}
	for _, rawModule := range getSlice(detail, "modules") {
		mod, _ := rawModule.(map[string]any)
		content := getMap(mod, "module_content")
		for _, rawPara := range getSlice(content, "paragraphs") {
			para, _ := rawPara.(map[string]any)
			for _, rawNode := range getSlice(para, "text", "nodes") {
				node, _ := rawNode.(map[string]any)
				if s := getString(node, "word", "words"); s != "" {
					parts = append(parts, s)
				}
			}
			for _, rawPic := range getSlice(para, "pic", "pics") {
				pic, _ := rawPic.(map[string]any)
				if u := ensureHTTPS(getString(pic, "url")); u != "" {
					images = append(images, []string{u})
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), uniqueURLGroups(images)
}

func splitBiliOpusText(s string) (string, string) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	title := ""
	body := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(body) > 0 {
				body = append(body, "")
			}
			continue
		}
		if title == "" {
			title = line
			continue
		}
		body = append(body, line)
	}
	return title, strings.TrimSpace(strings.Join(body, "\n"))
}

func parseBilibiliPGCNative(cfg config, raw string) (mediaMeta, error) {
	epID := ""
	seasonID := ""
	if m := epRE.FindStringSubmatch(raw); len(m) > 1 {
		epID = m[1]
	}
	if m := ssRE.FindStringSubmatch(raw); len(m) > 1 {
		seasonID = m[1]
	}
	season, ep, err := fetchBiliPGCSeason(cfg, epID, seasonID)
	if err != nil {
		return mediaMeta{}, err
	}
	if ep.EPID == 0 {
		return mediaMeta{}, errors.New("B站番剧缺少 ep_id")
	}
	play, err := fetchBiliPGCPlay(cfg, ep.EPID, raw)
	if err != nil {
		return mediaMeta{}, err
	}
	videoURL := buildBiliVideoURL(cfg, play)
	if videoURL == "" {
		return mediaMeta{}, errors.New("B站番剧播放地址为空")
	}
	headers := biliHeaders(raw, cfg)
	author := firstNonEmpty(season.UpInfo.Name, season.Publisher.Name, season.SeasonTitle, season.Title)
	if author == "" && season.UpInfo.MID > 0 {
		author = fmt.Sprintf("(uid:%d)", season.UpInfo.MID)
	}
	title := firstNonEmpty(ep.ShareCopy, ep.LongTitle, ep.Title, season.SeasonTitle, season.Title)
	if season.SeasonTitle != "" && title != "" && !strings.Contains(title, season.SeasonTitle) {
		title = season.SeasonTitle + " " + title
	}
	cover := firstNonEmpty(ep.Cover, season.Cover)
	meta := mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "bilibili",
		Title:      title,
		Author:     author,
		Timestamp:  formatUnix(ep.PubTime),
		Desc:       firstNonEmpty(season.Evaluate, season.Summary),
		Cover:      cover,
		VideoURLs:  [][]string{{videoURL}},
		VideoHeads: headers,
		ImageHeads: headers,
		ForceLocal: strings.HasPrefix(videoURL, "dash:"),
	}
	logDebug(cfg, "bilibili pgc metadata title=%q ep=%d cid=%d q=%d video=%s", meta.Title, ep.EPID, ep.CID, play.Quality, truncate(videoURL, 120))
	return meta, nil
}

func expandBiliURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	if host != "b23.tv" && !strings.HasSuffix(host, ".b23.tv") {
		return raw, nil
	}
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	req.Header.Set("User-Agent", defaultUA)
	c := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String(), nil
	}
	return raw, nil
}

func extractBiliOpusID(raw string) string {
	if m := opusRE.FindStringSubmatch(raw); len(m) > 1 {
		return m[1]
	}
	if m := tBiliRE.FindStringSubmatch(raw); len(m) > 1 {
		return m[1]
	}
	return ""
}

func biliModules(item map[string]any) map[string]any {
	modules := getMap(item, "modules")
	if modules != nil {
		return modules
	}
	out := map[string]any{}
	for _, raw := range getSlice(item, "modules") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if strings.HasPrefix(k, "module_") {
				out[k] = v
			}
		}
	}
	return out
}

func biliDynamicDesc(descObj any) string {
	desc, _ := descObj.(map[string]any)
	if desc == nil {
		return ""
	}
	if text := strings.TrimSpace(getString(desc, "text")); text != "" {
		return text
	}
	parts := []string{}
	for _, raw := range getSlice(desc, "rich_text_nodes") {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text := firstNonEmpty(getString(node, "text"), getString(node, "orig_text")); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func biliMajorTitle(major map[string]any) string {
	for _, key := range []string{"opus", "article", "archive", "common", "draw"} {
		if title := getString(major, key, "title"); title != "" {
			return title
		}
	}
	return ""
}

func biliDynamicImages(major map[string]any) [][]string {
	groups := [][]string{}
	add := func(raw string) {
		raw = ensureHTTPS(strings.TrimSpace(raw))
		if raw != "" {
			groups = append(groups, []string{raw})
		}
	}
	for _, raw := range getSlice(major, "draw", "items") {
		switch item := raw.(type) {
		case map[string]any:
			add(firstNonEmpty(getString(item, "src"), getString(item, "url")))
		case string:
			add(item)
		}
	}
	opus := getMap(major, "opus")
	for _, key := range []string{"pics", "pictures", "items"} {
		for _, raw := range getSlice(opus, key) {
			switch item := raw.(type) {
			case map[string]any:
				add(firstNonEmpty(getString(item, "url"), getString(item, "src"), getString(item, "img_src")))
			case string:
				add(item)
			}
		}
	}
	for _, raw := range getSlice(major, "article", "covers") {
		if s, ok := raw.(string); ok {
			add(s)
		}
	}
	add(getString(major, "common", "cover"))
	return uniqueURLGroups(groups)
}

func biliDynamicTime(author map[string]any) string {
	if ts := getFloat(author, "pub_ts"); ts > 0 {
		return formatUnix(int64(ts))
	}
	return getString(author, "pub_time")
}

func firstURLGroup(groups [][]string) string {
	for _, group := range groups {
		if len(group) > 0 && group[0] != "" {
			return group[0]
		}
	}
	return ""
}

func fetchBiliView(cfg config, raw string) (biliView, error) {
	params := url.Values{}
	if bv := bvRE.FindString(raw); bv != "" {
		params.Set("bvid", bv)
	} else if m := avRE.FindStringSubmatch(raw); len(m) > 1 {
		params.Set("aid", m[1])
	} else if u, err := url.Parse(raw); err == nil {
		q := u.Query()
		if bv := q.Get("bvid"); bv != "" {
			params.Set("bvid", bv)
		} else if aid := q.Get("aid"); aid != "" {
			params.Set("aid", aid)
		}
	}
	if len(params) == 0 {
		return biliView{}, errors.New("未找到 B站 BV/AV 号")
	}
	api := "https://api.bilibili.com/x/web-interface/view?" + params.Encode()
	var resp biliResponse[biliView]
	if err := biliJSON(cfg, api, "", &resp); err != nil {
		return biliView{}, err
	}
	if resp.Code != 0 {
		return biliView{}, fmt.Errorf("B站 view API: %d %s", resp.Code, resp.Message)
	}
	return resp.Data, nil
}

func fetchBiliPlay(cfg config, aid int64, bvid string, cid int64, epID string) (biliPlay, error) {
	params := url.Values{}
	if bvid != "" {
		params.Set("bvid", bvid)
	} else if aid > 0 {
		params.Set("avid", strconv.FormatInt(aid, 10))
	}
	if epID != "" {
		params.Set("ep_id", epID)
	}
	params.Set("cid", strconv.FormatInt(cid, 10))
	params.Set("qn", strconv.Itoa(targetBiliQN(cfg)))
	params.Set("fnval", "4048")
	params.Set("fnver", "0")
	params.Set("fourk", "1")
	params.Set("high_quality", "1")
	signed, err := signBiliWBI(cfg, params)
	if err != nil {
		logDebug(cfg, "wbi sign failed fallback unsigned error=%v", err)
		signed = params.Encode()
	}
	api := "https://api.bilibili.com/x/player/wbi/playurl?" + signed
	var resp biliResponse[biliPlay]
	if err := biliJSON(cfg, api, "", &resp); err != nil {
		return biliPlay{}, err
	}
	if resp.Code != 0 {
		return biliPlay{}, fmt.Errorf("B站 playurl API: %d %s", resp.Code, resp.Message)
	}
	return resp.Data, nil
}

func fetchBiliPGCSeason(cfg config, epID, seasonID string) (biliPGCSeason, biliPGCEpisode, error) {
	params := url.Values{}
	if epID != "" {
		params.Set("ep_id", epID)
	} else if seasonID != "" {
		params.Set("season_id", seasonID)
	} else {
		return biliPGCSeason{}, biliPGCEpisode{}, errors.New("B站番剧链接缺少 ep_id/season_id")
	}
	api := "https://api.bilibili.com/pgc/view/web/season?" + params.Encode()
	var resp biliResponse[biliPGCSeason]
	if err := biliJSON(cfg, api, "https://www.bilibili.com", &resp); err != nil {
		return biliPGCSeason{}, biliPGCEpisode{}, err
	}
	if resp.Code != 0 {
		return biliPGCSeason{}, biliPGCEpisode{}, fmt.Errorf("B站 PGC season API: %d %s", resp.Code, resp.Message)
	}
	season := resp.Result
	if len(season.Episodes) == 0 {
		return season, biliPGCEpisode{}, errors.New("B站番剧分集列表为空")
	}
	if epID != "" {
		want, _ := strconv.ParseInt(epID, 10, 64)
		for _, ep := range season.Episodes {
			if ep.EPID == want {
				return season, ep, nil
			}
		}
		return season, biliPGCEpisode{}, fmt.Errorf("B站番剧未找到 ep_id=%s", epID)
	}
	return season, season.Episodes[0], nil
}

func fetchBiliPGCPlay(cfg config, epID int64, referer string) (biliPlay, error) {
	params := url.Values{}
	params.Set("ep_id", strconv.FormatInt(epID, 10))
	params.Set("qn", strconv.Itoa(targetBiliQN(cfg)))
	params.Set("fnval", "4048")
	params.Set("fnver", "0")
	params.Set("fourk", "1")
	params.Set("otype", "json")
	api := "https://api.bilibili.com/pgc/player/web/v2/playurl?" + params.Encode()
	var resp biliResponse[biliPlay]
	if err := biliJSON(cfg, api, referer, &resp); err != nil {
		return biliPlay{}, err
	}
	if resp.Code != 0 {
		return biliPlay{}, fmt.Errorf("B站 PGC playurl API: %d %s", resp.Code, resp.Message)
	}
	play := resp.Result
	if play.VideoInfo != nil {
		play = *play.VideoInfo
	}
	return play, nil
}

func targetBiliQN(cfg config) int {
	if qn, ok := biliQuality[cfg.BilibiliMaxQuality]; ok && qn > 0 {
		return qn
	}
	return 120
}

func buildBiliVideoURL(cfg config, play biliPlay) string {
	if play.VideoInfo != nil {
		play = *play.VideoInfo
	}
	if len(play.Dash.Video) > 0 {
		video := pickBiliVideo(cfg, play.Dash.Video)
		if video.BaseURL == "" {
			video.BaseURL = video.BaseURL2
		}
		if video.BaseURL == "" {
			return ""
		}
		audio := pickBiliAudio(play.Dash.Audio)
		audioURL := firstNonEmpty(audio.BaseURL, audio.BaseURL2)
		if audioURL != "" {
			return "dash:" + video.BaseURL + "||" + audioURL
		}
		return video.BaseURL
	}
	if len(play.Durl) > 0 {
		return play.Durl[0].URL
	}
	return ""
}

func pickBiliVideo(cfg config, videos []biliDashMedia) biliDashMedia {
	limit := biliQuality[cfg.BilibiliMaxQuality]
	filtered := make([]biliDashMedia, 0, len(videos))
	for _, v := range videos {
		if limit > 0 && v.ID > limit {
			continue
		}
		filtered = append(filtered, v)
	}
	if len(filtered) == 0 {
		filtered = videos
	}
	if cfg.AvoidAV1 {
		nonAV1 := filtered[:0]
		for _, v := range filtered {
			if !strings.Contains(strings.ToLower(v.Codecs), "av01") {
				nonAV1 = append(nonAV1, v)
			}
		}
		if len(nonAV1) > 0 {
			filtered = nonAV1
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.ID != b.ID {
			return a.ID > b.ID
		}
		if codecRank(a.Codecs) != codecRank(b.Codecs) {
			return codecRank(a.Codecs) > codecRank(b.Codecs)
		}
		return a.Bandwidth > b.Bandwidth
	})
	return filtered[0]
}

func pickBiliAudio(audios []biliDashMedia) biliDashMedia {
	if len(audios) == 0 {
		return biliDashMedia{}
	}
	sort.SliceStable(audios, func(i, j int) bool {
		if audios[i].ID != audios[j].ID {
			return audios[i].ID > audios[j].ID
		}
		return audios[i].Bandwidth > audios[j].Bandwidth
	})
	return audios[0]
}

func codecRank(codec string) int {
	c := strings.ToLower(codec)
	switch {
	case strings.Contains(c, "avc"):
		return 3
	case strings.Contains(c, "hev") || strings.Contains(c, "hvc"):
		return 2
	case strings.Contains(c, "av01"):
		return 0
	default:
		return 1
	}
}

func biliHeaders(referer string, cfg config) map[string]string {
	headers := map[string]string{
		"User-Agent": defaultUA,
		"Referer":    referer,
		"Origin":     "https://www.bilibili.com",
	}
	if cfg.BilibiliUseCookie && cfg.BilibiliCookie != "" {
		headers["Cookie"] = cfg.BilibiliCookie
	} else {
		headers["Cookie"] = biliGuestCookie()
	}
	return headers
}

func biliGuestCookie() string {
	sum := md5.Sum([]byte("mediaparser-buvid-" + time.Now().UTC().Format("20060102")))
	buvid := strings.ToUpper(hex.EncodeToString(sum[:]))
	return "buvid3=" + buvid + "; b_nut=" + strconv.FormatInt(time.Now().Unix(), 10) + "; _uuid=" + buvid
}

func biliJSON(cfg config, api, referer string, out any) error {
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return err
	}
	for k, v := range biliHeaders(firstNonEmpty(referer, "https://www.bilibili.com"), cfg) {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

func signBiliWBI(cfg config, params url.Values) (string, error) {
	type navData struct {
		WBIImg struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	}
	var nav biliResponse[navData]
	if err := biliJSON(cfg, "https://api.bilibili.com/x/web-interface/nav", "", &nav); err != nil {
		return "", err
	}
	imgKey := keyStem(nav.Data.WBIImg.ImgURL)
	subKey := keyStem(nav.Data.WBIImg.SubURL)
	if imgKey == "" || subKey == "" {
		return "", errors.New("missing wbi keys")
	}
	mixin := mixinKey(imgKey + subKey)
	params.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.Map(func(r rune) rune {
			if strings.ContainsRune("!'()*", r) {
				return -1
			}
			return r
		}, params.Get(k))
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}
	query := strings.Join(parts, "&")
	sum := md5.Sum([]byte(query + mixin))
	return query + "&w_rid=" + hex.EncodeToString(sum[:]), nil
}

func keyStem(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := u.Path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx >= 0 {
		base = base[:idx]
	}
	return base
}

func mixinKey(raw string) string {
	var b strings.Builder
	for _, idx := range wbiMixTab {
		if idx >= 0 && idx < len(raw) {
			b.WriteByte(raw[idx])
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}

func formatUnix(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}
