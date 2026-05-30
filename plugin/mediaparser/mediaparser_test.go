package mediaparser

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionOKSeparatesPrivateAndGroupAccess(t *testing.T) {
	cfg := config{
		PrivateAccessMode: accessWhitelist,
		GroupAccessMode:   accessWhitelist,
		UserWhitelist:     map[int64]bool{10000: true},
		GroupWhitelist:    map[int64]bool{},
		UserBlacklist:     map[int64]bool{},
		GroupBlacklist:    map[int64]bool{},
	}

	if ok, reason := permissionOK(cfg, 10000, 0); !ok {
		t.Fatalf("private whitelisted user should pass, reason=%s", reason)
	}
	if ok, reason := permissionOK(cfg, 10000, 123456); ok {
		t.Fatalf("group should not pass via user whitelist, reason=%s", reason)
	}
}

func TestLiveUserLinks(t *testing.T) {
	if os.Getenv("MEDIAPARSER_LIVE") == "" {
		t.Skip("set MEDIAPARSER_LIVE=1 to hit live platforms")
	}
	cfg := defaultConfig()
	cases := []struct {
		name string
		fn   func() (mediaMeta, error)
	}{
		{"xiaoheihe_bbs", func() (mediaMeta, error) {
			return parseXiaoheihe(cfg, "https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?h_camp=link&h_session_id=gI0l4ZUMQmztkMEA&h_src=YXBwX3NoYXJl&link_id=975771f1d3a9")
		}},
		{"xiaohongshu_h5", func() (mediaMeta, error) {
			return parseXiaohongshu(cfg, "https://www.xiaohongshu.com/discovery/item/69fe08f800000000350242fc?app_platform=android&ignoreEngage=true&app_version=9.21.0&share_from_user_hidden=true&xsec_source=app_share&type=normal&xsec_token=CBEAGjkQgcye5oD16jqmw4TxvRfSWy7kELPU_Mb5u8NQE%3D&author_share=1&xhsshare=&shareRedId=N0o7NUQ-Ok02NzUyOTgwNjY0OTc7RzxC&apptime=1780026941&share_id=78963b36c2a64e70aa4fa873ee69b106&share_channel=copy_link")
		}},
		{"xiaohongshu_gallery_short", func() (mediaMeta, error) {
			return parseXiaohongshu(cfg, "http://xhslink.com/o/Ea8wIsvXFd")
		}},
		{"bilibili_opus", func() (mediaMeta, error) {
			return parseBilibili(cfg, "https://m.bilibili.com/opus/1207735356902866977")
		}},
		{"weibo_single_image", func() (mediaMeta, error) {
			return parseWeibo(cfg, "https://weibo.com/1642632024/QF2Gpe3Ww")
		}},
		{"xianyu_mtb_short", func() (mediaMeta, error) {
			return parseXianyu(cfg, "https://m.tb.cn/h.RT9Lh91?tk=i31S5yHWj6i")
		}},
		{"acfun_video", func() (mediaMeta, error) {
			return parseAcfun(cfg, "https://www.acfun.cn/v/ac11348130")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := tc.fn()
			if err != nil {
				t.Fatal(err)
			}
			if len(meta.VideoURLs)+len(meta.ImageURLs) == 0 {
				t.Fatal("no media")
			}
			t.Log(fmt.Sprintf("%s title=%q author=%q videos=%d images=%d cover=%q desc=%q", meta.Platform, meta.Title, meta.Author, len(meta.VideoURLs), len(meta.ImageURLs), meta.Cover, truncate(meta.Desc, 80)))
		})
	}
}

func TestPermissionOKUsesGroupListsOnlyInGroups(t *testing.T) {
	cfg := config{
		PrivateAccessMode: accessBlacklist,
		GroupAccessMode:   accessWhitelist,
		UserWhitelist:     map[int64]bool{},
		GroupWhitelist:    map[int64]bool{123456: true},
		UserBlacklist:     map[int64]bool{10000: true},
		GroupBlacklist:    map[int64]bool{},
	}

	if ok, reason := permissionOK(cfg, 10000, 123456); !ok {
		t.Fatalf("group whitelisted group should pass even if user is private-blacklisted, reason=%s", reason)
	}
	if ok, reason := permissionOK(cfg, 10000, 0); ok {
		t.Fatalf("private blacklisted user should not pass, reason=%s", reason)
	}
}

