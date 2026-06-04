package mediaparser

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FloatTech/gg"
	zip "github.com/alexmullins/zip"
	"github.com/disintegration/imaging"
	zero "github.com/wdvxdr1123/ZeroBot"
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

func TestQQBotMediaLinePartsOnlyAllowsCacheMedia(t *testing.T) {
	oldCacheDir := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = oldCacheDir }()

	allowed := filepath.Join(cacheDir, "card.png")
	if err := os.WriteFile(allowed, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	blockedDir, err := os.MkdirTemp(".", ".qqbot-media-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(blockedDir) })
	blocked, err := filepath.Abs(filepath.Join(blockedDir, "secret.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}

	driver := &qqBotDriver{}
	text, items := driver.mediaLineParts("前置\nMEDIA:" + allowed + "\n后置")
	if len(items) != 1 {
		t.Fatalf("expected one media item, got %d", len(items))
	}
	if items[0].target != allowed || items[0].fileType != qqBotMediaTypeImage {
		t.Fatalf("unexpected media item: %#v", items[0])
	}
	if strings.Contains(text, "MEDIA:") || !strings.Contains(text, "前置") || !strings.Contains(text, "后置") {
		t.Fatalf("unexpected remaining text: %q", text)
	}

	text, items = driver.mediaLineParts("MEDIA:" + blocked)
	if len(items) != 0 {
		t.Fatalf("cache-external media should be rejected: %#v", items)
	}
	if !strings.Contains(text, "MEDIA:") {
		t.Fatalf("rejected media line should stay visible, got %q", text)
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

func TestExtractLinksRecognizesLinuxdoTopics(t *testing.T) {
	cfg := defaultConfig()
	raw := `https://linux.do/t/topic-title/12345/2`
	links := extractLinks(raw, cfg)
	if len(links) != 1 {
		t.Fatalf("links len=%d", len(links))
	}
	if links[0].Platform != "linuxdo" {
		t.Fatalf("platform=%s", links[0].Platform)
	}
}

func TestLinuxdoTopicID(t *testing.T) {
	cases := map[string]string{
		"https://linux.do/t/topic-title/12345":   "12345",
		"https://linux.do/t/topic-title/12345/2": "12345",
		"https://linux.do/t/12345/2":             "12345",
		"https://linux.do/t/12345.json":          "12345",
	}
	for raw, want := range cases {
		if got := linuxdoTopicID(raw); got != want {
			t.Fatalf("linuxdoTopicID(%q)=%q, want %q", raw, got, want)
		}
	}
}

func TestLinuxdoPostNumber(t *testing.T) {
	cases := map[string]string{
		"https://linux.do/t/topic-title/12345":   "",
		"https://linux.do/t/topic-title/12345/3": "3",
		"https://linux.do/t/12345/3":             "3",
	}
	for raw, want := range cases {
		if got := linuxdoPostNumber(raw); got != want {
			t.Fatalf("linuxdoPostNumber(%q)=%q, want %q", raw, got, want)
		}
	}
}

func TestParseLinuxdoTopicJSON(t *testing.T) {
	body := `{
	  "id": 12345,
	  "slug": "topic-title",
	  "title": "Linux.do 分享图片功能",
	  "post_stream": {
	    "posts": [{
	      "post_number": 1,
	      "username": "neo",
	      "name": "Neo",
	      "avatar_template": "/user_avatar/linux.do/neo/{size}/1_2.png",
	      "created_at": "2026-06-03T12:34:56.000Z",
	      "cooked": "<p>这是主帖内容。</p><p><img src=\"/uploads/default/original/1X/test.png\"></p>"
	    }]
	  }
	}`
	meta, err := parseLinuxdoTopicJSON("https://linux.do/t/topic-title/12345", "https://linux.do/t/12345.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Platform != "linuxdo" || meta.Title != "Linux.do 分享图片功能" || meta.Author != "Neo" {
		t.Fatalf("meta=%+v", meta)
	}
	if !strings.Contains(meta.Desc, "这是主帖内容") {
		t.Fatalf("desc=%q", meta.Desc)
	}
	if meta.URL != "https://linux.do/t/topic-title/12345/1" {
		t.Fatalf("url=%q", meta.URL)
	}
	if len(meta.ImageURLs) != 1 || meta.ImageURLs[0][0] != "https://linux.do/uploads/default/original/1X/test.png" {
		t.Fatalf("images=%v", meta.ImageURLs)
	}
	if meta.Avatar != "https://linux.do/user_avatar/linux.do/neo/120/1_2.png" {
		t.Fatalf("avatar=%q", meta.Avatar)
	}
}

func TestLinuxdoExtractDiscoursePreloadedTopic(t *testing.T) {
	html := `<html><head><title>Linux.do</title></head><body>
<script type="application/json" data-discourse-preloaded="topic_12345">{
  "id":12345,
  "slug":"topic-title",
  "title":"Linux.do Preloaded",
  "post_stream":{"posts":[{"post_number":1,"username":"neo","cooked":"<p>full body from preloaded</p>"}]}
}</script>
</body></html>`
	topic := linuxdoExtractTopicJSONFromHTML(html)
	if topic == nil {
		t.Fatal("topic not extracted")
	}
	meta, err := parseLinuxdoTopicJSON("https://linux.do/t/topic-title/12345", "https://linux.do/t/topic-title/12345", mustJSON(topic))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Linux.do Preloaded" || !strings.Contains(meta.Desc, "full body from preloaded") {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestParseLinuxdoTopicJSONSelectsRequestedPost(t *testing.T) {
	body := `{
	  "id": 12345,
	  "slug": "topic-title",
	  "title": "Linux.do 楼层链接",
	  "post_stream": {
	    "stream": [101,102,103],
	    "posts": [{
	      "id": 101,
	      "post_number": 1,
	      "username": "neo",
	      "cooked": "<p>主帖内容</p>"
	    },{
	      "id": 103,
	      "post_number": 3,
	      "username": "ada",
	      "name": "Ada",
	      "cooked": "<p>第三楼内容</p>"
	    }]
	  }
	}`
	meta, err := parseLinuxdoTopicJSON("https://linux.do/t/topic-title/12345/3", "https://linux.do/t/12345.json", body)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Author != "Ada" || !strings.Contains(meta.Desc, "第三楼内容") {
		t.Fatalf("meta=%+v", meta)
	}
	if meta.URL != "https://linux.do/t/topic-title/12345/3" {
		t.Fatalf("url=%q", meta.URL)
	}
}

func TestLinuxdoMergePostIntoTopic(t *testing.T) {
	topic := `{
	  "id": 12345,
	  "slug": "topic-title",
	  "title": "Linux.do 楼层链接",
	  "post_stream": {
	    "stream": [101,102,103],
	    "posts": [{
	      "id": 101,
	      "post_number": 1,
	      "username": "neo",
	      "cooked": "<p>主帖内容</p>"
	    }]
	  }
	}`
	postID, loaded := linuxdoPostIDForNumber(topic, "3")
	if loaded || postID != "103" {
		t.Fatalf("postID=%q loaded=%v", postID, loaded)
	}
	merged, err := linuxdoMergePostIntoTopic(topic, `{
	  "id": 103,
	  "post_number": 3,
	  "username": "ada",
	  "name": "Ada",
	  "cooked": "<p>第三楼补充内容</p>"
	}`)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := parseLinuxdoTopicJSON("https://linux.do/t/topic-title/12345/3", "https://linux.do/posts/103.json", merged)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Author != "Ada" || !strings.Contains(meta.Desc, "第三楼补充内容") {
		t.Fatalf("meta=%+v", meta)
	}
}

func TestLinuxdoCleanCookedStripsPromotionDeclaration(t *testing.T) {
	cooked := `<p>本帖使用社区开源推广，符合推广要求。我申明并遵循社区要求的以下内容：</p>
<p>我的帖子已经打上 开源推广 标签： 是</p>
<p>我的开源项目完整开源，无未开源部分： 是</p>
<p>我的开源项目已链接认可 LINUX DO 社区： 是</p>
<p>我帖子内的项目介绍，AI生成、润色内容部分已截图发出： 是</p>
<p>以上选择我承诺是永久有效的，接受社区和佬友监督： 是</p>
<p>以下为项目介绍正文内容，AI生成、润色内容已使用截图方式发出</p>
<p>这里是真正的项目介绍。</p>`
	got := linuxdoCleanCooked(cooked)
	if strings.Contains(got, "开源推广") || strings.Contains(got, "社区要求") || strings.Contains(got, "以上选择") {
		t.Fatalf("promotion declaration was not stripped: %q", got)
	}
	if !strings.Contains(got, "这里是真正的项目介绍") {
		t.Fatalf("project body missing: %q", got)
	}
}

func TestLinuxdoBodyLinesKeepsLongBody(t *testing.T) {
	body := strings.Repeat("Linux.do full body line with enough words to wrap.\n", 40)
	lines := linuxdoBodyLines(nil, body, 620)
	if len(lines) <= 22 {
		t.Fatalf("linuxdo body should not be capped at 22 lines, got %d", len(lines))
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Linux.do full body line") {
		t.Fatalf("body content missing: %q", joined)
	}
}

func TestLinuxdoHeadersIncludeCookieAndUA(t *testing.T) {
	cfg := defaultConfig()
	cfg.LinuxdoCookie = "_t=token; cf_clearance=clearance"
	cfg.LinuxdoUA = "UnitTest/Linuxdo"

	headers := linuxdoHeaders(cfg, "https://linux.do/t/topic/123")
	if headers["Cookie"] != cfg.LinuxdoCookie {
		t.Fatalf("cookie header=%q", headers["Cookie"])
	}
	if headers["User-Agent"] != cfg.LinuxdoUA {
		t.Fatalf("ua header=%q", headers["User-Agent"])
	}
	if headers["Origin"] != linuxdoBase {
		t.Fatalf("origin=%q", headers["Origin"])
	}
}

func TestCookieCloudHeaderForDomains(t *testing.T) {
	cookies := []cookieCloudCookie{
		{Domain: ".linux.do", Name: "_t", Value: "token"},
		{Domain: "www.linux.do", Name: "cf_clearance", Value: "clear"},
		{Domain: ".example.com", Name: "skip", Value: "nope"},
	}
	got := cookieCloudHeaderForDomains(cookies, []string{"linux.do"})
	if got != "_t=token; cf_clearance=clear" {
		t.Fatalf("header=%q", got)
	}
	if !cookieCloudDomainMatch("static.youtube.com", []string{"youtube.com"}) {
		t.Fatal("expected subdomain match")
	}
}

func TestApplyCookieCloudCookiesUpdatesSelectedPlatforms(t *testing.T) {
	oldConfigPath := configPath
	oldConf := snapshotConfig()
	configPath = filepath.Join(t.TempDir(), "config.json")
	currentConf = defaultConfig()
	defer func() {
		configPath = oldConfigPath
		currentConf = oldConf
	}()

	result := applyCookieCloudCookies([]cookieCloudPlatformSpec{
		{Name: "linuxdo", Domains: []string{"linux.do"}},
		{Name: "instagram", Domains: []string{"instagram.com"}},
	}, []cookieCloudCookie{
		{Domain: ".linux.do", Name: "_t", Value: "token"},
		{Domain: ".instagram.com", Name: "sessionid", Value: "ig"},
	})
	if currentConf.LinuxdoCookie != "_t=token" || currentConf.InstagramCookie != "sessionid=ig" {
		t.Fatalf("cookies not applied: linuxdo=%q instagram=%q", currentConf.LinuxdoCookie, currentConf.InstagramCookie)
	}
	if !containsString(result.Warnings, "linuxdo 缺少 cf_clearance") {
		t.Fatalf("warnings=%v", result.Warnings)
	}
}

func TestLinuxdoErrorSummaryDetectsCloudflare(t *testing.T) {
	body := `<html><head><title>Just a moment...</title></head><body><script src="/cdn-cgi/challenge-platform/h/b"></script></body></html>`

	got := linuxdoErrorSummary(body)
	if !strings.Contains(got, "title=\"Just a moment...\"") || !strings.Contains(got, "cloudflare_challenge") {
		t.Fatalf("summary=%q", got)
	}
	if !strings.Contains(got, "body_len=") || !strings.Contains(got, "snippet=") {
		t.Fatalf("summary missing diagnostics: %q", got)
	}
}

func TestRenderLinuxdoShareCard(t *testing.T) {
	oldCacheDir := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = oldCacheDir }()

	img := image.NewRGBA(image.Rect(0, 0, 360, 180))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 75, G: 122, B: 255, A: 255}}, image.Point{}, draw.Src)
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if err := png.Encode(w, img); err != nil {
			t.Errorf("encode image: %v", err)
		}
	}))
	defer imgSrv.Close()

	meta := mediaMeta{
		URL:       "https://linux.do/t/topic-title/12345/1",
		SourceURL: "https://linux.do/t/topic-title/12345",
		Platform:  "linuxdo",
		Title:     "Linux.do 分享图片功能",
		Author:    "Neo",
		Timestamp: "2026-06-03 12:34:56",
		Desc:      "这是主帖内容。\n第二行摘要。",
		ImageURLs: [][]string{{imgSrv.URL + "/post.png"}},
	}
	out, err := renderInfoCard(meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("card not saved: %v", err)
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

func TestKeylolBuildBlocksCleansBBCollapse(t *testing.T) {
	blocks := keylolBuildBlocks(`[collapse=大型限时福利的具体定义]隐藏内容[/collapse]<br>正文`, nil, "https://keylol.com/t1-1-1")
	if len(blocks) < 2 || blocks[0].Kind != "collapse" || blocks[0].Text != "大型限时福利的具体定义" {
		t.Fatalf("collapse block not parsed: %#v", blocks)
	}
	got := keylolDescFromBlocks(blocks)
	if strings.Contains(got, "collapse") || !strings.Contains(got, "正文") {
		t.Fatalf("collapse tag leaked into desc=%q blocks=%#v", got, blocks)
	}
}

func TestKeylolRequiresReplyVisible(t *testing.T) {
	html := `<div class="showhide"><p>隐藏内容，<a href="javascript:;" class="showhide-btn">点击显示</a></p></div>`
	if !keylolRequiresReplyVisible(html) {
		t.Fatalf("reply-visible showhide should be detected")
	}
	if keylolRequiresReplyVisible(`<div class="sff_collapse"><p>隐藏内容</p></div>`) {
		t.Fatalf("plain collapse should not be treated as reply-visible")
	}
}

func TestKeylolBuildBlocksKeepsShowhideImagesWithoutControlText(t *testing.T) {
	html := `<h2 class="KyloStylisedHeader1">游戏截图</h2>
<div class="showhide"><p>隐藏内容，<a href="javascript:;" class="showhide-btn">点击显示</a></p><div style="display:none" class="spoiler">
<img class="zoom" file="https://shared.cdn.queniuqe.com/store_item_assets/steam/apps/4435490/shot1.jpg?t=1"><br>
<img class="zoom" file="https://shared.cdn.queniuqe.com/store_item_assets/steam/apps/4435490/shot2.jpg?t=1"><div><a href="javascript:;">点击隐藏</a></div>
</div></div>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039073-1-1")
	imageCount := 0
	for _, block := range blocks {
		if block.Kind == "image" {
			imageCount++
		}
		if block.Kind == "text" && (strings.Contains(block.Text, "点击显示") || strings.Contains(block.Text, "点击隐藏")) {
			t.Fatalf("showhide control text leaked: %#v", blocks)
		}
	}
	if imageCount != 2 {
		t.Fatalf("hidden screenshot count=%d blocks=%#v", imageCount, blocks)
	}
}

func TestKeylolBuildBlocksKeepsBilibiliIframePreview(t *testing.T) {
	html := `<h2 class="KyloStylisedHeader1">宣传视频</h2><iframe class="html5video" src="https://keylol.com/source/plugin/onexin_html5player/open/bilibili/html5player.html?bvid=BV16AGQ6LEQJ&page="></iframe><p>正文</p>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039073-1-1")
	found := false
	for _, block := range blocks {
		if block.Kind == "video_embed" {
			found = true
			if block.URL != "https://www.bilibili.com/video/BV16AGQ6LEQJ" || !strings.Contains(block.Title, "BV16AGQ6LEQJ") {
				t.Fatalf("bad video block: %#v", block)
			}
		}
	}
	if !found {
		t.Fatalf("missing video block: %#v", blocks)
	}
}

func TestKeylolBuildBlocksKeepsBilibiliMediaTagPreview(t *testing.T) {
	html := `宣传视频<br>[media]https://www.bilibili.com/video/BV16AGQ6LEQJ/[/media]<br>游戏截图`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039073-1-1")
	for _, block := range blocks {
		if block.Kind == "text" && strings.Contains(block.Text, "[media]") {
			t.Fatalf("media tag leaked as text: %#v", blocks)
		}
		if block.Kind == "video_embed" && block.URL == "https://www.bilibili.com/video/BV16AGQ6LEQJ" {
			return
		}
	}
	t.Fatalf("missing media tag video block: %#v", blocks)
}

func TestKeylolBuildBlocksLabelsShownHiddenImages(t *testing.T) {
	html := `<h2>游戏截图</h2><div class="showhide"><p>隐藏内容，<a href="javascript:;" class="showhide-btn">点击显示</a></p><div class="spoiler"><img file="https://img.example.com/a.jpg"></div></div>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039073-1-1")
	foundLabel := false
	foundImage := false
	for _, block := range blocks {
		if block.Kind == "hidden_label" && block.Text == "已显示隐藏内容" {
			foundLabel = true
		}
		if block.Kind == "image" && block.URL == "https://img.example.com/a.jpg" {
			foundImage = true
		}
	}
	if !foundLabel || !foundImage {
		t.Fatalf("hidden label/image missing: %#v", blocks)
	}
}

func TestKeylolBuildBlocksKeepsSmiliesInline(t *testing.T) {
	html := `蓝色窃听<img class="zoom" file="https://keylol.com/static/image/smiley/steamcn_9/0140.gif" width="61" height="55">不过后续不错`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039233-1-1")
	if len(blocks) != 1 || blocks[0].Kind != "text" {
		t.Fatalf("smiley should stay inline in text: %#v", blocks)
	}
	if strings.Contains(blocks[0].Text, "static/image/smiley") || !strings.Contains(blocks[0].Text, "😎") {
		t.Fatalf("bad smiley text: %#v", blocks)
	}
}

func TestKeylolBuildBlocksKeepsSpoilerStyled(t *testing.T) {
	html := `美妙邦女郎<span class="bbcode_spoiler"><span class="bbcode_spoiler_content">还有会说日语的坏邦女郎</span></span><br>[spoil]不要显示标签[/spoil]<br>正文`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039233-1-1")
	found := false
	for _, block := range blocks {
		if block.Kind == "spoiler" {
			found = true
			if block.Text != "还有会说日语的坏邦女郎" {
				t.Fatalf("bad spoiler text: %#v", blocks)
			}
		}
		if block.Kind == "text" && strings.Contains(block.Text, "还有会说日语") {
			t.Fatalf("spoiler leaked as plain text: %#v", blocks)
		}
		if strings.Contains(block.Text, "[spoil]") || strings.Contains(block.Text, "[/spoil]") {
			t.Fatalf("spoil tag leaked: %#v", blocks)
		}
	}
	if !found {
		t.Fatalf("missing spoiler block: %#v", blocks)
	}
}

func TestKeylolBuildBlocksHandlesNamedSpoilAndSteamMediaLinks(t *testing.T) {
	html := `[spoil=隐藏内容]https://video.akamai.steamstatic.com/store_trailers/1875580/3231/hls_264_master.m3u8?t=1778085[/spoil]`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1033463-1-1")
	if len(blocks) < 2 || blocks[0].Kind != "hidden_label" || blocks[1].Kind != "video_embed" {
		t.Fatalf("bad blocks: %#v", blocks)
	}
	for _, block := range blocks {
		if strings.Contains(block.Text, "spoil") {
			t.Fatalf("spoil tag leaked: %#v", blocks)
		}
	}
}

func TestKeylolLimitSteamCards(t *testing.T) {
	blocks := make([]keylolBlock, 0, 22)
	for i := 0; i < 22; i++ {
		blocks = append(blocks, keylolBlock{Kind: "steam_card", URL: fmt.Sprintf("https://store.steampowered.com/app/%d/", 1000+i)})
	}
	got := keylolLimitSteamCards(blocks, "https://keylol.com/t1-1-1", 20)
	steamCount := 0
	hasLink := false
	for _, block := range got {
		if block.Kind == "steam_card" {
			steamCount++
		}
		if block.Kind == "link" && strings.Contains(block.Text, "https://keylol.com/t1-1-1") {
			hasLink = true
		}
	}
	if steamCount != 20 || !hasLink {
		t.Fatalf("bad limited blocks count=%d link=%v %#v", steamCount, hasLink, got)
	}
}

func TestKeylolCleanTitleUnescapesQuotes(t *testing.T) {
	if got := keylolCleanTitle(`steam自动化&quot;倒余额&quot;工具`); got != `steam自动化"倒余额"工具` {
		t.Fatalf("bad title: %q", got)
	}
}

func TestKeylolCategoryLabel(t *testing.T) {
	cases := []struct {
		thread map[string]any
		raw    string
		want   string
	}{
		{map[string]any{"fid": "319", "typeid": "469"}, "https://keylol.com/t572814-1-1", "福利放送·Steam"},
		{map[string]any{"fid": "301", "typeid": "380"}, "https://keylol.com/forum.php?mod=viewthread&tid=1037076&typeid=380", "交易市场·Steam"},
	}
	for _, tt := range cases {
		if got := keylolCategoryLabel(tt.thread, tt.raw); got != tt.want {
			t.Fatalf("keylolCategoryLabel=%q want %q", got, tt.want)
		}
	}
}

func TestKeylolShortVideoDesc(t *testing.T) {
	if !keylolShortVideoDesc("") || !keylolShortVideoDesc("短简介") {
		t.Fatal("empty and short video descriptions should use compact card")
	}
	if keylolShortVideoDesc("这是一段超过二十个字的视频简介，用来展示长卡片正文区域") {
		t.Fatal("long video descriptions should use full card")
	}
}

func TestKeylolBuildBlocksKeepsColorAndLinkBlocks(t *testing.T) {
	html := `<strong><span style="color:Red">论坛信息</span></strong><br><strong><span style="color:Green">Steam购买</span></strong><br><a href="https://keylol.com/t1039189-1-1">2026年6月发售游戏汇总</a>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039283-1-1")
	kinds := []string{}
	for _, block := range blocks {
		kinds = append(kinds, block.Kind+":"+block.Text)
	}
	got := strings.Join(kinds, "|")
	for _, want := range []string{"color_red:论坛信息", "color_green:Steam购买", "link:2026年6月发售游戏汇总"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %#v", want, blocks)
		}
	}
}

func TestKeylolBuildBlocksKeepsCodeBlocks(t *testing.T) {
	html := `正文<br>[code]!addlicense ASF a/123,a/456[/code]<br><pre>https://keylol.com/t1-1-1</pre>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039283-1-1")
	count := 0
	for _, block := range blocks {
		if block.Kind == "code" {
			count++
			if !strings.Contains(block.Text, "addlicense") && !strings.Contains(block.Text, "https://keylol.com") {
				t.Fatalf("bad code text: %#v", block)
			}
		}
	}
	if count != 2 {
		t.Fatalf("code blocks=%d %#v", count, blocks)
	}
}

func TestKeylolInlineImageKeepsTextOrder(t *testing.T) {
	html := `<img width="72" height="39" src="https://blob.keylol.com/forum/new.png">《Bunny Guys》【现已可领取】`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	if len(blocks) != 2 || blocks[0].Kind != "inline_image" || blocks[1].Kind != "text" || !strings.Contains(blocks[1].Text, "Bunny Guys") {
		t.Fatalf("bad inline image/text blocks: %#v", blocks)
	}
}

func TestKeylolInlineImageAllowsRepeatedBadges(t *testing.T) {
	html := `<img width="44" height="19" src="https://blob.keylol.com/forum/new.png"> 第一行<br><img width="44" height="19" src="https://blob.keylol.com/forum/new.png"> 第二行`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	count := 0
	for _, block := range blocks {
		if block.Kind == "inline_image" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("repeated inline badges should be preserved, got %d: %#v", count, blocks)
	}
}

func TestKeylolSteamAppID(t *testing.T) {
	cases := map[string]string{
		`https://store.steampowered.com/app/2680010/The_Evil_Within_2/`: "2680010",
		`//store.steampowered.com/app/2333500/?utm_source=keylol`:       "2333500",
		`https://example.com/app/123`:                                   "",
	}
	for raw, want := range cases {
		if got := keylolSteamAppID(raw); got != want {
			t.Fatalf("keylolSteamAppID(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestKeylolBuildBlocksKeepsSteamCard(t *testing.T) {
	html := `<p>领取方式</p><a href="https://store.steampowered.com/app/2680010/The_Evil_Within_2/">Steam 上的 The Evil Within 2</a><p>正文继续</p>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	found := false
	for _, block := range blocks {
		if block.Kind == "steam_card" {
			found = true
			if block.URL != "https://store.steampowered.com/app/2680010/The_Evil_Within_2/" || block.Title != "The Evil Within 2" {
				t.Fatalf("bad steam block: %#v", block)
			}
		}
		if block.Kind == "text" && strings.Contains(block.Text, "Steam 上的 The Evil Within 2") {
			t.Fatalf("steam link text leaked into normal text: %#v", blocks)
		}
	}
	if !found {
		t.Fatalf("missing steam card: %#v", blocks)
	}
}

func TestKeylolBuildBlocksKeepsSteamWidgetCard(t *testing.T) {
	html := `<iframe src="https://store.steampowered.com/widget/4459100/?utm_source=keylol" style="border:none;height:190px;width:100%;max-width:646px;"></iframe><br><span><a href="https://store.steampowered.com/app/4459100/">Steam商店</a> | <a href="#asf4459100" onclick="setCopy('!addlicense asf a/'+this.href.split('#asf')[1], '代码复制成功');return false;">复制ASF代码</a></span>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1038886-1-1")
	gotKinds := []string{}
	for _, block := range blocks {
		gotKinds = append(gotKinds, block.Kind+":"+block.Title+block.Text)
	}
	got := strings.Join(gotKinds, "|")
	if strings.Count(got, "steam_card:") != 1 || !strings.Contains(got, "asf_link:4459100") {
		t.Fatalf("steam widget not parsed: %#v", blocks)
	}
}

func TestKeylolBuildBlocksDedupesSteamCards(t *testing.T) {
	html := `<a href="https://store.steampowered.com/app/1785650/NBA_2K26/">NBA 2K26</a><a href="https://store.steampowered.com/app/1785650/NBA_2K26/">Steam商店</a>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	count := 0
	for _, block := range blocks {
		if block.Kind == "steam_card" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("steam card count=%d blocks=%#v", count, blocks)
	}
}

func TestKeylolBuildBlocksFormatsASFLinks(t *testing.T) {
	html := `<p>Steam商店 Steam评测区 | 其乐相关帖 SteamDB AStats SCE Barter | Steam客户端中查看 入库或安装 | <a href="javascript:;" data-clipboard-text="!addlicense asf a/1785650">复制ASF代码</a></p>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t572814-1-1")
	if len(blocks) != 3 {
		t.Fatalf("blocks=%#v", blocks)
	}
	if blocks[0].Kind != "toolbar" || strings.Contains(blocks[0].Text, "复制ASF代码") {
		t.Fatalf("steam toolbar should be separated from asf label: %#v", blocks)
	}
	if blocks[1].Kind != "toolbar" || blocks[1].Text != "复制ASF代码" || strings.Contains(blocks[1].Text, "\n") {
		t.Fatalf("asf label should stay as one small toolbar line: %#v", blocks)
	}
	if blocks[2].Kind != "asf_link" || blocks[2].Title != "1785650" {
		t.Fatalf("bad asf link block: %#v", blocks)
	}
}

func TestKeylolBuildBlocksKeepsSteamToolbarTogether(t *testing.T) {
	html := `<a href="https://store.steampowered.com/app/2878980/NBA_2K26/">NBA 2K26</a><br>
<span><a href="https://store.steampowered.com/app/2878980/NBA_2K26/">Steam商店</a>，<a href="https://store.steampowered.com/app/2878980/NBA_2K26/#app_reviews_hash">Steam评测区</a> | <a href="https://keylol.com/plugin.php?id=keylol_tags:redirect&appid=2878980">其乐相关帖</a>，<a href="https://steamdb.info/app/2878980/">SteamDB</a>，<a href="https://astats.astats.nl/astats/Steam_Game_Info.php?AppID=2878980">AStats</a>，<a href="https://www.steamcardexchange.net/index.php?gamepage-appid-2878980">SCE</a>，<a href="https://barter.vg/steam/app/2878980/">Barter</a> | <a href="steam://nav/games/details/2878980">Steam客户端中查看</a>，<a href="steam://install/2878980">入库或安装</a> | <a href="#asf2878980" onclick="setCopy('!addlicense asf a/'+this.href.split('#asf')[1], '代码复制成功');return false;">复制ASF代码</a></span>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039213-1-1")
	gotKinds := []string{}
	for _, block := range blocks {
		gotKinds = append(gotKinds, block.Kind+":"+block.Text+block.Title)
	}
	got := strings.Join(gotKinds, "|")
	if strings.Count(got, "steam_card:") != 1 || !strings.Contains(got, "toolbar:Steam商店") || !strings.Contains(got, "toolbar:复制ASF代码") || !strings.Contains(got, "asf_link:2878980") {
		t.Fatalf("bad toolbar blocks: %#v", blocks)
	}
	if strings.Contains(got, "|link:") || strings.HasPrefix(got, "link:") {
		t.Fatalf("toolbar should not split normal links: %#v", blocks)
	}
}

func TestKeylolASFForwardItems(t *testing.T) {
	blocks := []keylolBlock{
		{Kind: "steam_card", URL: "https://store.steampowered.com/app/2878980/NBA_2K26/", Title: "NBA 2K26", Cover: "https://cdn.example.com/nba.jpg"},
		{Kind: "toolbar", Text: "复制ASF代码"},
		{Kind: "asf_link", Title: "2878980"},
		{Kind: "steam_card", URL: "https://store.steampowered.com/app/1785650/TopSpin_2K25/", Title: "TopSpin 2K25", Cover: "https://cdn.example.com/topspin.jpg"},
		{Kind: "asf_link", Title: "1785650"},
		{Kind: "asf_link", Title: "1785650"},
	}
	items := keylolASFForwardItems(mediaMeta{Platform: "keylol", KeylolBlocks: blocks})
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	if items[0].AppID != "2878980" || items[0].Title != "NBA 2K26" || items[0].Code != "!addlicense asf a/2878980" || items[0].Cover == "" {
		t.Fatalf("bad first item: %#v", items[0])
	}
	if items[1].AppID != "1785650" || items[1].Title != "TopSpin 2K25" || items[1].Code != "!addlicense asf a/1785650" {
		t.Fatalf("bad second item: %#v", items[1])
	}
}

func TestKeylolASFBatchCode(t *testing.T) {
	items := []keylolASFForwardItem{{AppID: "2878980"}, {AppID: "1785650"}, {AppID: "2878980"}}
	if got := keylolASFBatchCode(items); got != "!addlicense ASF a/2878980,a/1785650" {
		t.Fatalf("batch=%q", got)
	}
}

func TestKeylolBuildBlocksUsesSplitAttachmentURL(t *testing.T) {
	attachments := map[string]any{
		"2432618": map[string]any{
			"url":        "https://blob.keylol.com/forum/",
			"attachment": "202605/30/174513jq3aqzo838ku8ldk.png",
			"isimage":    "1",
		},
	}
	blocks := keylolBuildBlocks("羊毛裙返利链接<br>领红包 更省钱：https://u.jd.com/RrRshM8", attachments, "https://keylol.com/t1039270-1-1")
	if len(blocks) != 2 || blocks[0].Kind != "text" || blocks[1].Kind != "image" {
		t.Fatalf("bad blocks: %#v", blocks)
	}
	if got := blocks[1].URL; got != "https://blob.keylol.com/forum/202605/30/174513jq3aqzo838ku8ldk.png" {
		t.Fatalf("attachment url=%q", got)
	}
}

func TestKeylolFetchSteamAppFallbackFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("appids") != "2680010" || r.URL.Query().Get("cc") != "cn" || r.URL.Query().Get("l") != "schinese" {
			t.Fatalf("bad steam query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"2680010": {
				"success": true,
				"data": {
					"name": "The Evil Within 2",
					"short_description": "",
					"about_the_game": "<p>生存恐怖续作，寻找你的女儿。</p>",
					"header_image": "",
					"capsule_image": "https://cdn.example.com/capsule.jpg"
				}
			}
		}`))
	}))
	defer srv.Close()
	oldAPI := steamAPIBase
	steamAPIBase = srv.URL
	defer func() { steamAPIBase = oldAPI }()

	info, err := keylolFetchSteamApp("https://store.steampowered.com/app/2680010/The_Evil_Within_2/")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "The Evil Within 2" || info.Desc != "生存恐怖续作，寻找你的女儿。" || info.Cover != "https://cdn.example.com/capsule.jpg" {
		t.Fatalf("bad steam info: %#v", info)
	}
}

