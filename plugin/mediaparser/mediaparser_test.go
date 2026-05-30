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
	cfg := defaultConfig()
	cfg.YTDLPCookieFile = "/tmp/all-cookies.txt"
	cfg.InstagramCookieFile = "/tmp/ig-cookies.txt"

	ytArgs := appendYTDLPPlatformArgs([]string{"-J"}, cfg, "youtube")
	if !containsArgPair(ytArgs, "--extractor-args", "youtube:player_client=default,android;formats=missing_pot") {
		t.Fatalf("youtube extractor args missing: %#v", ytArgs)
	}
	if !containsArgPair(ytArgs, "--cookies", "/tmp/all-cookies.txt") {
		t.Fatalf("global cookie missing: %#v", ytArgs)
	}

	igArgs := appendYTDLPPlatformArgs([]string{"-J"}, cfg, "instagram")
	if !containsArgPair(igArgs, "--cookies", "/tmp/ig-cookies.txt") {
		t.Fatalf("instagram cookie missing: %#v", igArgs)
	}
	if containsArgPair(igArgs, "--cookies", "/tmp/all-cookies.txt") {
		t.Fatalf("instagram should prefer platform cookie: %#v", igArgs)
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