func TestExtractLinksUnescapesHTMLAmpersands(t *testing.T) {
	cfg := defaultConfig()
	raw := `https://api.xiaoheihe.cn/v3/bbs/app/api/web/share?h_camp=link&amp;h_session_id=gI0l4ZUMQmztkMEA&amp;h_src=YXBwX3NoYXJl&amp;link_id=975771f1d3a9`
	links := extractLinks(raw, cfg)
	if len(links) != 1 {
		t.Fatalf("links len=%d", len(links))
	}
	if links[0].Platform != "xiaoheihe" {
		t.Fatalf("platform=%s", links[0].Platform)
	}
	if !strings.Contains(links[0].URL, "link_id=975771f1d3a9") {
		t.Fatalf("link_id was lost: %s", links[0].URL)
	}
}

func TestExtractLinksRecognizesXianyuMtbShortLinks(t *testing.T) {
	cfg := defaultConfig()
	raw := `【闲鱼】https://m.tb.cn/h.RT9Lh91?tk=i31S5yHWj6i tG-#22&gt;lD 「快来捡漏」`
	links := extractLinks(raw, cfg)
	if len(links) != 1 {
		t.Fatalf("links len=%d", len(links))
	}
	if links[0].Platform != "xianyu" {
		t.Fatalf("platform=%s", links[0].Platform)
	}
}