func TestSteamPlatformDetection(t *testing.T) {
	cfg := defaultConfig()
	links := extractLinks("看看 https://store.steampowered.com/app/1245620/ELDEN_RING/", cfg)
	if len(links) != 1 || links[0].Platform != "steam" {
		t.Fatalf("steam link not detected: %#v", links)
	}
}

func TestParseSteamUsesStoreAPIs(t *testing.T) {
	appSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("appids"); got != "1245620" {
			t.Fatalf("bad app id: %s", got)
		}
		_, _ = w.Write([]byte(`{"1245620":{"success":true,"data":{"name":"ELDEN RING","steam_appid":1245620,"header_image":"https://cdn.example.com/header.jpg","capsule_image":"https://cdn.example.com/capsule.jpg","short_description":"THE NEW FANTASY ACTION RPG.","genres":[{"description":"动作角色扮演"},{"description":"开放世界"}],"price_overview":{"currency":"CNY","initial":39800,"final":29800,"discount_percent":25,"initial_formatted":"¥ 398.00","final_formatted":"¥ 298.00"}}}}`))
	}))
	defer appSrv.Close()
	reviewSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/1245620") {
			t.Fatalf("bad review path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":1,"query_summary":{"total_reviews":1000,"total_positive":950,"review_score_desc":"好评如潮"}}`))
	}))
	defer reviewSrv.Close()
	oldAppAPI := steamAPIBase
	oldReviewAPI := steamReviewsAPIBase
	steamAPIBase = appSrv.URL
	steamReviewsAPIBase = reviewSrv.URL
	defer func() {
		steamAPIBase = oldAppAPI
		steamReviewsAPIBase = oldReviewAPI
	}()

	meta, err := parseSteam(defaultConfig(), "https://store.steampowered.com/app/1245620/ELDEN_RING/")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Platform != "steam" || meta.Title != "ELDEN RING" || meta.Cover == "" {
		t.Fatalf("bad steam meta: %#v", meta)
	}
	if meta.SteamAppID != "1245620" || meta.SteamReviewPercent != 95 || meta.SteamReviewSummary != "好评如潮" {
		t.Fatalf("bad steam review meta: %#v", meta)
	}
	if meta.SteamPriceCurrent != "¥ 298.00" || meta.SteamPriceOriginal != "¥ 398.00" || meta.SteamDiscount != 25 {
		t.Fatalf("bad steam price meta: %#v", meta)
	}
	if got := strings.Join(meta.SteamGenres, "|"); !strings.Contains(got, "动作角色扮演") || !strings.Contains(got, "开放世界") {
		t.Fatalf("bad steam genres: %#v", meta.SteamGenres)
	}
}

func TestRenderSteamGameCardPreview(t *testing.T) {
	oldCacheDir := cacheDir
	cacheDir = t.TempDir()
	defer func() { cacheDir = oldCacheDir }()

	out, err := renderInfoCard(mediaMeta{
		SourceURL:          "steam-preview",
		Platform:           "steam",
		Title:              "Elden Ring",
		Desc:               "艾尔登法环",
		SteamAppID:         "1245620",
		SteamGenres:        []string{"Action RPG", "Open World", "Fantasy"},
		SteamReviewPercent: 95,
		SteamReviewSummary: "特别好评",
		SteamPriceCurrent:  "¥298.00",
		SteamPriceOriginal: "¥398.00",
		SteamDiscount:      25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
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

func TestProxyOnlyAppliesToOverseasPlatforms(t *testing.T) {
	cfg := defaultConfig()
	cfg.Proxy = "socks5://127.0.0.1:7890"

	for _, platform := range []string{"twitter", "tiktok", "youtube", "instagram"} {
		if got := proxyForPlatform(cfg, platform); got != cfg.Proxy {
			t.Fatalf("proxyForPlatform(%q)=%q, want %q", platform, got, cfg.Proxy)
		}
	}
	for _, platform := range []string{"bilibili", "douyin", "xiaohongshu", "weibo", ""} {
		if got := proxyForPlatform(cfg, platform); got != "" {
			t.Fatalf("proxyForPlatform(%q)=%q, want empty", platform, got)
		}
	}
}

func TestYTDLPProxyIsScopedToOverseasPlatforms(t *testing.T) {
	cfg := defaultConfig()
	cfg.Proxy = "http://127.0.0.1:7890"

	ytArgs := []string{"-J"}
	if proxy := proxyForPlatform(cfg, "youtube"); proxy != "" {
		ytArgs = append(ytArgs, "--proxy", proxy)
	}
	if !containsArgPair(ytArgs, "--proxy", cfg.Proxy) {
		t.Fatalf("youtube proxy missing: %#v", ytArgs)
	}

	biliArgs := []string{"-J"}
	if proxy := proxyForPlatform(cfg, "bilibili"); proxy != "" {
		biliArgs = append(biliArgs, "--proxy", proxy)
	}
	if containsArgPair(biliArgs, "--proxy", cfg.Proxy) {
		t.Fatalf("bilibili should not use proxy: %#v", biliArgs)
	}
}

func TestOneBotLocalMediaTargetPrefersLoopbackCacheURL(t *testing.T) {
	oldCacheDir := cacheDir
	oldSystem := runtimeSystem
	cacheDir = filepath.Join(t.TempDir(), "data", "mediaparser", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cacheDir, "card_test.png")
	if err := os.WriteFile(card, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	SetRuntimeSystemSettings(SystemSettings{
		WSURL:           "ws://127.0.0.1:3001",
		OneBotDataDir:   "/host/data",
		QQBotPublicBase: "https://public.example/cache",
	})
	defer func() {
		cacheDir = oldCacheDir
		SetRuntimeSystemSettings(oldSystem)
	}()

	got := oneBotLocalMediaTarget(card)
	want := "http://127.0.0.1:3088/cache/card_test.png"
	if got != want {
		t.Fatalf("target=%q, want %q", got, want)
	}
}

func TestOneBotLocalMediaTargetUsesMappedPathWhenNotLoopback(t *testing.T) {
	oldCacheDir := cacheDir
	oldSystem := runtimeSystem
	cacheDir = filepath.Join(t.TempDir(), "data", "mediaparser", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cacheDir, "nested", "card_test.png")
	if err := os.MkdirAll(filepath.Dir(card), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(card, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	SetRuntimeSystemSettings(SystemSettings{
		WSURL:           "ws://0.0.0.0:3001",
		OneBotDataDir:   "/host/data",
		QQBotPublicBase: "https://public.example/cache",
	})
	defer func() {
		cacheDir = oldCacheDir
		SetRuntimeSystemSettings(oldSystem)
	}()

	got := filepath.ToSlash(oneBotLocalMediaTarget(card))
	want := "file:///host/data/mediaparser/cache/nested/card_test.png"
	if got != want {
		t.Fatalf("target=%q, want %q", got, want)
	}
}

func TestOneBotLocalMediaTargetFallsBackToPublicURL(t *testing.T) {
	oldCacheDir := cacheDir
	oldSystem := runtimeSystem
	cacheDir = filepath.Join(t.TempDir(), "data", "mediaparser", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(cacheDir, "card_test.png")
	if err := os.WriteFile(card, []byte("png"), 0644); err != nil {
		t.Fatal(err)
	}
	SetRuntimeSystemSettings(SystemSettings{
		WSURL:           "ws://0.0.0.0:3001",
		QQBotPublicBase: "https://public.example/cache/",
	})
	defer func() {
		cacheDir = oldCacheDir
		SetRuntimeSystemSettings(oldSystem)
	}()

	got := oneBotLocalMediaTarget(card)
	want := "https://public.example/cache/card_test.png"
	if got != want {
		t.Fatalf("target=%q, want %q", got, want)
	}
}

func TestQQBotMediaTargetUsesContainerLocalPath(t *testing.T) {
	oldCacheDir := cacheDir
	oldSystem := runtimeSystem
	cacheDir = filepath.Join(t.TempDir(), "data", "mediaparser", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(cacheDir, "bilibili_test", "dash_0.mp4")
	image := filepath.Join(cacheDir, "weibo_test", "image_0.jpg")
	for _, path := range []string{video, image} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("media"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	SetRuntimeSystemSettings(SystemSettings{
		WSURL:           "ws://127.0.0.1:3001",
		OneBotDataDir:   "/host/data",
		QQBotPublicBase: "https://public.example/cache/",
	})
	defer func() {
		cacheDir = oldCacheDir
		SetRuntimeSystemSettings(oldSystem)
	}()

	meta := mediaMeta{
		VideoURLs:  [][]string{{"https://example.com/video.mp4"}},
		ImageURLs:  [][]string{{"https://example.com/image.jpg"}},
		VideoModes: []string{"local"},
		ImageModes: []string{"local"},
		FilePaths:  []string{video, image},
	}
	if got, want := qqBotMediaVideoTarget(&meta, 0), filepath.Clean(video); filepath.Clean(got) != want {
		t.Fatalf("qqbot video target=%q, want local %q", got, want)
	}
	if got, want := qqBotMediaImageTarget(&meta, 0), filepath.Clean(image); filepath.Clean(got) != want {
		t.Fatalf("qqbot image target=%q, want local %q", got, want)
	}
}

func TestOneBotUploadFilePathPrefersMappedHostPath(t *testing.T) {
	oldCacheDir := cacheDir
	oldSystem := runtimeSystem
	cacheDir = filepath.Join(t.TempDir(), "data", "mediaparser", "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cacheDir, "nested", "shield_0.zip")
	if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("zip"), 0644); err != nil {
		t.Fatal(err)
	}
	SetRuntimeSystemSettings(SystemSettings{
		WSURL:         "ws://0.0.0.0:3001",
		OneBotDataDir: "/host/data",
	})
	defer func() {
		cacheDir = oldCacheDir
		SetRuntimeSystemSettings(oldSystem)
	}()

	got := filepath.ToSlash(oneBotUploadFilePath(archive))
	want := filepath.ToSlash("/host/data/mediaparser/cache/nested/shield_0.zip")
	if got != want {
		t.Fatalf("upload path=%q, want %q", got, want)
	}
}

func TestMergeSystemSettingsForStartupKeepsRuntimeDataDir(t *testing.T) {
	current := SystemSettings{
		WebUIAddr:     "0.0.0.0:3000",
		WSURL:         "ws://127.0.0.1:3001",
		OneBotDataDir: "/host/data",
	}
	saved := SystemSettings{
		WebUIAddr:     "127.0.0.1:3000",
		WSURL:         "ws://old:3001",
		OneBotDataDir: "/app/data",
		QQBotName:     "saved",
	}

	got := mergeSystemSettingsForStartup(current, saved)
	if got.OneBotDataDir != current.OneBotDataDir {
		t.Fatalf("onebot data dir=%q, want runtime %q", got.OneBotDataDir, current.OneBotDataDir)
	}
	if got.WSURL != current.WSURL {
		t.Fatalf("ws url=%q, want runtime %q", got.WSURL, current.WSURL)
	}
	if got.QQBotName != saved.QQBotName {
		t.Fatalf("qqbot name=%q, want saved fallback %q", got.QQBotName, saved.QQBotName)
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

func TestPlatformResolutionOverridesGlobalResolution(t *testing.T) {
	cfg := defaultConfig()
	cfg.VideoMaxResolution = 1080
	cfg.PlatformResolution = map[string]int{
		"bilibili": 720,
		"acfun":    360,
	}
	normalizeConfig(&cfg)

	bili := configForPlatform(cfg, "bilibili")
	if bili.VideoMaxResolution != 720 {
		t.Fatalf("bilibili resolution=%d", bili.VideoMaxResolution)
	}
	if bili.BilibiliMaxQuality != "720P" {
		t.Fatalf("bilibili quality=%q", bili.BilibiliMaxQuality)
	}

	acfun := configForPlatform(cfg, "acfun")
	if acfun.VideoMaxResolution != 360 {
		t.Fatalf("acfun resolution=%d", acfun.VideoMaxResolution)
	}

	douyin := configForPlatform(cfg, "douyin")
	if douyin.VideoMaxResolution != 1080 {
		t.Fatalf("douyin should use global resolution, got %d", douyin.VideoMaxResolution)
	}
}

func TestNormalizeConfigDropsInvalidPlatformResolution(t *testing.T) {
	cfg := defaultConfig()
	cfg.VideoMaxResolution = 720
	cfg.PlatformResolution = map[string]int{
		"bilibili": 999,
	}
	normalizeConfig(&cfg)

	if cfg.PlatformResolution["bilibili"] != 0 {
		t.Fatalf("invalid platform resolution should reset to 0, got %d", cfg.PlatformResolution["bilibili"])
	}
	if got := configForPlatform(cfg, "bilibili").VideoMaxResolution; got != 0 {
		t.Fatalf("explicit unlimited platform override should win over global, got %d", got)
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

func TestCardTitleWrapAvoidsOrphansAndHangingPunctuation(t *testing.T) {
	lines := wrapCardText("完全由大脑驱动的哈基米德田园猫太豪了", 32)
	if len(lines) != 1 {
		t.Fatalf("short Chinese title should not be split into orphan lines: %#v", lines)
	}

	lines = wrapCardText("【AC19】 给Ac娘19岁写的一首歌～《AC Pink Heart》 来了！", 34)
	for _, line := range lines {
		if strings.HasSuffix(line, "《") || strings.HasPrefix(line, "》") {
			t.Fatalf("title line has hanging book-title punctuation: %#v", lines)
		}
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

func TestCompactCardImagesDropsFailedFetches(t *testing.T) {
	img := testGradientImage(32, 32, color.RGBA{R: 1, G: 2, B: 3, A: 255}, color.RGBA{R: 4, G: 5, B: 6, A: 255})
	blank := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(blank, blank.Bounds(), &image.Uniform{C: color.RGBA{R: 216, G: 216, B: 216, A: 255}}, image.Point{}, draw.Src)
	got := compactCardImages([]image.Image{img, nil, blank, img})
	if len(got) != 2 || got[0] == nil || got[1] == nil {
		t.Fatalf("bad compacted images: %#v", got)
	}
	if h := galleryGridHeightForImages(nil, 900); h != 0 {
		t.Fatalf("empty gallery should have no placeholder height, got %d", h)
	}
}

func TestBlankCardImageDetectsLightPlaceholders(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 248, G: 248, B: 248, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(112, 112, 120, 120), &image.Uniform{C: color.RGBA{R: 210, G: 210, B: 210, A: 255}}, image.Point{}, draw.Src)
	if !isBlankCardImage(img) {
		t.Fatal("mostly white placeholder should be treated as blank")
	}
}

func TestGalleryGridColsAvoidsEightImageHole(t *testing.T) {
	if cols := galleryGridCols(8); cols != 2 {
		t.Fatalf("8-image galleries should use 2 columns to avoid a trailing empty slot, got %d", cols)
	}
}

func TestFloatingImageCellDrawsAfterPreviousClips(t *testing.T) {
	dc := gg.NewContext(260, 130)
	dc.SetRGB255(255, 255, 255)
	dc.Clear()
	red := image.NewRGBA(image.Rect(0, 0, 40, 40))
	draw.Draw(red, red.Bounds(), &image.Uniform{C: color.RGBA{R: 220, G: 20, B: 20, A: 255}}, image.Point{}, draw.Src)
	blue := image.NewRGBA(image.Rect(0, 0, 40, 40))
	draw.Draw(blue, blue.Bounds(), &image.Uniform{C: color.RGBA{R: 20, G: 80, B: 220, A: 255}}, image.Point{}, draw.Src)

	drawFloatingImageCellAnchored(dc, red, 10, 10, 100, 100, imaging.Center)
	drawFloatingImageCellAnchored(dc, blue, 140, 10, 100, 100, imaging.Center)

	r, g, b, _ := dc.Image().At(190, 60).RGBA()
	if uint8(r>>8) < 10 || uint8(g>>8) < 40 || uint8(b>>8) < 180 {
		t.Fatalf("second floating cell image was not drawn, got rgb=(%d,%d,%d)", uint8(r>>8), uint8(g>>8), uint8(b>>8))
	}
}

func TestFetchCardImageGroupFallsBackFromBlankCandidate(t *testing.T) {
	blank := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(blank, blank.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 255, B: 255, A: 255}}, image.Point{}, draw.Src)
	good := testGradientImage(32, 32, color.RGBA{R: 16, G: 32, B: 48, A: 255}, color.RGBA{R: 180, G: 80, B: 40, A: 255})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		switch r.URL.Path {
		case "/blank.png":
			_ = png.Encode(w, blank)
		case "/good.png":
			_ = png.Encode(w, good)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	img := fetchCardImageGroup([]string{server.URL + "/blank.png", server.URL + "/good.png"}, nil)
	if isBlankCardImage(img) {
		t.Fatal("expected fallback image to be non-blank")
	}
}

func TestXhsImageURLCandidatesKeepsFallbacks(t *testing.T) {
	img := map[string]any{
		"urlDefault": "https://img.example.com/default.jpg",
		"url":        "https://img.example.com/raw.jpg",
		"infoList": []any{
			map[string]any{"imageScene": "CRD_PRV_WEBP", "url": "https://img.example.com/preview.webp"},
			map[string]any{"imageScene": "WB_DFT", "url": "https://img.example.com/wb.jpg"},
			map[string]any{"imageScene": "CRD_WM_WEBP", "url": "https://img.example.com/wm.webp"},
		},
	}
	got := xhsImageURLCandidates(img)
	want := []string{
		"https://img.example.com/wb.jpg",
		"https://img.example.com/wm.webp",
		"https://img.example.com/preview.webp",
		"https://img.example.com/default.jpg",
		"https://img.example.com/raw.jpg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected candidates:\n got=%#v\nwant=%#v", got, want)
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

func TestRenderKeylolSteamToolbarPreview(t *testing.T) {
	if os.Getenv("MEDIAPARSER_KEYLOL_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_KEYLOL_PREVIEW=1 to render keylol preview")
	}
	oldCacheDir := cacheDir
	cacheDir = filepath.Join("..", "..", "build", "mediaparser-keylol-preview")
	defer func() { cacheDir = oldCacheDir }()

	html := `<a href="https://store.steampowered.com/app/2878980/NBA_2K26/">NBA 2K26</a><br>