func TestKeylolThreadID(t *testing.T) {
	cases := map[string]string{
		"https://keylol.com/t1039281-1-1":                       "1039281",
		"https://keylol.com/thread-1039281-1-1.html":            "1039281",
		"https://keylol.com/forum.php?mod=viewthread&tid=12345": "12345",
	}
	for raw, want := range cases {
		if got := keylolThreadID(raw); got != want {
			t.Fatalf("keylolThreadID(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestKeylolTimeUnescapesHTMLSpace(t *testing.T) {
	if got := keylolTime("2&nbsp;小时前"); got != "2 小时前" {
		t.Fatalf("keylolTime html space=%q", got)
	}
}

func TestKeylolFooterTemplate(t *testing.T) {
	stateMu.Lock()
	currentConf = defaultConfig()
	currentConf.KeylolFooter = "来源 {title} / {author} / {time}"
	stateMu.Unlock()
	got := keylolFooterLine(mediaMeta{Title: "标题", Author: "作者"})
	if !strings.Contains(got, "来源 标题 / 作者 / ") || strings.Contains(got, "{time}") {
		t.Fatalf("footer=%q", got)
	}
}

func TestKeylolBuildBlocksKeepsForumWidgets(t *testing.T) {
	html := `<div class="sff_collapse sff_collapsed"><div class="sff_collapse_b"><span>&gt;</span> 大型限时福利的具体定义</div><div class="sff_collapse_d">隐藏内容<div><a>点击隐藏</a></div></div></div>
<h1 class="KyloStylisedHeader0">战网</h1>
<img width="72" height="39" src="https://blob.keylol.com/forum/202211/29/logo.png?a=a"><h3 class="KyloStylisedHeader2">《暗黑破坏神Ⅳ》国服基础版</h3>
正文`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	kinds := make([]string, 0, len(blocks))
	for _, block := range blocks {
		kinds = append(kinds, block.Kind)
	}
	got := strings.Join(kinds, ",")
	for _, want := range []string{"collapse", "heading1", "inline_image", "heading2", "text"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s: %#v", want, got, blocks)
		}
	}
	if strings.Contains(keylolDescFromBlocks(blocks), "隐藏内容") {
		t.Fatalf("collapsed hidden content leaked: %#v", blocks)
	}
}

func TestParseKeylolFirstPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("module") != "viewthread" || r.URL.Query().Get("tid") != "1039281" {
			t.Fatalf("bad query: %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Cookie"); got != "unit_test_cookie=1" {
			t.Fatalf("cookie header=%q", got)
		}
		_, _ = w.Write([]byte(`{
			"Variables":{
				"thread":{"subject":"华硕出了个单独的主板控制软件","author":"楼主","authorid":"1706647","dateline":"1780141700"},
				"postlist":[{
					"author":"m4262253402",
					"authorid":"1706647",
					"dateline":"1780141700",
					"message":"找驱动有没有更新<br><img src=\"https:\/\/keylol.com\/static\/image\/common\/fav.gif\"><img zoomfile=\"https:\/\/blob.keylol.com\/forum\/202605\/30\/194721x.png?a=a\"><p>好像是单独的主板控制软件。<\/p>",
					"attachments":{"1":{"url":"https:\/\/blob.keylol.com\/forum\/202605\/30\/194739i.png?a=a"}}
				}]
			}
		}`))
	}))
	defer srv.Close()
	oldAPI := keylolAPIBase
	keylolAPIBase = srv.URL
	defer func() { keylolAPIBase = oldAPI }()

	cfg := defaultConfig()
	cfg.KeylolCookie = "unit_test_cookie=1"
	meta, err := parseKeylol(cfg, "https://keylol.com/t1039281-1-1")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Platform != "keylol" || meta.Title != "华硕出了个单独的主板控制软件" || meta.Author != "m4262253402" {
		t.Fatalf("bad meta: %#v", meta)
	}
	if !strings.Contains(meta.Desc, "找驱动") || !strings.Contains(meta.Desc, "单独的主板控制软件") {
		t.Fatalf("bad desc: %q", meta.Desc)
	}
	if len(meta.ImageURLs) != 2 {
		t.Fatalf("images len=%d: %#v", len(meta.ImageURLs), meta.ImageURLs)
	}
	if strings.Contains(strings.Join([]string{meta.ImageURLs[0][0], meta.ImageURLs[1][0]}, "\n"), "fav.gif") {
		t.Fatalf("icon image was not filtered: %#v", meta.ImageURLs)
	}
	if !strings.Contains(meta.Avatar, "/001/70/66/47_avatar_middle.jpg") {
		t.Fatalf("avatar=%q", meta.Avatar)
	}
}

func TestExtractWeiboMediaUsesPicInfosAndSkipsAvatars(t *testing.T) {
	raw := `{
		"pic_ids":["abc123"],
		"pic_infos":{
			"abc123":{
				"large":{"url":"https://wx1.sinaimg.cn/large/abc123.jpg"},
				"thumbnail":{"url":"https://wx1.sinaimg.cn/thumbnail/abc123.jpg"}
			}
		},
		"user":{
			"profile_image_url":"https://tvax1.sinaimg.cn/crop.0.0.180.180.180/avatar.jpg",
			"avatar_hd":"https://tvax1.sinaimg.cn/crop.0.0.1024.1024.1024/avatar_hd.jpg"
		},
		"page_info":{
			"page_pic":{"url":"https://h5.sinaimg.cn/upload/2015/09/25/3/timeline_card_small_movie_default.png"}
		}
	}`
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}

	_, images := extractWeiboMedia(data)
	if len(images) != 1 {
		t.Fatalf("images len=%d, want 1: %#v", len(images), images)
	}
	if got := images[0][0]; got != "https://wx1.sinaimg.cn/large/abc123.jpg" {
		t.Fatalf("image url=%s", got)
	}
}

func TestParseAcfunVideoInfoHTML(t *testing.T) {
	html := `<script class="videoInfo">
window.pageInfo = window.videoInfo ={
  "title":"测试 A 站视频",
  "description":"简介文本",
  "createTimeMillis":1700000000000,
  "coverUrl":"https://imgs.aixifan.com/cover.jpg",
  "user":{"name":"AcFun UP","headUrl":"https://imgs.aixifan.com/avatar.jpg"},
		"currentVideoInfo":{
    "durationMillis":67000,
    "ksPlayJson":{"adaptationSet":[{"representation":[{"url":"https://video.acfun.cn/720.m3u8","qualityType":"720p"},{"url":"https://video.acfun.cn/360.m3u8","qualityType":"360p"}]}]}
  }
}</script>`

	meta, err := parseAcfunVideoInfoHTML(defaultConfig(), "https://www.acfun.cn/v/ac123", html)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Platform != "acfun" || meta.Title != "测试 A 站视频" || meta.Author != "AcFun UP" {
		t.Fatalf("bad meta: %#v", meta)
	}
	if meta.Cover != "https://imgs.aixifan.com/cover.jpg" || meta.Avatar != "https://imgs.aixifan.com/avatar.jpg" {
		t.Fatalf("bad images: cover=%q avatar=%q", meta.Cover, meta.Avatar)
	}
	if len(meta.VideoURLs) != 1 || meta.VideoURLs[0][0] != "m3u8:https://video.acfun.cn/720.m3u8" {
		t.Fatalf("bad video urls: %#v", meta.VideoURLs)
	}
}

func TestAppendYTDLPPlatformArgs(t *testing.T) {
	oldCacheDir := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = oldCacheDir }()

	cfg := defaultConfig()
	cfg.YTDLPCookieFile = "/tmp/legacy-all-cookies.txt"
	cfg.YouTubeCookie = "YT_TEST_COOKIE=value"
	cfg.InstagramCookie = "IG_TEST_COOKIE=value"

	ytArgs := appendYTDLPPlatformArgs([]string{"-J"}, cfg, "youtube")
	if !containsArgPair(ytArgs, "--extractor-args", "youtube:player_client=default,android;formats=missing_pot") {
		t.Fatalf("youtube extractor args missing: %#v", ytArgs)
	}
	if !containsArgWithPrefix(ytArgs, "--cookies", cacheDir) {
		t.Fatalf("youtube cookie file missing: %#v", ytArgs)
	}
	if containsArgPair(ytArgs, "--cookies", "/tmp/legacy-all-cookies.txt") {
		t.Fatalf("youtube should not use global tool cookie: %#v", ytArgs)
	}

	igArgs := appendYTDLPPlatformArgs([]string{"-J"}, cfg, "instagram")
	if !containsArgWithPrefix(igArgs, "--cookies", cacheDir) {
		t.Fatalf("instagram cookie file missing: %#v", igArgs)
	}
	if containsArgPair(igArgs, "--cookies", "/tmp/legacy-all-cookies.txt") {
		t.Fatalf("instagram should not use global tool cookie: %#v", igArgs)
	}
}

func TestBiliQualityFollowsGlobalResolution(t *testing.T) {
	cfg := defaultConfig()
	cfg.VideoMaxResolution = 720
	cfg.BilibiliMaxQuality = "4K"
	normalizeConfig(&cfg)
	if cfg.BilibiliMaxQuality != "720P" {
		t.Fatalf("bilibili quality=%q", cfg.BilibiliMaxQuality)
	}
}

func TestXiaohongshuHeadersIncludeCookie(t *testing.T) {
	cfg := defaultConfig()
	cfg.XiaohongshuCookie = "a=b; xsec=1"
	headers := xhsPageHeaders(cfg, "https://www.xiaohongshu.com/explore/abc")
	if headers["Cookie"] != cfg.XiaohongshuCookie {
		t.Fatalf("cookie header=%q", headers["Cookie"])
	}
}