<span style="font-size:10px"><a href="https://store.steampowered.com/app/2878980/NBA_2K26/">Steam商店</a>，<a href="https://store.steampowered.com/app/2878980/NBA_2K26/#app_reviews_hash">Steam评测区</a> | <a href="https://keylol.com/plugin.php?id=keylol_tags:redirect&appid=2878980">其乐相关帖</a>，<a href="https://steamdb.info/app/2878980/">SteamDB</a>，<a href="https://astats.astats.nl/astats/Steam_Game_Info.php?AppID=2878980">AStats</a>，<a href="https://www.steamcardexchange.net/index.php?gamepage-appid-2878980">SCE</a>，<a href="https://barter.vg/steam/app/2878980/">Barter</a> | <a href="steam://nav/games/details/2878980">Steam客户端中查看</a>，<a href="steam://install/2878980">入库或安装</a> | <a href="#asf2878980" onclick="setCopy('!addlicense asf a/'+this.href.split('#asf')[1], '代码复制成功');return false;">复制ASF代码</a></span><br>
<a href="https://store.steampowered.com/app/1785650/TopSpin_2K25/">TopSpin 2K25</a><br>
<span style="font-size:10px"><a href="https://store.steampowered.com/app/1785650/TopSpin_2K25/">Steam商店</a>，<a href="https://store.steampowered.com/app/1785650/TopSpin_2K25/#app_reviews_hash">Steam评测区</a> | <a href="https://keylol.com/plugin.php?id=keylol_tags:redirect&appid=1785650">其乐相关帖</a>，<a href="https://steamdb.info/app/1785650/">SteamDB</a>，<a href="https://astats.astats.nl/astats/Steam_Game_Info.php?AppID=1785650">AStats</a>，<a href="https://www.steamcardexchange.net/index.php?gamepage-appid-1785650">SCE</a>，<a href="https://barter.vg/steam/app/1785650/">Barter</a> | <a href="steam://nav/games/details/1785650">Steam客户端中查看</a>，<a href="steam://install/1785650">入库或安装</a> | <a href="#asf1785650" onclick="setCopy('!addlicense asf a/'+this.href.split('#asf')[1], '代码复制成功');return false;">复制ASF代码</a></span>`
	blocks := keylolBuildBlocks(html, nil, "https://keylol.com/t1039213-1-1")
	blocks = keylolEnrichSteamBlocks(blocks)
	out, err := renderInfoCard(mediaMeta{
		URL:          "https://keylol.com/t1039213-1-1",
		SourceURL:    "keylol-toolbar-preview",
		Platform:     "keylol",
		Title:        "【预告】HB 游戏包 2K Sports Champions",
		Author:       "万猫飞仙",
		Timestamp:    "昨天 02:13",
		Desc:         keylolDescFromBlocks(blocks),
		KeylolBlocks: blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
}

func TestRenderKeylolCompactVideoPreview(t *testing.T) {
	if os.Getenv("MEDIAPARSER_KEYLOL_VIDEO_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_KEYLOL_VIDEO_PREVIEW=1 to render keylol video preview")
	}
	oldCacheDir := cacheDir
	cacheDir = filepath.Join("..", "..", "build", "mediaparser-keylol-video-preview")
	defer func() { cacheDir = oldCacheDir }()

	coverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		img := image.NewRGBA(image.Rect(0, 0, 960, 540))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 32, G: 58, B: 88, A: 255}}, image.Point{}, draw.Src)
		for x := 0; x < 960; x += 24 {
			draw.Draw(img, image.Rect(x, 0, minInt(x+12, 960), 540), &image.Uniform{C: color.RGBA{R: 52, G: 112, B: 156, A: 255}}, image.Point{}, draw.Src)
		}
		_ = png.Encode(w, img)
	}))
	defer coverSrv.Close()

	blocks := []keylolBlock{
		{Kind: "heading1", Text: "宣传视频"},
		{Kind: "video_embed", URL: "https://www.bilibili.com/video/BV16AGQ6LEQJ", Title: "《使命召唤：现代战争4》中文预告", Cover: coverSrv.URL + "/cover.png"},
		{Kind: "video_embed", URL: "https://www.bilibili.com/video/BV16AGQ6LEQJ", Title: "长简介视频卡片", Desc: "这是一段超过二十个字的视频简介，用来展示长卡片正文区域在帖子中的阅读效果。", Cover: coverSrv.URL + "/cover.png"},
	}
	out, err := renderInfoCard(mediaMeta{
		URL:          "https://keylol.com/t-video-preview",
		SourceURL:    "keylol-video-preview",
		Platform:     "keylol",
		Title:        "Keylol 视频卡片预览",
		Author:       "Codex",
		Timestamp:    "刚刚",
		KeylolBlocks: blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
}

func TestRenderAllPlatformCardPreviews(t *testing.T) {
	if os.Getenv("MEDIAPARSER_ALL_PLATFORM_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_ALL_PLATFORM_PREVIEW=1 to render all platform previews")
	}
	outDir := firstNonEmpty(os.Getenv("MEDIAPARSER_PREVIEW_DIR"), filepath.Join("..", "..", "build", "mediaparser-all-platform-preview"))
	oldCacheDir := cacheDir
	cacheDir = outDir
	defer func() { cacheDir = oldCacheDir }()

	assetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var img image.Image
		switch r.URL.Path {
		case "/avatar.png":
			img = testGradientImage(180, 180, color.RGBA{R: 98, G: 154, B: 230, A: 255}, color.RGBA{R: 231, G: 244, B: 255, A: 255})
		case "/cover-wide.png":
			img = testGradientImage(1280, 720, color.RGBA{R: 42, G: 62, B: 92, A: 255}, color.RGBA{R: 72, G: 154, B: 202, A: 255})
		case "/cover-tall.png":
			img = testGradientImage(720, 1080, color.RGBA{R: 102, G: 76, B: 170, A: 255}, color.RGBA{R: 232, G: 95, B: 124, A: 255})
		default:
			img = testGradientImage(900, 900, color.RGBA{R: 40, G: 92, B: 120, A: 255}, color.RGBA{R: 238, G: 184, B: 98, A: 255})
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	}))
	defer assetSrv.Close()

	base := mediaMeta{
		Author:    "预览用户",
		Avatar:    assetSrv.URL + "/avatar.png",
		Timestamp: "2026-05-31 04:30",
		Desc:      "这是一段用于检查卡片排版、平台 logo、透明底和正文换行的预览文本。",
		Cover:     assetSrv.URL + "/cover-wide.png",
		VideoURLs: [][]string{{assetSrv.URL + "/video.mp4"}},
	}
	platformsForPreview := []string{"bilibili", "douyin", "tiktok", "kuaishou", "weibo", "xiaohongshu", "xianyu", "acfun", "youtube", "instagram", "toutiao", "xiaoheihe", "twitter"}
	written := []string{}
	for _, platform := range platformsForPreview {
		video := base
		video.SourceURL = "preview-" + platform + "-video"
		video.Platform = platform
		video.Title = platformLabel(platform) + " 视频模式"
		out, err := renderInfoCard(video)
		if err != nil {
			t.Fatalf("%s video: %v", platform, err)
		}
		written = append(written, out)

		gallery := base
		gallery.SourceURL = "preview-" + platform + "-gallery"
		gallery.Platform = platform
		gallery.Title = platformLabel(platform) + " 图文模式"
		gallery.VideoURLs = nil
		gallery.ImageURLs = [][]string{{assetSrv.URL + "/cover-wide.png"}, {assetSrv.URL + "/cover-tall.png"}, {assetSrv.URL + "/cover-square.png"}}
		out, err = renderInfoCard(gallery)
		if err != nil {
			t.Fatalf("%s gallery: %v", platform, err)
		}
		written = append(written, out)

		if isCombinedMediaPlatform(platform) {
			mixed := gallery
			mixed.SourceURL = "preview-" + platform + "-mixed"
			mixed.Title = platformLabel(platform) + " 混合模式"
			mixed.VideoURLs = [][]string{{assetSrv.URL + "/video.mp4"}}
			mixed.MediaItems = []mediaItem{{Kind: "image", Index: 0}, {Kind: "video", Index: 0}, {Kind: "image", Index: 1}}
			out, err = renderInfoCard(mixed)
			if err != nil {
				t.Fatalf("%s mixed: %v", platform, err)
			}
			written = append(written, out)
		}
	}
	steamOut, err := renderInfoCard(mediaMeta{
		SourceURL:          "preview-steam-video",
		Platform:           "steam",
		Title:              "人渣",
		Desc:               "欢迎来到《SCUM》——一款残酷的开放世界生存游戏。",
		Cover:              assetSrv.URL + "/cover-tall.png",
		SteamHeaderImage:   assetSrv.URL + "/cover-wide.png",
		SteamGenres:        []string{"动作", "冒险", "独立", "大型多人在线"},
		SteamReviewPercent: 74,
		SteamReviewSummary: "Mostly Positive",
		SteamPriceCurrent:  "¥ 70.00",
		SteamPriceOriginal: "¥ 175.00",
		SteamDiscount:      60,
	})
	if err != nil {
		t.Fatal(err)
	}
	written = append(written, steamOut)
	keylolOut, err := renderInfoCard(mediaMeta{
		SourceURL:      "preview-keylol-thread",
		Platform:       "keylol",
		Title:          "限免游戏福利《暗黑破坏神四》国服",
		Author:         "福利搬运",
		Timestamp:      "刚刚",
		KeylolCategory: "福利放送·Steam",
		KeylolBlocks: []keylolBlock{
			{Kind: "text", Text: "这里是 Keylol 帖子正文预览，用于检查头部分类、右上角 logo 和时间位置。"},
			{Kind: "image", URL: assetSrv.URL + "/cover-wide.png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	written = append(written, keylolOut)
	t.Logf("wrote %d previews to %s\n%s", len(written), outDir, strings.Join(written, "\n"))
}

func TestRenderKeylolLivePreview(t *testing.T) {
	raw := os.Getenv("MEDIAPARSER_KEYLOL_LIVE_PREVIEW")
	if raw == "" {
		t.Skip("set MEDIAPARSER_KEYLOL_LIVE_PREVIEW to a keylol thread URL")
	}
	oldCacheDir := cacheDir
	cacheDir = firstNonEmpty(os.Getenv("MEDIAPARSER_KEYLOL_PREVIEW_DIR"), filepath.Join("..", "..", "build", "mediaparser-keylol-live-preview"))
	defer func() { cacheDir = oldCacheDir }()

	cfg := defaultConfig()
	cfg.KeylolCookie = os.Getenv("MEDIAPARSER_KEYLOL_COOKIE")
	meta, err := parseKeylol(cfg, raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderInfoCard(meta)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
}

func TestRenderKeylolLivePreviewBatch(t *testing.T) {
	listPath := os.Getenv("MEDIAPARSER_KEYLOL_LIVE_PREVIEW_LIST")
	if listPath == "" {
		t.Skip("set MEDIAPARSER_KEYLOL_LIVE_PREVIEW_LIST to a json link list")
	}
	outDir := firstNonEmpty(os.Getenv("MEDIAPARSER_KEYLOL_PREVIEW_DIR"), filepath.Join("..", "..", "build", "mediaparser-keylol-live-preview"))
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	var boards []struct {
		FID     string `json:"fid"`
		Text    string `json:"text"`
		Threads []struct {
			TID   string `json:"tid"`
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(data, &boards); err != nil {
		t.Fatal(err)
	}

	oldCacheDir := cacheDir
	cacheDir = outDir
	defer func() { cacheDir = oldCacheDir }()

	oldTheme, hadTheme := os.LookupEnv("MEDIAPARSER_KEYLOL_THEME")
	defer func() {
		if hadTheme {
			_ = os.Setenv("MEDIAPARSER_KEYLOL_THEME", oldTheme)
		} else {
			_ = os.Unsetenv("MEDIAPARSER_KEYLOL_THEME")
		}
	}()

	cfg := defaultConfig()
	cfg.KeylolCookie = os.Getenv("MEDIAPARSER_KEYLOL_COOKIE")
	themes := []string{"light", "dark"}
	written := 0
	failures := []string{}
	index := 1
	for _, board := range boards {
		for _, thread := range board.Threads {
			if thread.URL == "" {
				continue
			}
			for _, theme := range themes {
				_ = os.Setenv("MEDIAPARSER_KEYLOL_THEME", theme)
				meta, err := parseKeylol(cfg, thread.URL)
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s %s: %v", board.Text, thread.URL, err))
					continue
				}
				out, err := renderInfoCard(meta)
				if err != nil {
					failures = append(failures, fmt.Sprintf("%s %s: %v", board.Text, thread.URL, err))
					continue
				}
				name := fmt.Sprintf("%03d_%s_%s_%s.png", index, sanitizePreviewName(board.Text), firstNonEmpty(thread.TID, keylolThreadID(thread.URL)), theme)
				target := filepath.Join(outDir, name)
				_ = os.Remove(target)
				if err := os.Rename(out, target); err != nil {
					t.Fatal(err)
				}
				t.Log(target)
				written++
			}
			index++
		}
	}
	if len(failures) > 0 {
		failurePath := filepath.Join(outDir, "failures.txt")
		_ = os.WriteFile(failurePath, []byte(strings.Join(failures, "\n")), 0644)
		t.Logf("keylol preview failures: %d, see %s", len(failures), failurePath)
	}
	if written == 0 {
		t.Fatalf("no keylol previews rendered")
	}
}

func sanitizePreviewName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "board"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			return r
		case r >= '\u4e00' && r <= '\u9fff':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

func TestRenderKeylolSpoilerPreview(t *testing.T) {
	if os.Getenv("MEDIAPARSER_KEYLOL_SPOILER_PREVIEW") == "" {
		t.Skip("set MEDIAPARSER_KEYLOL_SPOILER_PREVIEW=1 to render keylol spoiler preview")
	}
	oldCacheDir := cacheDir
	cacheDir = filepath.Join("..", "..", "build", "mediaparser-keylol-spoiler-preview")
	defer func() { cacheDir = oldCacheDir }()

	blocks := keylolBuildBlocks(`经典镜头结尾，留下 Will Return 的提示，<br><span class="bbcode_spoiler"><span class="bbcode_spoiler_content">追击环肆女郎</span></span><br>后续图片应继续正常排版。<br><a href="https://keylol.com/t1039189-1-1">相关帖子链接</a><br>[code]示例配置内容 = true[/code]`, nil, "https://keylol.com/t1039233-1-1")
	out, err := renderInfoCard(mediaMeta{
		URL:          "https://keylol.com/t1039233-1-1",
		SourceURL:    "keylol-spoiler-preview",
		Platform:     "keylol",
		Title:        "涂黑文字块预览",
		Author:       "万猫飞仙",
		Timestamp:    "刚刚",
		Desc:         keylolDescFromBlocks(blocks),
		KeylolBlocks: blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(out)
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

func TestSafetyBlockedUsesGlobalCategories(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories[safetyCategoryAdult] = true
	meta := mediaMeta{Platform: "bilibili", Title: "normal title", Desc: "contains NSFW marker"}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected global adult category to block")
	}
	if hit.Category != safetyCategoryAdult {
		t.Fatalf("category=%s", hit.Category)
	}
}

func TestSafetyBlockedUsesXAdultTags(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories[safetyCategoryAdult] = true
	meta := mediaMeta{Platform: "twitter", Title: "tag batch", Desc: "#goon #nsfwtwt wataa"}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected X adult tags to block")
	}
	if hit.Category != safetyCategoryAdult {
		t.Fatalf("category=%s", hit.Category)
	}
}

func TestSafetyBlockedUsesTwitterSensitiveMarker(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories = map[string]bool{}
	meta := mediaMeta{Platform: "twitter", AccessText: safetyMarkerTwitterSensitive}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected twitter sensitive marker to block")
	}
	if hit.Category != safetyCategoryAdult || hit.Source != "platform_sensitive" {
		t.Fatalf("unexpected hit=%+v", hit)
	}
}

func TestSafetyBlockedSkipsTwitterSensitiveMarkerWhenDisabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyTwitterSensitive = false
	cfg.SafetyGlobalCategories = map[string]bool{}
	meta := mediaMeta{Platform: "twitter", AccessText: safetyMarkerTwitterSensitive}
	if hit, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatalf("did not expect twitter sensitive marker to block when disabled, hit=%+v", hit)
	}
}

func TestSafetyBlockedIgnoresTwitterSensitiveMarkerOnOtherPlatforms(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories = map[string]bool{}
	meta := mediaMeta{Platform: "bilibili", AccessText: safetyMarkerTwitterSensitive}
	if hit, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatalf("did not expect marker to block other platforms, hit=%+v", hit)
	}
}

func TestParseFxTwitterResponseMarksPossiblySensitive(t *testing.T) {
	info, err := parseFxTwitterResponse(map[string]any{
		"tweet": map[string]any{
			"text":               "probe text",
			"possibly_sensitive": true,
			"author": map[string]any{
				"name":        "Probe",
				"screen_name": "probe",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.SafetyText != safetyMarkerTwitterSensitive {
		t.Fatalf("safety marker=%q", info.SafetyText)
	}
}

func TestParseVxTwitterResponseMarksPossiblySensitive(t *testing.T) {
	info, err := parseVxTwitterResponse(map[string]any{
		"text":                   "probe text",
		"user_name":              "Probe",
		"user_screen_name":       "probe",
		"user_profile_image_url": "https://example.com/avatar.jpg",
		"date":                   "Mon Jun 01 14:31:43 +0000 2026",
		"possibly_sensitive":     true,
		"media_extended": []any{
			map[string]any{
				"type":          "video",
				"url":           "https://video.example.com/probe.mp4",
				"thumbnail_url": "https://example.com/thumb.jpg",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.SafetyText != safetyMarkerTwitterSensitive {
		t.Fatalf("safety marker=%q", info.SafetyText)
	}
	if len(info.Videos) != 1 || info.Videos[0].URL == "" || info.Cover == "" {
		t.Fatalf("unexpected videos=%+v cover=%q", info.Videos, info.Cover)
	}
}

func TestSafetyBlockedUsesPlatformCategoriesOnlyForPlatform(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyPlatformCategories["twitter"] = map[string]bool{safetyCategoryAd: true}
	meta := mediaMeta{Platform: "twitter", Desc: "dm for menu"}
	if _, blocked := safetyBlocked(cfg, meta, ""); !blocked {
		t.Fatal("expected twitter ad category to block")
	}
	meta.Platform = "bilibili"
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("did not expect platform-only category to block bilibili")
	}
}

func TestSafetyBlockedUsesCustomWords(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomCategories["custom_marketing"] = safetyCustomCategory{
		Label: "Marketing",
		Words: []string{"custom-secret"},
	}
	cfg.SafetyGlobalCategories["custom_marketing"] = true
	meta := mediaMeta{Platform: "xiaohongshu", Title: "this has CUSTOM-secret text"}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected custom word to block")
	}
	if hit.Source != "custom_category" {
		t.Fatalf("source=%s", hit.Source)
	}
}

func TestSafetyBlockedUsesBuiltinCategorySupplementWords(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories[safetyCategoryAdult] = true
	cfg.SafetyCustomGlobal[safetyCategoryAdult] = []string{"custom-adult-extra"}
	meta := mediaMeta{Platform: "twitter", Title: "this has custom-adult-extra text"}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected builtin category supplement word to block")
	}
	if hit.Category != safetyCategoryAdult || hit.Source != "builtin_custom" {
		t.Fatalf("unexpected hit=%+v", hit)
	}
	cfg.SafetyGlobalCategories[safetyCategoryAdult] = false
	if hit, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatalf("did not expect supplement word to block when category is disabled, hit=%+v", hit)
	}
}

func TestSafetyBlockedSupportsRegexCustomWords(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomCategories["custom_regex"] = safetyCustomCategory{
		Label: "Regex",
		Words: []string{`re:foo\s*bar`},
	}
	cfg.SafetyGlobalCategories["custom_regex"] = true
	meta := mediaMeta{Platform: "twitter", Title: "foo bar"}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked {
		t.Fatal("expected regex custom word to block")
	}
	if hit.Keyword != `re:foo\s*bar` {
		t.Fatalf("keyword=%q", hit.Keyword)
	}
}

func TestSafetyBlockedSupportsWildcardCustomWords(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomCategories["custom_wildcard"] = safetyCustomCategory{
		Label: "Wildcard",
		Words: []string{"foo*bar"},
	}
	cfg.SafetyGlobalCategories["custom_wildcard"] = true
	meta := mediaMeta{Platform: "twitter", Title: "foo---bar"}
	if _, blocked := safetyBlocked(cfg, meta, ""); !blocked {
		t.Fatal("expected wildcard custom word to block")
	}
}

func TestSafetyInvalidRegexDoesNotBlock(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomCategories["custom_bad_regex"] = safetyCustomCategory{
		Label: "Bad regex",
		Words: []string{`re:[`},
	}
	cfg.SafetyGlobalCategories["custom_bad_regex"] = true
	meta := mediaMeta{Platform: "twitter", Title: "anything"}
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("did not expect invalid regex to block")
	}
}

func TestSafetyBlockedUsesCustomCategoryOnPlatform(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomCategories["custom_adult_extra"] = safetyCustomCategory{
		Label: "成人扩展",
		Words: []string{"三连抽奖"},
	}
	cfg.SafetyPlatformCategories["twitter"] = map[string]bool{"custom_adult_extra": true}
	meta := mediaMeta{Platform: "twitter", Title: "关注三连抽奖"}
	if _, blocked := safetyBlocked(cfg, meta, ""); !blocked {
		t.Fatal("expected custom platform category to block twitter")
	}
	meta.Platform = "bilibili"
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("did not expect custom platform category to block bilibili")
	}
}

func TestSafetyBlockedUsesGlobalExcludes(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories[safetyCategoryAdult] = true
	cfg.SafetyExcludeGlobal[safetyCategoryAdult] = []string{"nsfw art contest"}
	meta := mediaMeta{Platform: "bilibili", Title: "NSFW art contest recap"}
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("expected global exclude to allow matching builtin word")
	}
}

func TestSafetyBlockedUsesPlatformExcludesOnlyForPlatform(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyPlatformCategories["twitter"] = map[string]bool{safetyCategoryAd: true}
	cfg.SafetyPlatformCategories["instagram"] = map[string]bool{safetyCategoryAd: true}
	cfg.SafetyExcludePlatform["twitter"] = map[string][]string{
		safetyCategoryAd: {"dm for menu archive"},
	}
	meta := mediaMeta{Platform: "twitter", Desc: "dm for menu archive"}
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("expected twitter platform exclude to allow matching platform category")
	}
	meta.Platform = "instagram"
	if _, blocked := safetyBlocked(cfg, meta, ""); !blocked {
		t.Fatal("expected platform exclude not to affect instagram")
	}
}

func TestNormalizeSafetyWordsDeduplicatesAndTrims(t *testing.T) {
	got := uniqueSafetyWords([]string{" #NSFW ", "nsfw", "", "R-18"})
	if len(got) != 2 {
		t.Fatalf("len=%d got=%v", len(got), got)
	}
	if containsString(got, "#NSFW") || !containsString(got, "NSFW") {
		t.Fatalf("expected leading hash to be stripped, got=%v", got)
	}
}

func TestDefaultSafetyCategoriesOnlyEnableGlobalPolitics(t *testing.T) {
	cfg := defaultConfig()
	if len(cfg.SafetyPlatformCategories) != 0 {
		t.Fatalf("expected no default platform categories, got=%v", cfg.SafetyPlatformCategories)
	}
	if len(cfg.SafetyGlobalCategories) != 1 || !cfg.SafetyGlobalCategories[safetyCategoryPolitics] {
		t.Fatalf("expected only global politics enabled, got=%v", cfg.SafetyGlobalCategories)
	}
}

func TestNormalizeSafetyCustomCategoriesCleansLegacyPlatformLabels(t *testing.T) {
	got := normalizeSafetyCustomCategories(map[string]safetyCustomCategory{
		"custom_adult":    {Label: "Instagram 自定义-黄推诈骗/导流", Words: []string{"#NSFW"}},
		"custom_politics": {Label: "tk 自定义-political_sensitive", Words: []string{"legacy"}},
	})
	if got["custom_adult"].Label != "自定义-色情" {
		t.Fatalf("adult label=%q", got["custom_adult"].Label)
	}
	if got["custom_politics"].Label != "自定义-政治" {
		t.Fatalf("politics label=%q", got["custom_politics"].Label)
	}
	if containsString(got["custom_adult"].Words, "#NSFW") || !containsString(got["custom_adult"].Words, "NSFW") {
		t.Fatalf("expected custom words without leading hash, got=%v", got["custom_adult"].Words)
	}
}

func TestNormalizeConfigMigratesLegacyCustomWords(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories["minor_risk"] = true
	cfg.SafetyGlobalCategories["custom_twitter_adult_scam"] = true
	cfg.SafetyPlatformCategories["twitter"] = map[string]bool{
		"adult_scam":                true,
		"custom_twitter_adult_scam": true,
	}
	cfg.SafetyCustomCategories["custom_twitter_adult_scam"] = safetyCustomCategory{
		Label: "Twitter legacy adult scam",
		Words: []string{"legacy twitter adult", "t.co"},
	}
	cfg.SafetyCustomGlobal = map[string][]string{
		"adult_scam": {"legacy adult ad"},
	}
	cfg.SafetyCustomPlatform = map[string]map[string][]string{
		"twitter": {"political_sensitive": {"legacy politics"}},
	}
	cfg.SafetyCustomSeedVersion = 0
	if changed := normalizeConfig(&cfg); !changed {
		t.Fatal("expected legacy migration to mark config changed")
	}
	if cfg.SafetyCustomSeedVersion != currentSafetyCustomSeedVersion {
		t.Fatalf("seed version=%d", cfg.SafetyCustomSeedVersion)
	}
	seedID := "custom_" + safetyCategoryAdult
	if len(cfg.SafetyCustomCategories[seedID].Words) == 0 {
		t.Fatal("expected legacy adult words")
	}
	for _, word := range cfg.SafetyCustomCategories[seedID].Words {
		if normalizeSafetyText(word) == "t co" {
			t.Fatal("expected broad t.co legacy word to be dropped")
		}
	}
	if _, ok := cfg.SafetyCustomCategories["custom_twitter_adult_scam"]; ok {
		t.Fatal("expected legacy platform custom category to be removed")
	}
	if cfg.SafetyGlobalCategories["minor_risk"] || cfg.SafetyPlatformCategories["twitter"]["adult_scam"] || cfg.SafetyPlatformCategories["twitter"]["custom_twitter_adult_scam"] {
		t.Fatal("expected legacy category switches to be migrated")
	}
	if !cfg.SafetyGlobalCategories[safetyCategoryAdult] || !cfg.SafetyGlobalCategories[seedID] || !cfg.SafetyPlatformCategories["twitter"][safetyCategoryAdult] || !cfg.SafetyPlatformCategories["twitter"][seedID] {
		t.Fatal("expected legacy category switches to enable migrated adult categories")
	}
	politicsID := "custom_" + safetyCategoryPolitics
	if len(cfg.SafetyCustomCategories[politicsID].Words) == 0 {
		t.Fatal("expected legacy platform politics words to become global custom category")
	}
	if cfg.SafetyCustomPlatform == nil || len(cfg.SafetyCustomPlatform) != 0 {
		t.Fatal("expected legacy platform custom words to be cleared")
	}
}

func TestSeedSafetyCustomWordsAddsTwitterAdultExtension(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyCustomSeedVersion = 0
	if changed := normalizeConfig(&cfg); !changed {
		t.Fatal("expected seed migration to mark config changed")
	}
	item := cfg.SafetyCustomCategories["custom_adult_extra"]
	if item.Label != "成人扩展" {
		t.Fatalf("label=%q", item.Label)
	}
	for _, want := range []string{"福利姬", "裸聊", "intercourse", "裏垢", "섹트"} {
		if !containsString(item.Words, want) {
			t.Fatalf("expected adult extension to contain %q", want)
		}
	}
	if cfg.SafetyGlobalCategories["custom_adult_extra"] {
		t.Fatal("did not expect adult extension to be enabled globally")
	}
	if !cfg.SafetyPlatformCategories["twitter"]["custom_adult_extra"] {
		t.Fatal("expected adult extension to be enabled on twitter")
	}
	meta := mediaMeta{Platform: "twitter", Title: "裸聊"}
	if _, blocked := safetyBlocked(cfg, meta, ""); !blocked {
		t.Fatal("expected twitter adult extension to block")
	}
	meta.Platform = "bilibili"
	if _, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatal("did not expect twitter adult extension to block bilibili")
	}
}

func TestSafetyMigrationDoesNotBlockNormalXTCoLinks(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyGlobalCategories["custom_twitter_adult_scam"] = true
	cfg.SafetyPlatformCategories["twitter"] = map[string]bool{
		"custom_twitter_adult_scam": true,
	}
	cfg.SafetyCustomCategories["custom_twitter_adult_scam"] = safetyCustomCategory{
		Label: "Twitter legacy adult scam",
		Words: []string{"t.co"},
	}
	if changed := normalizeConfig(&cfg); !changed {
		t.Fatal("expected legacy category migration")
	}
	meta := mediaMeta{
		Platform: "twitter",
		Title:    "Serena 的推文",
		Desc:     "Codex 中文内容创作者十大必装 Skills 来了 Github 链接 https://t.co/zncmfoVPun #codex #skill",
	}
	if hit, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatalf("expected normal X t.co link to pass, hit=%+v", hit)
	}
}

func TestSafetyNoticeTextUsesDefault(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyFilterNoticeText = ""
	got := safetyNoticeText(cfg, mediaMeta{}, safetyHit{})
	if got != "内容触发安全屏蔽，已停止解析。" {
		t.Fatalf("notice=%q", got)
	}
}

func TestSafetyNoticeTextSupportsTemplate(t *testing.T) {
	cfg := defaultConfig()
	cfg.SafetyFilterNoticeText = "已屏蔽 {platform}/{category}: {title}"
	meta := mediaMeta{Platform: "twitter", Title: "测试标题"}
	hit := safetyHit{Category: "adult"}
	got := safetyNoticeText(cfg, meta, hit)
	if got != "已屏蔽 twitter/adult: 测试标题" {
		t.Fatalf("notice=%q", got)
	}
}

func TestDecodeSafetyBuiltinWordsTrimsLeadingHash(t *testing.T) {
	got := decodeSafetyBuiltinWords([]string{"I05TRlc=", "I+eUt+iPqeiQqA=="})
	for _, word := range got {
		if strings.HasPrefix(word, "#") {
			t.Fatalf("expected leading hash to be trimmed, got %q in %v", word, got)
		}
	}
	if !containsString(got, "NSFW") || !containsString(got, "男菩萨") {
		t.Fatalf("expected decoded words without hash, got %v", got)
	}
}

func TestSafetyBuiltinWordsExcludeBroadDiscussionTerms(t *testing.T) {
	var adultWords, adWords, violenceWords []string
	for _, def := range safetyCategoryDefs {
		switch def.ID {
		case safetyCategoryAdult:
			adultWords = safetyBuiltinWords(def)
		case safetyCategoryAd:
			adWords = safetyBuiltinWords(def)
		case safetyCategoryViolence:
			violenceWords = safetyBuiltinWords(def)
		}
	}
	for _, word := range decodeSafetyBuiltinWords([]string{"5pOm6L65", "6buE5o6o", "56aP5Yip5aes", "56eB5oi/", "57qm54Ku"}) {
		if safetyWordsContain(normalizeSafetyText(word), adultWords) {
			t.Fatalf("adult builtin should not include broad discussion term %q", word)
		}
	}
	for _, word := range decodeSafetyBuiltinWords([]string{"5byV5rWB", "5pyA5paw5Zyw5Z2A", "6Ziy6LWw5aSx", "5aSH55So5Z+f5ZCN", "6Lez6L2s6ZO+5o6l"}) {
		if safetyWordsContain(normalizeSafetyText(word), adWords) {
			t.Fatalf("ad builtin should not include broad discussion term %q", word)
		}
	}
	for _, word := range decodeSafetyBuiltinWords([]string{"6KGA6IWl", "5rWB6KGA", "Z29yZQ==", "44Kw44Ot", "6rOg7Ja0", "7Jyg7ZiI"}) {
		if safetyWordsContain(normalizeSafetyText(word), violenceWords) {
			t.Fatalf("violence builtin should not include broad gore term %q", word)
		}
	}
	if !safetyWordsContain(normalizeSafetyText("https://t.me/example"), adWords) {
		t.Fatal("expected t.me links to remain in ad builtin words")
	}
}

func TestMediaShieldDefaultsDisabledWithActiveKeywords(t *testing.T) {
	cfg := defaultConfig()
	if cfg.MediaShieldEnabled {
		t.Fatalf("media shield should default off")
	}
	if !cfg.MediaShieldPassive || !cfg.MediaShieldActive {
		t.Fatalf("media shield passive/active defaults should be ready when enabled")
	}
	if !mediaShieldActiveTriggered(cfg, "https://x.com/i/status/1 setu") {
		t.Fatalf("expected default active keyword to trigger")
	}
}

func TestMediaShieldShouldHandleTwitterAdultOnlyWhenEnabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.MediaShieldPassiveWords = []string{"nsfw"}
	meta := mediaMeta{Platform: "twitter"}
	hit := safetyHit{Category: safetyCategoryAdult, Keyword: "nsfw", Source: "builtin"}
	if mediaShieldShouldHandle(cfg, meta, "", hit, true, 0, 0) {
		t.Fatalf("disabled media shield should not handle")
	}
	cfg.MediaShieldEnabled = true
	cfg.MediaShieldPrivateEnabled = true
	cfg.MediaShieldUserEnabled = map[int64]bool{456: true}
	if !mediaShieldShouldHandle(cfg, meta, "nsfw", hit, true, 0, 456) {
		t.Fatalf("enabled media shield should handle twitter adult hit with shield passive word")
	}
	meta.Platform = "bilibili"
	if mediaShieldShouldHandle(cfg, meta, "setu", hit, true, 0, 456) {
		t.Fatalf("media shield must stay twitter-only")
	}
}

func TestMediaShieldPassiveWordsAreIndependentFromSafety(t *testing.T) {
	cfg := defaultConfig()
	cfg.MediaShieldEnabled = true
	cfg.MediaShieldPrivateEnabled = true
	cfg.MediaShieldUserEnabled = map[int64]bool{456: true}
	cfg.MediaShieldPassiveWords = []string{"shield-only-word"}
	cfg.SafetyGlobalCategories = map[string]bool{}
	meta := mediaMeta{Platform: "twitter", Title: "shield-only-word"}
	if hit, blocked := safetyBlocked(cfg, meta, ""); blocked {
		t.Fatalf("safety should not block shield-only word, hit=%+v", hit)
	}
	if !mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 0, 456) {
		t.Fatalf("media shield should use its own passive words")
	}
	cfg.MediaShieldPassiveExcludes = []string{"shield-only-word"}
	if mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 0, 456) {
		t.Fatalf("media shield exclude words should suppress passive trigger")
	}
}

func TestMediaShieldSensitiveMarkerOnlyChecksRiskCategories(t *testing.T) {
	cfg := defaultConfig()
	cfg.MediaShieldEnabled = true
	cfg.MediaShieldPrivateEnabled = true
	cfg.MediaShieldUserEnabled = map[int64]bool{456: true}
	cfg.MediaShieldPassiveWords = []string{"shield-adult-word"}
	cfg.SafetyTwitterSensitive = true
	cfg.SafetyGlobalCategories = map[string]bool{safetyCategoryPolitics: true}
	meta := mediaMeta{
		Platform:   "twitter",
		AccessText: safetyMarkerTwitterSensitive,
		Title:      "regular title",
	}
	hit, blocked := safetyBlocked(cfg, meta, "")
	if !blocked || hit.Source != "platform_sensitive" {
		t.Fatalf("expected twitter sensitive safety hit, hit=%+v blocked=%v", hit, blocked)
	}
	if !mediaShieldShouldHandle(cfg, meta, "", hit, blocked, 0, 456) {
		t.Fatalf("sensitive marker should trigger shield without adult keyword")
	}

	meta.Title = "re:politics-risk-test"
	cfg.SafetyCustomCategories = map[string]safetyCustomCategory{
		"custom_politics": {Words: []string{"re:politics-risk-test"}},
	}
	cfg.SafetyGlobalCategories["custom_politics"] = true
	if mediaShieldShouldHandle(cfg, meta, "", hit, blocked, 0, 456) {
		t.Fatalf("politics risk should prevent shield takeover")
	}
}

func TestMediaShieldGroupSwitch(t *testing.T) {
	cfg := defaultConfig()
	cfg.MediaShieldEnabled = true
	cfg.MediaShieldPassiveWords = []string{"nsfw"}
	meta := mediaMeta{Platform: "twitter", Title: "nsfw"}
	if mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 123, 456) {
		t.Fatalf("group message should not trigger until the group is enabled")
	}
	cfg.MediaShieldGroupEnabled = map[int64]bool{123: true}
	if !mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 123, 456) {
		t.Fatalf("enabled group should trigger media shield")
	}
	if mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 0, 456) {
		t.Fatalf("private message should default off")
	}
	cfg.MediaShieldPrivateEnabled = true
	if mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 0, 456) {
		t.Fatalf("private message should require user whitelist")
	}
	cfg.MediaShieldUserEnabled = map[int64]bool{456: true}
	if !mediaShieldShouldHandle(cfg, meta, "", safetyHit{}, false, 0, 456) {
		t.Fatalf("private message should trigger only after private switch and user whitelist")
	}
}

func TestMediaShieldActiveTriggeredSupportsCustomKeywords(t *testing.T) {
	cfg := defaultConfig()
	cfg.MediaShieldKeywords = []string{"re:s[e3]tu", "大*奶"}
	if !mediaShieldActiveTriggered(cfg, "给我来点 s3tu") {
		t.Fatalf("expected regex active keyword to trigger")
	}
	if !mediaShieldActiveTriggered(cfg, "想看大大的奶") {
		t.Fatalf("expected wildcard active keyword to trigger")
	}
	if mediaShieldActiveTriggered(cfg, "普通 X 链接") {
		t.Fatalf("unexpected active trigger")
	}
}

func TestMediaShieldForwardSenderUsesOriginalSender(t *testing.T) {
	ctx := &zero.Ctx{Event: &zero.Event{
		SelfID: 10000,
		UserID: 12345,
		Sender: &zero.User{
			ID:       12345,
			NickName: "nickname",
			Card:     "group-card",
		},
	}}

	name, id := mediaShieldForwardSender(ctx)
	if name != "group-card" || id != 12345 {
		t.Fatalf("sender=%s(%d), want group-card(12345)", name, id)
	}
}

func TestMediaShieldPreviewForwardNodesIncludePreviewAndPassword(t *testing.T) {
	nodes := mediaShieldPreviewForwardNodes("sender", 12345, "password: 123456", "file:///host/card.png", true, "smirk")
	if len(nodes) != 3 {
		t.Fatalf("nodes=%d, want 3", len(nodes))
	}
	wantTypes := []string{"text", "image", "text"}
	for i, want := range wantTypes {
		data, ok := nodes[i]["data"].(map[string]any)
		if !ok {
			t.Fatalf("node %d data has unexpected type %T", i, nodes[i]["data"])
		}
		content, ok := data["content"].([]map[string]any)
		if !ok || len(content) != 1 {
			t.Fatalf("node %d content=%T len=%d", i, data["content"], len(content))
		}
		if content[0]["type"] != want {
			t.Fatalf("node %d type=%v, want %s", i, content[0]["type"], want)
		}
	}
}

func TestMediaShieldPreviewForwardNodesContainNoFileNode(t *testing.T) {
	nodes := mediaShieldPreviewForwardNodes("sender", 12345, "password: 123456", "", false, "")
	if len(nodes) != 1 {
		t.Fatalf("nodes=%d, want 1", len(nodes))
	}
	data, ok := nodes[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("node data has unexpected type %T", nodes[0]["data"])
	}
	content, ok := data["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content=%T len=%d", data["content"], len(content))
	}
	if content[0]["type"] == "file" {
		t.Fatalf("preview forward should not contain file node")
	}
	if content[0]["type"] != "text" {
		t.Fatalf("content type=%v, want text", content[0]["type"])
	}
}

func TestMediaShieldForwardMayStillCompleteOnlyForLongEmptyResponse(t *testing.T) {
	if !mediaShieldForwardMayStillComplete(zero.APIResponse{}, 25*time.Second) {
		t.Fatalf("long empty response should be treated as pending")
	}
	if mediaShieldForwardMayStillComplete(zero.APIResponse{}, time.Second) {
		t.Fatalf("short empty response should not be treated as pending")
	}
	if mediaShieldForwardMayStillComplete(zero.APIResponse{Status: "failed"}, 25*time.Second) {
		t.Fatalf("failed response should not be treated as pending")
	}
	if !mediaShieldForwardMayStillComplete(zero.APIResponse{Status: "failed", Message: "context deadline exceeded"}, 25*time.Second) {
		t.Fatalf("long timeout-like forward failure should be treated as pending")
	}
	if mediaShieldForwardMayStillComplete(zero.APIResponse{Status: "failed", Message: "permission denied"}, 25*time.Second) {
		t.Fatalf("non-timeout forward failure should not be treated as pending")
	}
}

func TestCreateMediaShieldZipRequiresPassword(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "media.txt")
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "media.zip")
	if err := createMediaShieldZip([]string{src}, out, "123456"); err != nil {
		t.Fatalf("create zip: %v", err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		t.Fatalf("zip files=%d", len(r.File))
	}
	if !r.File[0].IsEncrypted() {
		t.Fatalf("zip entry should be encrypted")
	}
	r.File[0].SetPassword("123456")
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatalf("open encrypted entry: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read encrypted entry: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("zip content=%q", data)
	}
}

func TestCardDisplayTitleUsesDescFirstLine(t *testing.T) {
	title := cardDisplayTitle(mediaMeta{
		Platform: "weibo",
		Desc:     "\n正文第一行\n正文第二行",
	}, "gallery")
	if title != "正文第一行" {
		t.Fatalf("title=%q, want desc first line", title)
	}
	if strings.Contains(title, "媒体解析") {
		t.Fatalf("title should not use generic fallback: %q", title)
	}
}

func TestCardDisplayTitlePlatformFallback(t *testing.T) {
	tests := []struct {
		name string
		meta mediaMeta
		kind string
		want string
	}{
		{name: "weibo gallery", meta: mediaMeta{Platform: "weibo"}, kind: "gallery", want: "微博图文"},
		{name: "bilibili video", meta: mediaMeta{Platform: "bilibili"}, kind: "video", want: "B站视频"},
		{name: "unknown gallery", meta: mediaMeta{Platform: "unknown"}, kind: "gallery", want: "图文内容"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cardDisplayTitle(tt.meta, tt.kind); got != tt.want {
				t.Fatalf("title=%q, want %q", got, tt.want)
			}
		})
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