func TestYTDLPAvatarExtractionHelpers(t *testing.T) {
	ytHTML := `{"avatar":{"thumbnails":[{"url":"https://yt3.ggpht.com/avatar=s88-c-k-c0x00ffffff-no-rj\u0026x=1"}]}}`
	if got := firstRegexGroup(ytHTML, `"avatar"\s*:\s*\{[^{}]*"thumbnails"\s*:\s*\[\s*\{[^{}]*"url"\s*:\s*"([^"]+)`); got != "https://yt3.ggpht.com/avatar=s88-c-k-c0x00ffffff-no-rj&x=1" {
		t.Fatalf("youtube avatar=%q", got)
	}
	igHTML := `{"profile_pic_url_hd":"https:\/\/instagram.fabc1-1.fna.fbcdn.net\/avatar.jpg?_nc_ht=x"}`
	if got := firstRegexGroup(igHTML, `"profile_pic_url_hd"\s*:\s*"([^"]+)`); got != "https://instagram.fabc1-1.fna.fbcdn.net/avatar.jpg?_nc_ht=x" {
		t.Fatalf("instagram avatar=%q", got)
	}
	if got := instagramUsernameFromYTDLP("Post by jaychou", "", "5951385086"); got != "jaychou" {
		t.Fatalf("instagram username=%q", got)
	}
}

func TestInstagramBuildMetaUsesOwnerAvatarAndCarousel(t *testing.T) {
	if got := instagramShortcode("https://www.instagram.com/p/DYyvSw5D1wi/?utm_source=ig_web_copy_link"); got != "DYyvSw5D1wi" {
		t.Fatalf("shortcode=%q", got)
	}
	if got := instagramShortcodePK("DYyvSw5D1wi"); got != "3905391824517159970" {
		t.Fatalf("pk=%q", got)
	}
	raw := `{
	  "taken_at": 1780000000,
	  "caption": {"text": "第一行标题\n#topic 正文"},
	  "user": {
	    "username": "jaychou",
	    "full_name": "Jay Chou 周杰倫",
	    "profile_pic_url": "https://scontent.cdninstagram.com/v/t51.2885-19/292049853_avatar.jpg"
	  },
	  "carousel_media": [
	    {"image_versions2": {"candidates": [
	      {"url": "https://img/small.jpg", "width": 320, "height": 320},
	      {"url": "https://img/large.jpg", "width": 1080, "height": 1080}
	    ]}},
	    {"video_versions": [
	      {"url": "https://video/low.mp4", "width": 360, "height": 640},
	      {"url": "https://video/high.mp4", "width": 1080, "height": 1920}
	    ], "image_versions2": {"candidates": [
	      {"url": "https://img/cover.jpg", "width": 720, "height": 1280}
	    ]}}
	  ]
	}`
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	meta := buildInstagramMeta("https://www.instagram.com/p/DYyvSw5D1wi/", "DYyvSw5D1wi", item)
	if meta.Author != "Jay Chou 周杰倫" || !strings.Contains(meta.Avatar, "292049853") {
		t.Fatalf("bad owner fields author=%q avatar=%q", meta.Author, meta.Avatar)
	}
	if len(meta.ImageURLs) != 2 || meta.ImageURLs[0][0] != "https://img/large.jpg" || meta.ImageURLs[1][0] != "https://img/cover.jpg" {
		t.Fatalf("bad images=%#v", meta.ImageURLs)
	}
	if len(meta.VideoURLs) != 1 || meta.VideoURLs[0][0] != "https://video/high.mp4" {
		t.Fatalf("bad videos=%#v", meta.VideoURLs)
	}
	if len(meta.MediaItems) != 2 || meta.MediaItems[0].Kind != "image" || meta.MediaItems[1].Kind != "video" {
		t.Fatalf("bad media item order=%#v", meta.MediaItems)
	}
}

func TestInstagramSingleVideoUsesCoverOnlyForCard(t *testing.T) {
	raw := `{
	  "caption": {"text": "video caption"},
	  "user": {"username": "demo", "profile_pic_url": "https://img/avatar.jpg"},
	  "video_versions": [
	    {"url": "https://video/high.mp4", "width": 1080, "height": 1920}
	  ],
	  "image_versions2": {"candidates": [
	    {"url": "https://img/cover.jpg", "width": 720, "height": 1280}
	  ]}
	}`
	var item map[string]any
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	meta := buildInstagramMeta("https://www.instagram.com/reel/DYumcWHt8Rc/", "DYumcWHt8Rc", item)
	if len(meta.VideoURLs) != 1 || len(meta.ImageURLs) != 0 || meta.Cover != "https://img/cover.jpg" {
		t.Fatalf("single video should not send cover separately: videos=%#v images=%#v cover=%q", meta.VideoURLs, meta.ImageURLs, meta.Cover)
	}
	if !shouldForwardCombinedMedia(&meta) {
		t.Fatal("instagram single video should use forward message with caption")
	}
}

func TestCardWrapKeepsEnglishWords(t *testing.T) {
	lines := wrapCardText("Representing Holland at the upcoming football world tournament? @virgilvandijk is taking it all in ⚽", 34)
	for _, line := range lines {
		for _, broken := range []string{"upco", "vir", "gil"} {
			if line == broken {
				t.Fatalf("unexpected broken word line: %#v", lines)
			}
		}
	}
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "upcoming football") || !strings.Contains(joined, "@virgilvandijk") {
		t.Fatalf("bad wrapped text: %#v", lines)
	}
}

func TestCombinedMediaPlatformsNeedMixedItems(t *testing.T) {
	for _, platform := range []string{"instagram", "twitter", "weibo"} {
		meta := mediaMeta{
			Platform:   platform,
			VideoURLs:  [][]string{{"video.mp4"}},
			ImageURLs:  [][]string{{"image.jpg"}, {"video-cover.jpg"}},
			MediaItems: []mediaItem{{Kind: "video", Index: 0}, {Kind: "image", Index: 0}},
		}
		if !shouldForwardCombinedMedia(&meta) || !shouldRenderAsGalleryCard(meta) || shouldDrawPlayOverlay(meta) {
			t.Fatalf("%s mixed media rules not applied", platform)
		}
	}
}

func TestLongImageCardMode(t *testing.T) {
	meta := mediaMeta{
		Platform:  "weibo",
		Desc:      "这是一段公告正文，后面只有一张长图。",
		ImageURLs: [][]string{{"https://example.com/notice.jpg"}},
	}
	if !shouldRenderLongImageCard(meta, testGradientImage(900, 1400, color.RGBA{R: 1, G: 2, B: 3, A: 255}, color.RGBA{R: 4, G: 5, B: 6, A: 255})) {
		t.Fatal("single image with text should use long image card")
	}
	img := testGradientImage(900, 1800, color.RGBA{R: 1, G: 2, B: 3, A: 255}, color.RGBA{R: 4, G: 5, B: 6, A: 255})
	if h := longImageCardHeight(img, 600); h != 1200 {
		t.Fatalf("unexpected long image height=%d", h)
	}
	if shouldRenderLongImageCard(meta, testGradientImage(900, 2600, color.RGBA{R: 1, G: 2, B: 3, A: 255}, color.RGBA{R: 4, G: 5, B: 6, A: 255})) {
		t.Fatal("extreme tall image should fall back to gallery card")
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func containsArgWithPrefix(args []string, key, prefix string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && strings.HasPrefix(args[i+1], prefix) {
			return true
		}
	}
	return false
}

func TestRenderInfoCardPreview(t *testing.T) {
	if os.Getenv("MEDIAPARSER_CARD_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_CARD_PREVIEW=1 to render preview")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var img image.Image
		switch r.URL.Path {
		case "/avatar.png":
			img = testGradientImage(180, 180, color.RGBA{R: 90, G: 205, B: 255, A: 255}, color.RGBA{R: 255, G: 205, B: 45, A: 255})
		default:
			img = testGradientImage(1280, 720, color.RGBA{R: 78, G: 65, B: 52, A: 255}, color.RGBA{R: 224, G: 178, B: 118, A: 255})
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	}))
	defer server.Close()

	oldCacheDir := cacheDir
	cacheDir = filepath.Join("..", "..", "build", "mediaparser-card-preview")
	defer func() { cacheDir = oldCacheDir }()

	out, err := renderInfoCard(mediaMeta{
		URL:       "https://www.bilibili.com/video/BV1fho5B2Ec7/",
		SourceURL: "preview",
		Platform:  "bilibili",
		Title:     "男生对小说的容忍度",
		Author:    "尘世小小鱼",
		Avatar:    server.URL + "/avatar.png",
		Timestamp: "2026-04-24 16:04:08",
		Desc:      "该视频暂不支持AI总结",
		Cover:     server.URL + "/cover.png",
		VideoURLs: [][]string{{server.URL + "/video.mp4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)

	gallery, err := renderInfoCard(mediaMeta{
		URL:       "https://www.xiaohongshu.com/explore/example",
		SourceURL: "gallery-preview",
		Platform:  "xiaohongshu",
		Title:     "在家对镜怎么拍",
		Author:    "苹果醋",
		Avatar:    server.URL + "/avatar.png",
		Timestamp: "05-09",
		Desc:      "#妈妈[话题]# #老婆[话题]# #对镜拍[话题]# #富士相机[话题]# #宅家拍照[话题]#",
		ImageURLs: [][]string{
			{server.URL + "/cover.png"},
			{server.URL + "/cover.png"},
			{server.URL + "/cover.png"},
			{server.URL + "/cover.png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(gallery)

	twitterGallery, err := renderInfoCard(mediaMeta{
		URL:       "https://x.com/example/status/1",
		SourceURL: "twitter-gallery-preview",
		Platform:  "twitter",
		Title:     "トーマ@裏垢(@Tomaaibjjo02) 的推文",
		Author:    "トーマ@裏垢",
		Avatar:    server.URL + "/avatar.png",
		Timestamp: "2026-05-28",
		Desc:      "信じられないレベルで美人な家政婦がきた結果、部屋が綺麗になる前に俺の理性が汚れた🤣🤣",
		ImageURLs: [][]string{
			{server.URL + "/cover.png"},
			{server.URL + "/cover.png"},
			{server.URL + "/cover.png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(twitterGallery)
}

func TestRenderLiveUserCardPreview(t *testing.T) {
	if os.Getenv("MEDIAPARSER_LIVE_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_LIVE_PREVIEW=1 to render live platform previews")
	}

	oldCacheDir := cacheDir
	cacheDir = filepath.Join("..", "..", "build", "mediaparser-live-preview")
	defer func() { cacheDir = oldCacheDir }()

	cfg := defaultConfig()
	cases := []struct {
		raw   string
		parse func(config, string) (mediaMeta, error)
	}{
		{"https://www.xiaohongshu.com/discovery/item/69fe08f800000000350242fc?app_platform=android&ignoreEngage=true&app_version=9.21.0&share_from_user_hidden=true&xsec_source=app_share&type=normal&xsec_token=CBEAGjkQgcye5oD16jqmw4TxvRfSWy7kELPU_Mb5u8NQE%3D&author_share=1&xhsshare=&shareRedId=N0o7NUQ-Ok02NzUyOTgwNjY0OTc7RzxC&apptime=1780026941&share_id=78963b36c2a64e70aa4fa873ee69b106&share_channel=copy_link", parseXiaohongshu},
		{"http://xhslink.com/o/2vNtkh31l2q", parseXiaohongshu},
		{"http://xhslink.com/o/40QY1k0q6MF", parseXiaohongshu},
		{"http://xhslink.com/o/Ea8wIsvXFd", parseXiaohongshu},
		{"https://m.bilibili.com/opus/1207735356902866977", parseBilibili},
	}
	for _, tc := range cases {
		meta, err := tc.parse(cfg, tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := renderInfoCard(meta)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("author=%q images=%d card=%s", meta.Author, len(meta.ImageURLs), out)
	}
}

func testGradientImage(w, h int, a, b color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x+y) / float64(w+h)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(float64(a.R)*(1-t) + float64(b.R)*t),
				G: uint8(float64(a.G)*(1-t) + float64(b.G)*t),
				B: uint8(float64(a.B)*(1-t) + float64(b.B)*t),
				A: 255,
			})
		}
	}
	return img
}
