// Package mediaparser ports astrbot_plugin_media_parser to ZeroBot.
package mediaparser

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	ctrl "github.com/FloatTech/zbpctrl"
	"github.com/FloatTech/zbputils/control"
	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	defaultMaxVideoMB = 1000
	defaultTTLMinutes = 60
	defaultTimeoutSec = 180

	accessNone      = "none"
	accessBlacklist = "blacklist"
	accessWhitelist = "whitelist"

	outputOff      = "off"
	outputAll      = "all"
	outputTextOnly = "text"
	outputRichOnly = "rich"
)

var (
	engine = control.AutoRegister(&ctrl.Options[*zero.Ctx]{
		DisableOnDefault: false,
		Brief:            "媒体链接解析",
		Help: "自动解析 B站/抖音/TikTok/快手/微博/小红书/闲鱼/头条/小黑盒/Twitter 链接。\n" +
			"- /媒体解析状态\n" +
			"- /媒体解析 开启|关闭\n" +
			"- /媒体解析 调试 开启|关闭\n" +
			"- /媒体解析 名单模式 关闭|黑名单|白名单\n" +
			"- /媒体解析 黑名单|白名单 用户|群 添加|删除 ID\n" +
			"- /媒体解析 平台 bilibili 开启|关闭\n" +
			"- /媒体解析 输出 bilibili 全部|文本|媒体|关闭\n" +
			"- /媒体解析 b站cookie 设置 COOKIE\n" +
			"- /媒体解析 画质 不限制|360p|720p|1080p\n" +
			"- /媒体解析 备用ytdlp 开启|关闭\n" +
			"- /媒体解析 清缓存",
		PrivateDataFolder: "mediaparser",
	})

	urlRE       = regexp.MustCompile(`https?://[^\s<>"'\]\)）】》、，。；;]+`)
	originalRE  = regexp.MustCompile(`(?i)原始链接\s*[:：]\s*https?://`)
	stateMu     sync.RWMutex
	currentConf config
	configPath  string
	cacheDir    string
	client      = &http.Client{Timeout: 45 * time.Second}
	limitMu     sync.Mutex
	lastParseAt = map[int64]time.Time{}
	runtimeMu   sync.RWMutex
	runtimeInfo = RuntimeStatus{StartedAt: time.Now()}
)

type config struct {
	AutoParse           bool                      `json:"auto_parse"`
	Keywords            []string                  `json:"keywords"`
	AdminID             int64                     `json:"admin_id"`
	AccessMode          string                    `json:"access_mode"`
	PrivateAccessMode   string                    `json:"private_access_mode"`
	GroupAccessMode     string                    `json:"group_access_mode"`
	GroupUserAccessMode string                    `json:"group_user_access_mode"`
	UserBlacklist       map[int64]bool            `json:"user_blacklist"`
	GroupBlacklist      map[int64]bool            `json:"group_blacklist"`
	GroupUserBlacklist  map[int64]bool            `json:"group_user_blacklist"`
	UserWhitelist       map[int64]bool            `json:"user_whitelist"`
	GroupWhitelist      map[int64]bool            `json:"group_whitelist"`
	GroupUserWhitelist  map[int64]bool            `json:"group_user_whitelist"`
	WhitelistMode       bool                      `json:"whitelist_mode"`
	PlatformEnabled     map[string]bool           `json:"platform_enabled"`
	PlatformInfoCard    map[string]bool           `json:"platform_info_card"`
	PlatformSendMedia   map[string]bool           `json:"platform_send_media"`
	PlatformDownload    map[string]bool           `json:"platform_download_video"`
	PlatformGroupBlock  map[string]map[int64]bool `json:"platform_group_block"`
	OutputMode          map[string]string         `json:"output_mode"`
	SendInfoCard        bool                      `json:"send_info_card"`
	SendMedia           bool                      `json:"send_media"`
	DownloadVideo       bool                      `json:"download_video"`
	MaxVideoMB          int64                     `json:"max_video_mb"`
	VideoMaxResolution  int                       `json:"video_max_resolution"`
	CacheTTLMinutes     int                       `json:"cache_ttl_minutes"`
	TimeoutSeconds      int                       `json:"timeout_seconds"`
	Proxy               string                    `json:"proxy"`
	Debug               bool                      `json:"debug"`
	ParseReaction       bool                      `json:"parse_reaction"`
	ParseReactionEmoji  string                    `json:"parse_reaction_emoji"`
	FailReactionEmoji   string                    `json:"fail_reaction_emoji"`

	BilibiliUseCookie  bool   `json:"bilibili_use_cookie"`
	BilibiliCookie     string `json:"bilibili_cookie"`
	BilibiliMaxQuality string `json:"bilibili_max_quality"`
	XiaohongshuCookie  string `json:"xiaohongshu_cookie"`
	YouTubeCookie      string `json:"youtube_cookie"`
	InstagramCookie    string `json:"instagram_cookie"`
	KeylolCookie       string `json:"keylol_cookie"`
	AvoidAV1           bool   `json:"avoid_av1"`

	UseYTDLPFallback     bool   `json:"use_yt_dlp_fallback"`
	YTDLPPath            string `json:"yt_dlp_path"`
	YTDLPCookieFile      string `json:"yt_dlp_cookie_file"`
	YouTubeCookieFile    string `json:"youtube_cookie_file"`
	InstagramCookieFile  string `json:"instagram_cookie_file"`
	YouTubeExtractorArgs string `json:"youtube_extractor_args"`
}

type mediaMeta struct {
	URL          string
	SourceURL    string
	Platform     string
	Title        string
	Author       string
	Avatar       string
	Timestamp    string
	Desc         string
	Cover        string
	VideoURLs    [][]string
	ImageURLs    [][]string
	VideoHeads   map[string]string
	ImageHeads   map[string]string
	ForceLocal   bool
	AccessText   string
	Error        string
	KeylolBlocks []keylolBlock

	FilePaths        []string
	VideoSizes       []float64
	VideoModes       []string
	ImageModes       []string
	VideoSkipReasons []string
	ImageSkipReasons []string
	HasValidMedia    bool
	HasAccessDenied  bool
	ExceedsMaxSize   bool
	MediaItems       []mediaItem
}

type mediaItem struct {
	Kind  string
	Index int
}

type platform struct {
	Name    string
	Hosts   []string
	Aliases []string
}

type RuntimeStatus struct {
	StartedAt    time.Time `json:"started_at"`
	LastSelfID   int64     `json:"last_self_id"`
	LastUserID   int64     `json:"last_user_id"`
	LastGroupID  int64     `json:"last_group_id"`
	LastMessage  string    `json:"last_message"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	ParseSuccess int64     `json:"parse_success"`
	ParseFailed  int64     `json:"parse_failed"`
}

var platforms = []platform{
	{Name: "bilibili", Hosts: []string{"bilibili.com", "b23.tv", "bili2233.cn", "acg.tv"}, Aliases: []string{"b站", "哔哩哔哩", "bili"}},
	{Name: "douyin", Hosts: []string{"douyin.com", "iesdouyin.com", "v.douyin.com"}, Aliases: []string{"抖音"}},
	{Name: "tiktok", Hosts: []string{"tiktok.com", "vm.tiktok.com", "vt.tiktok.com"}, Aliases: []string{"tk"}},
	{Name: "kuaishou", Hosts: []string{"kuaishou.com", "gifshow.com", "chenzhongtech.com", "v.kuaishou.com"}, Aliases: []string{"快手", "ks"}},
	{Name: "weibo", Hosts: []string{"weibo.com", "weibo.cn", "m.weibo.cn", "video.weibo.com"}, Aliases: []string{"微博"}},
	{Name: "xiaohongshu", Hosts: []string{"xiaohongshu.com", "xhslink.com"}, Aliases: []string{"小红书", "xhs"}},
	{Name: "xianyu", Hosts: []string{"goofish.com", "2.taobao.com", "market.m.taobao.com", "m.tb.cn"}, Aliases: []string{"闲鱼"}},
	{Name: "acfun", Hosts: []string{"acfun.cn", "m.acfun.cn"}, Aliases: []string{"A站", "ac"}},
	{Name: "youtube", Hosts: []string{"youtube.com", "youtu.be", "m.youtube.com", "music.youtube.com"}, Aliases: []string{"YouTube", "yt"}},
	{Name: "instagram", Hosts: []string{"instagram.com", "www.instagram.com"}, Aliases: []string{"Instagram", "ig"}},
	{Name: "toutiao", Hosts: []string{"toutiao.com", "toutiaoimg.com", "snssdk.com"}, Aliases: []string{"头条"}},
	{Name: "xiaoheihe", Hosts: []string{"xiaoheihe.cn", "heybox.cn"}, Aliases: []string{"小黑盒", "heybox"}},
	{Name: "twitter", Hosts: []string{"twitter.com", "x.com", "fxtwitter.com", "fixupx.com", "vxtwitter.com"}, Aliases: []string{"推特", "x"}},
	{Name: "keylol", Hosts: []string{"keylol.com", "www.keylol.com"}, Aliases: []string{"Keylol", "keylol"}},
}

func init() {
	configPath = filepath.Join(engine.DataFolder(), "config.json")
	cacheDir = filepath.Join(engine.DataFolder(), "cache")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		logrus.Errorln("[mediaparser] create cache:", err)
	}
	if err := loadConfig(); err != nil {
		logrus.Errorln("[mediaparser] load config:", err)
	}

	zero.OnCommand("媒体解析", zero.AdminPermission).SetBlock(true).Handle(handleCommand)
	zero.OnFullMatch("媒体解析状态", zero.AdminPermission).SetBlock(true).Handle(func(ctx *zero.Ctx) {
		ctx.SendChain(message.Text(formatStatus()))
	})
	zero.OnMessage().SetBlock(false).Handle(handleAutoParse)
}

func defaultConfig() config {
	enabled := make(map[string]bool, len(platforms))
	infoCard := make(map[string]bool, len(platforms))
	sendMedia := make(map[string]bool, len(platforms))
	download := make(map[string]bool, len(platforms))
	platformGroupBlock := make(map[string]map[int64]bool, len(platforms))
	output := make(map[string]string, len(platforms))
	for _, p := range platforms {
		enabled[p.Name] = true
		infoCard[p.Name] = true
		sendMedia[p.Name] = true
		download[p.Name] = true
		platformGroupBlock[p.Name] = map[int64]bool{}
		output[p.Name] = outputAll
	}
	return config{
		AutoParse:            true,
		Keywords:             []string{"视频解析", "解析视频", "解析", "parse"},
		AdminID:              10000,
		AccessMode:           accessNone,
		PrivateAccessMode:    accessNone,
		GroupAccessMode:      accessNone,
		GroupUserAccessMode:  accessNone,
		UserBlacklist:        map[int64]bool{},
		GroupBlacklist:       map[int64]bool{},
		GroupUserBlacklist:   map[int64]bool{},
		UserWhitelist:        map[int64]bool{},
		GroupWhitelist:       map[int64]bool{},
		GroupUserWhitelist:   map[int64]bool{},
		PlatformEnabled:      enabled,
		PlatformInfoCard:     infoCard,
		PlatformSendMedia:    sendMedia,
		PlatformDownload:     download,
		PlatformGroupBlock:   platformGroupBlock,
		OutputMode:           output,
		SendInfoCard:         true,
		SendMedia:            true,
		DownloadVideo:        true,
		MaxVideoMB:           defaultMaxVideoMB,
		VideoMaxResolution:   0,
		CacheTTLMinutes:      defaultTTLMinutes,
		TimeoutSeconds:       defaultTimeoutSec,
		Debug:                true,
		ParseReaction:        true,
		ParseReactionEmoji:   "🍉",
		FailReactionEmoji:    "❌",
		AvoidAV1:             true,
		BilibiliMaxQuality:   "不限制",
		UseYTDLPFallback:     false,
		YTDLPPath:            "yt-dlp",
		YouTubeExtractorArgs: "youtube:player_client=default,android;formats=missing_pot",
	}
}

func loadConfig() error {
	cfg := defaultConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			currentConf = cfg
			return saveConfig()
		}
		return err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	normalizeConfig(&cfg)
	stateMu.Lock()
	currentConf = cfg
	stateMu.Unlock()
	return nil
}

func normalizeConfig(cfg *config) {
	if cfg.UserBlacklist == nil {
		cfg.UserBlacklist = map[int64]bool{}
	}
	if cfg.GroupBlacklist == nil {
		cfg.GroupBlacklist = map[int64]bool{}
	}
	if cfg.GroupUserBlacklist == nil {
		cfg.GroupUserBlacklist = map[int64]bool{}
	}
	if cfg.UserWhitelist == nil {
		cfg.UserWhitelist = map[int64]bool{}
	}
	if cfg.GroupWhitelist == nil {
		cfg.GroupWhitelist = map[int64]bool{}
	}
	if cfg.GroupUserWhitelist == nil {
		cfg.GroupUserWhitelist = map[int64]bool{}
	}
	if cfg.PlatformEnabled == nil {
		cfg.PlatformEnabled = map[string]bool{}
	}
	if cfg.PlatformInfoCard == nil {
		cfg.PlatformInfoCard = map[string]bool{}
	}
	if cfg.PlatformSendMedia == nil {
		cfg.PlatformSendMedia = map[string]bool{}
	}
	if cfg.PlatformDownload == nil {
		cfg.PlatformDownload = map[string]bool{}
	}
	if cfg.PlatformGroupBlock == nil {
		cfg.PlatformGroupBlock = map[string]map[int64]bool{}
	}
	if cfg.OutputMode == nil {
		cfg.OutputMode = map[string]string{}
	}
	for _, p := range platforms {
		if _, ok := cfg.PlatformEnabled[p.Name]; !ok {
			cfg.PlatformEnabled[p.Name] = true
		}
		if _, ok := cfg.PlatformInfoCard[p.Name]; !ok {
			cfg.PlatformInfoCard[p.Name] = true
		}
		if _, ok := cfg.PlatformSendMedia[p.Name]; !ok {
			cfg.PlatformSendMedia[p.Name] = true
		}
		if _, ok := cfg.PlatformDownload[p.Name]; !ok {
			cfg.PlatformDownload[p.Name] = true
		}
		if cfg.PlatformGroupBlock[p.Name] == nil {
			cfg.PlatformGroupBlock[p.Name] = map[int64]bool{}
		}
		if _, ok := cfg.OutputMode[p.Name]; !ok {
			cfg.OutputMode[p.Name] = outputAll
		}
	}
	if cfg.AccessMode == "" {
		if cfg.WhitelistMode {
			cfg.AccessMode = accessWhitelist
		} else {
			cfg.AccessMode = accessNone
		}
	}
	if cfg.AccessMode != accessNone && cfg.AccessMode != accessBlacklist && cfg.AccessMode != accessWhitelist {
		cfg.AccessMode = accessNone
	}
	if cfg.PrivateAccessMode == "" {
		cfg.PrivateAccessMode = cfg.AccessMode
	}
	if cfg.GroupAccessMode == "" {
		cfg.GroupAccessMode = cfg.AccessMode
	}
	if cfg.GroupUserAccessMode == "" {
		cfg.GroupUserAccessMode = accessNone
	}
	if !validAccessMode(cfg.PrivateAccessMode) {
		cfg.PrivateAccessMode = accessNone
	}
	if !validAccessMode(cfg.GroupAccessMode) {
		cfg.GroupAccessMode = accessNone
	}
	if !validAccessMode(cfg.GroupUserAccessMode) {
		cfg.GroupUserAccessMode = accessNone
	}
	if cfg.PrivateAccessMode == cfg.GroupAccessMode {
		cfg.AccessMode = cfg.PrivateAccessMode
	}
	cfg.WhitelistMode = cfg.PrivateAccessMode == accessWhitelist
	if cfg.MaxVideoMB <= 0 {
		cfg.MaxVideoMB = defaultMaxVideoMB
	}
	if cfg.VideoMaxResolution != 0 && cfg.VideoMaxResolution != 360 && cfg.VideoMaxResolution != 720 && cfg.VideoMaxResolution != 1080 {
		cfg.VideoMaxResolution = 0
	}
	if cfg.CacheTTLMinutes <= 0 {
		cfg.CacheTTLMinutes = defaultTTLMinutes
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultTimeoutSec
	}
	if len(cfg.Keywords) == 0 {
		cfg.Keywords = []string{"视频解析", "解析视频", "解析", "parse"}
	}
	if cfg.YTDLPPath == "" {
		cfg.YTDLPPath = "yt-dlp"
	}
	if strings.TrimSpace(cfg.YouTubeExtractorArgs) == "" {
		cfg.YouTubeExtractorArgs = "youtube:player_client=default,android;formats=missing_pot"
	}
	cfg.YTDLPCookieFile = strings.TrimSpace(cfg.YTDLPCookieFile)
	cfg.YouTubeCookieFile = strings.TrimSpace(cfg.YouTubeCookieFile)
	cfg.InstagramCookieFile = strings.TrimSpace(cfg.InstagramCookieFile)
	cfg.XiaohongshuCookie = strings.TrimSpace(cfg.XiaohongshuCookie)
	cfg.YouTubeCookie = strings.TrimSpace(cfg.YouTubeCookie)
	cfg.InstagramCookie = strings.TrimSpace(cfg.InstagramCookie)
	cfg.KeylolCookie = strings.TrimSpace(cfg.KeylolCookie)
	if strings.TrimSpace(cfg.ParseReactionEmoji) == "" {
		cfg.ParseReactionEmoji = "🍉"
	}
	if strings.TrimSpace(cfg.FailReactionEmoji) == "" {
		cfg.FailReactionEmoji = "❌"
	}
	cfg.BilibiliMaxQuality = biliQualityFromResolution(cfg.VideoMaxResolution)
}

func saveConfig() error {
	stateMu.RLock()
	cfg := currentConf
	stateMu.RUnlock()
	normalizeConfig(&cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func saveConfigLocked() error {
	normalizeConfig(&currentConf)
	data, err := json.MarshalIndent(currentConf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func snapshotConfig() config {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return currentConf
}

func handleCommand(ctx *zero.Ctx) {
	args := strings.Fields(strings.TrimSpace(ctx.State["args"].(string)))
	if len(args) == 0 || args[0] == "状态" {
		ctx.SendChain(message.Text(formatStatus()))
		return
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	switch args[0] {
	case "开启", "启用", "打开", "on":
		currentConf.AutoParse = true
	case "关闭", "禁用", "off":
		currentConf.AutoParse = false
	case "调试", "debug":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 调试 开启|关闭"))
			return
		}
		currentConf.Debug = isOn(args[1])
	case "pmode", "private_mode":
		if len(args) < 2 {
			ctx.SendChain(message.Text("usage: /媒体解析 pmode none|blacklist|whitelist"))
			return
		}
		mode, ok := parseAccessMode(args[1])
		if !ok {
			ctx.SendChain(message.Text("pmode must be none / blacklist / whitelist"))
			return
		}
		currentConf.PrivateAccessMode = mode
	case "gmode", "group_mode":
		if len(args) < 2 {
			ctx.SendChain(message.Text("usage: /媒体解析 gmode none|blacklist|whitelist"))
			return
		}
		mode, ok := parseAccessMode(args[1])
		if !ok {
			ctx.SendChain(message.Text("gmode must be none / blacklist / whitelist"))
			return
		}
		currentConf.GroupAccessMode = mode
	case "gumode", "group_user_mode":
		if len(args) < 2 {
			ctx.SendChain(message.Text("usage: /媒体解析 gumode none|blacklist|whitelist"))
			return
		}
		mode, ok := parseAccessMode(args[1])
		if !ok {
			ctx.SendChain(message.Text("gumode must be none / blacklist / whitelist"))
			return
		}
		currentConf.GroupUserAccessMode = mode
	case "名单模式", "模式":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 名单模式 关闭|黑名单|白名单"))
			return
		}
		mode, ok := parseAccessMode(args[1])
		if !ok {
			ctx.SendChain(message.Text("名单模式只能是: 关闭 / 黑名单 / 白名单"))
			return
		}
		currentConf.AccessMode = mode
		currentConf.PrivateAccessMode = mode
		currentConf.GroupAccessMode = mode
	case "黑名单", "白名单":
		if len(args) == 2 && isModeWord(args[1]) {
			mode, _ := parseAccessMode(args[0])
			if isOn(args[1]) {
				currentConf.AccessMode = mode
				currentConf.PrivateAccessMode = mode
				currentConf.GroupAccessMode = mode
			} else if currentConf.AccessMode == mode {
				currentConf.AccessMode = accessNone
				currentConf.PrivateAccessMode = accessNone
				currentConf.GroupAccessMode = accessNone
			}
			break
		}
		if len(args) < 4 {
			ctx.SendChain(message.Text("用法: /媒体解析 黑名单|白名单 用户|群|群成员 添加|删除 ID"))
			return
		}
		id, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil {
			ctx.SendChain(message.Text("ID 格式不正确"))
			return
		}
		setAccessList(args[0], args[1], args[2], id)
	case "平台":
		if len(args) < 3 {
			ctx.SendChain(message.Text("用法: /媒体解析 平台 bilibili 开启|关闭"))
			return
		}
		name := normalizePlatformName(args[1])
		if name == "" {
			ctx.SendChain(message.Text("未知平台: ", args[1]))
			return
		}
		currentConf.PlatformEnabled[name] = isOn(args[2])
	case "card", "info_card":
		if len(args) < 3 {
			ctx.SendChain(message.Text("usage: /媒体解析 card bilibili on|off"))
			return
		}
		name := normalizePlatformName(args[1])
		if name == "" {
			ctx.SendChain(message.Text("未知平台: ", args[1]))
			return
		}
		currentConf.PlatformInfoCard[name] = isOn(args[2])
	case "media_send", "send_media":
		if len(args) < 3 {
			ctx.SendChain(message.Text("usage: /媒体解析 media_send bilibili on|off"))
			return
		}
		name := normalizePlatformName(args[1])
		if name == "" {
			ctx.SendChain(message.Text("未知平台: ", args[1]))
			return
		}
		currentConf.PlatformSendMedia[name] = isOn(args[2])
	case "download_platform", "platform_download":
		if len(args) < 3 {
			ctx.SendChain(message.Text("usage: /媒体解析 download_platform bilibili on|off"))
			return
		}
		name := normalizePlatformName(args[1])
		if name == "" {
			ctx.SendChain(message.Text("未知平台: ", args[1]))
			return
		}
		currentConf.PlatformDownload[name] = isOn(args[2])
	case "输出":
		if len(args) < 3 {
			ctx.SendChain(message.Text("用法: /媒体解析 输出 bilibili 全部|文本|媒体|关闭"))
			return
		}
		name := normalizePlatformName(args[1])
		if name == "" {
			ctx.SendChain(message.Text("未知平台: ", args[1]))
			return
		}
		mode, ok := parseOutputMode(args[2])
		if !ok {
			ctx.SendChain(message.Text("输出模式只能是: 全部 / 文本 / 媒体 / 关闭"))
			return
		}
		currentConf.OutputMode[name] = mode
		currentConf.PlatformEnabled[name] = mode != outputOff
	case "信息图":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 信息图 开启|关闭"))
			return
		}
		currentConf.SendInfoCard = isOn(args[1])
	case "媒体":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 媒体 开启|关闭"))
			return
		}
		currentConf.SendMedia = isOn(args[1])
	case "下载":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 下载 开启|关闭"))
			return
		}
		currentConf.DownloadVideo = isOn(args[1])
	case "备用ytdlp", "ytdlp":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 备用ytdlp 开启|关闭"))
			return
		}
		currentConf.UseYTDLPFallback = isOn(args[1])
	case "b站cookie", "bilibili_cookie":
		if len(args) < 3 || args[1] != "设置" {
			ctx.SendChain(message.Text("用法: /媒体解析 b站cookie 设置 COOKIE"))
			return
		}
		currentConf.BilibiliUseCookie = true
		currentConf.BilibiliCookie = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ctx.State["args"].(string)), args[0]+" "+args[1]))
	case "b站画质", "bilibili_quality":
		if len(args) < 2 {
			ctx.SendChain(message.Text("用法: /媒体解析 画质 不限制|360p|720p|1080p"))
			return
		}
		res, ok := parseVideoResolution(args[1])
		if !ok {
			ctx.SendChain(message.Text("画质只能是 不限制 / 360p / 720p / 1080p"))
			return
		}
		currentConf.VideoMaxResolution = res
	case "画质", "resolution", "quality", "video_quality", "res":
		if len(args) < 2 {
			ctx.SendChain(message.Text("usage: /媒体解析 resolution unlimited|360p|720p|1080p"))
			return
		}
		res, ok := parseVideoResolution(args[1])
		if !ok {
			ctx.SendChain(message.Text("resolution must be unlimited / 360p / 720p / 1080p"))
			return
		}
		currentConf.VideoMaxResolution = res
	case "清缓存":
		stateMu.Unlock()
		n, err := cleanCache()
		stateMu.Lock()
		if err != nil {
			ctx.SendChain(message.Text("清理失败: ", err))
			return
		}
		ctx.SendChain(message.Text("已清理缓存文件 ", n, " 个"))
		return
	default:
		ctx.SendChain(message.Text("未知命令，发送 /用法 mediaparser 查看帮助"))
		return
	}

	if err := saveConfigLocked(); err != nil {
		ctx.SendChain(message.Text("保存配置失败: ", err))
		return
	}
	ctx.SendChain(message.Text("媒体解析配置已更新\n", formatStatusLocked()))
}

func handleAutoParse(ctx *zero.Ctx) {
	cfg := snapshotConfig()
	raw := ctx.Event.RawMessage
	updateRuntimeMessage(ctx.Event.SelfID, ctx.Event.UserID, ctx.Event.GroupID, raw)
	logDebug(cfg, "message user=%d group=%d auto=%v private_access=%s group_access=%s text=%q", ctx.Event.UserID, ctx.Event.GroupID, cfg.AutoParse, cfg.PrivateAccessMode, cfg.GroupAccessMode, truncate(raw, 160))
	if !cfg.AutoParse && !hasKeyword(raw, cfg.Keywords) {
		logDebug(cfg, "skip auto_parse_off_no_keyword")
		return
	}
	if originalRE.MatchString(raw) {
		logDebug(cfg, "skip original_link_echo")
		return
	}
	if ok, reason := permissionOK(cfg, ctx.Event.UserID, ctx.Event.GroupID); !ok {
		logDebug(cfg, "skip permission reason=%s", reason)
		return
	}
	links := extractLinks(raw, cfg)
	if len(links) == 0 {
		logDebug(cfg, "skip no_supported_link")
		return
	}
	if rateLimited(ctx.Event.UserID, ctx.Event.GroupID) {
		logDebug(cfg, "skip rate_limited")
		return
	}
	logrus.Infof("[mediaparser] event user=%d group=%d links=%d raw=%q", ctx.Event.UserID, ctx.Event.GroupID, len(links), truncate(raw, 160))
	activeLinks := make([]parsedLink, 0, len(links))
	for _, link := range links {
		if platformGroupBlocked(cfg, link.Platform, ctx.Event.GroupID) {
			logDebug(cfg, "skip platform_group_blocked platform=%s group=%d", link.Platform, ctx.Event.GroupID)
			continue
		}
		activeLinks = append(activeLinks, link)
	}
	if len(activeLinks) == 0 {
		return
	}
	sendParseReaction(ctx, cfg)
	for _, link := range activeLinks {
		if err := processLink(ctx, cfg, link); err != nil {
			addRuntimeParse(false)
			sendFailReaction(ctx, cfg)
			logrus.Warnf("[mediaparser] parse_failed platform=%s url=%s error=%v", link.Platform, link.URL, err)
		} else {
			addRuntimeParse(true)
		}
	}
}

func platformGroupBlocked(cfg config, platform string, groupID int64) bool {
	if groupID == 0 || platform == "" {
		return false
	}
	blocked := cfg.PlatformGroupBlock[platform]
	return blocked != nil && blocked[groupID]
}

func sendParseReaction(ctx *zero.Ctx, cfg config) {
	if !cfg.ParseReaction || ctx.Event.MessageID == 0 {
		return
	}
	emoji := firstReactionRune(cfg.ParseReactionEmoji)
	if emoji == 0 {
		return
	}
	if err := ctx.SetMessageEmojiLike(ctx.Event.MessageID, emoji); err != nil {
		logDebug(cfg, "parse_reaction_failed message_id=%d emoji=%q error=%v", ctx.Event.MessageID, string(emoji), err)
		return
	}
	logDebug(cfg, "parse_reaction_ok message_id=%d emoji=%q", ctx.Event.MessageID, string(emoji))
}

func sendFailReaction(ctx *zero.Ctx, cfg config) {
	if !cfg.ParseReaction || ctx.Event.MessageID == 0 {
		return
	}
	emoji := firstReactionRune(cfg.FailReactionEmoji)
	if emoji == 0 {
		return
	}
	if err := ctx.SetMessageEmojiLike(ctx.Event.MessageID, emoji); err != nil {
		logDebug(cfg, "fail_reaction_failed message_id=%d emoji=%q error=%v", ctx.Event.MessageID, string(emoji), err)
		return
	}
	logDebug(cfg, "fail_reaction_ok message_id=%d emoji=%q", ctx.Event.MessageID, string(emoji))
}

func firstReactionRune(s string) rune {
	for _, r := range strings.TrimSpace(s) {
		if r == 0xfe0e || r == 0xfe0f {
			continue
		}
		return r
	}
	return 0
}

type parsedLink struct {
	URL      string
	Platform string
}

func updateRuntimeMessage(selfID, userID, groupID int64, text string) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	runtimeInfo.LastSelfID = selfID
	runtimeInfo.LastUserID = userID
	runtimeInfo.LastGroupID = groupID
	runtimeInfo.LastMessage = truncate(text, 240)
	runtimeInfo.LastSeenAt = time.Now()
}

func addRuntimeParse(ok bool) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if ok {
		runtimeInfo.ParseSuccess++
	} else {
		runtimeInfo.ParseFailed++
	}
}

func snapshotRuntime() RuntimeStatus {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	return runtimeInfo
}

func processLink(ctx *zero.Ctx, cfg config, link parsedLink) error {
	started := time.Now()
	logrus.Infof("[mediaparser] parsing platform=%s url=%s", link.Platform, link.URL)
	meta, err := parseNative(cfg, link)
	if err != nil && cfg.UseYTDLPFallback {
		logrus.Warnf("[mediaparser] native_parse_failed fallback=ytdlp platform=%s url=%s error=%v", link.Platform, link.URL, err)
		meta, err = parseWithYTDLP(cfg, link)
	}
	if err != nil {
		return err
	}
	meta.Author = cardDisplayAuthor(meta.Author)
	applyOutputFlags(cfg, &meta)

	infoCardSent := false
	if cfg.SendInfoCard && cfg.PlatformInfoCard[meta.Platform] && wantsText(cfg, meta.Platform) {
		infoCardSent = sendInfoCard(ctx, cfg, meta)
	}
	if cfg.SendMedia && cfg.PlatformSendMedia[meta.Platform] && wantsRich(cfg, meta.Platform) {
		if err := sendMediaNodes(ctx, cfg, &meta); err != nil {
			return err
		}
		if meta.ExceedsMaxSize && !infoCardSent && cfg.PlatformInfoCard[meta.Platform] {
			infoCardSent = sendInfoCard(ctx, cfg, meta)
		}
	}
	logrus.Infof("[mediaparser] success platform=%s url=%s elapsed=%s", link.Platform, link.URL, time.Since(started).Round(time.Millisecond))
	return nil
}

func sendInfoCard(ctx *zero.Ctx, cfg config, meta mediaMeta) bool {
	card, err := renderInfoCard(meta)
	if err != nil {
		logrus.Warnf("[mediaparser] render_card_failed platform=%s error=%v", meta.Platform, err)
		ctx.SendChain(message.Text(buildText(meta)))
		return false
	}
	ctx.SendChain(message.Image(fileURI(card)))
	logrus.Infof("[mediaparser] sent_info_card platform=%s title=%q path=%s", meta.Platform, meta.Title, card)
	scheduleDelete(card, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
	return true
}

func parseNative(cfg config, link parsedLink) (mediaMeta, error) {
	switch link.Platform {
	case "bilibili":
		return parseBilibili(cfg, link.URL)
	case "douyin":
		return parseDouyin(cfg, link.URL)
	case "tiktok":
		return parseTikTok(cfg, link.URL)
	case "kuaishou":
		return parseKuaishou(cfg, link.URL)
	case "weibo":
		return parseWeibo(cfg, link.URL)
	case "xiaohongshu":
		return parseXiaohongshu(cfg, link.URL)
	case "xianyu":
		return parseXianyu(cfg, link.URL)
	case "acfun":
		return parseAcfun(cfg, link.URL)
	case "youtube":
		return parseWithYTDLP(cfg, link)
	case "instagram":
		return parseInstagram(cfg, link.URL)
	case "toutiao":
		return parseToutiao(cfg, link.URL)
	case "xiaoheihe":
		return parseXiaoheihe(cfg, link.URL)
	case "twitter":
		return parseTwitter(cfg, link.URL)
	case "keylol":
		return parseKeylol(cfg, link.URL)
	default:
		return parseOpenGraph(cfg, link)
	}
}

func sendMediaNodes(ctx *zero.Ctx, cfg config, meta *mediaMeta) error {
	if cfg.DownloadVideo && cfg.PlatformDownload[meta.Platform] {
		if err := processDownloads(cfg, meta); err != nil {
			logrus.Warnf("[mediaparser] download_process_failed platform=%s error=%v", meta.Platform, err)
		}
	}
	if meta.ExceedsMaxSize {
		logrus.Infof("[mediaparser] oversized_video_preview_only platform=%s title=%q max_mb=%d", meta.Platform, meta.Title, cfg.MaxVideoMB)
	}
	if shouldForwardCombinedMedia(meta) {
		return sendCombinedMediaForward(ctx, meta)
	}
	if len(meta.VideoURLs) == 0 && len(meta.ImageURLs) > 0 {
		return sendImageGalleryForward(ctx, meta)
	}
	for i := range meta.VideoURLs {
		target := mediaVideoTarget(meta, i)
		if target == "" {
			continue
		}
		ctx.SendChain(message.Video(target))
		logrus.Infof("[mediaparser] sent_video platform=%s title=%q target=%s", meta.Platform, meta.Title, target)
	}
	for i := range meta.ImageURLs {
		target := mediaImageTarget(meta, i)
		if target == "" {
			continue
		}
		ctx.SendChain(message.Image(target))
		logrus.Infof("[mediaparser] sent_image platform=%s title=%q target=%s", meta.Platform, meta.Title, target)
	}
	return nil
}

func shouldForwardCombinedMedia(meta *mediaMeta) bool {
	if meta == nil || !isCombinedMediaPlatform(meta.Platform) {
		return false
	}
	if meta.Platform == "instagram" && len(meta.VideoURLs) > 0 {
		return true
	}
	return hasMixedMediaItems(*meta)
}

func isCombinedMediaPlatform(platform string) bool {
	switch platform {
	case "instagram", "twitter", "weibo":
		return true
	default:
		return false
	}
}

func hasMixedMediaItems(meta mediaMeta) bool {
	hasVideo, hasImage := false, false
	for _, item := range meta.MediaItems {
		switch item.Kind {
		case "video":
			hasVideo = true
		case "image":
			hasImage = true
		}
	}
	if len(meta.MediaItems) == 0 {
		hasVideo = len(meta.VideoURLs) > 0
		hasImage = len(meta.ImageURLs) > 0
	}
	return hasVideo && hasImage
}

func mediaItemsFor(videos, images [][]string) []mediaItem {
	items := make([]mediaItem, 0, len(videos)+len(images))
	for i := range videos {
		items = append(items, mediaItem{Kind: "video", Index: i})
	}
	for i := range images {
		items = append(items, mediaItem{Kind: "image", Index: i})
	}
	return items
}

func mediaVideoTarget(meta *mediaMeta, i int) string {
	if i < 0 || i >= len(meta.VideoURLs) {
		return ""
	}
	mode := ""
	if i < len(meta.VideoModes) {
		mode = meta.VideoModes[i]
	}
	if mode == "skip" {
		return ""
	}
	if mode == "local" && i < len(meta.FilePaths) && meta.FilePaths[i] != "" {
		return fileURI(meta.FilePaths[i])
	}
	if len(meta.VideoURLs[i]) > 0 {
		return stripMediaPrefix(meta.VideoURLs[i][0])
	}
	return ""
}

func mediaImageTarget(meta *mediaMeta, i int) string {
	if i < 0 || i >= len(meta.ImageURLs) {
		return ""
	}
	mode := ""
	if i < len(meta.ImageModes) {
		mode = meta.ImageModes[i]
	}
	if mode == "skip" {
		return ""
	}
	offset := len(meta.VideoURLs)
	if mode == "local" && offset+i < len(meta.FilePaths) && meta.FilePaths[offset+i] != "" {
		return fileURI(meta.FilePaths[offset+i])
	}
	if len(meta.ImageURLs[i]) > 0 {
		return stripMediaPrefix(meta.ImageURLs[i][0])
	}
	return ""
}

func sendCombinedMediaForward(ctx *zero.Ctx, meta *mediaMeta) error {
	nodes := message.Message{}
	botName := "瑙嗛瑙ｆ瀽bot"
	botID := ctx.Event.SelfID
	items := meta.MediaItems
	if len(items) == 0 {
		for i := range meta.VideoURLs {
			items = append(items, mediaItem{Kind: "video", Index: i})
		}
		for i := range meta.ImageURLs {
			items = append(items, mediaItem{Kind: "image", Index: i})
		}
	}
	videoCount, imageCount := 0, 0
	for _, item := range items {
		switch item.Kind {
		case "video":
			if target := mediaVideoTarget(meta, item.Index); target != "" {
				nodes = append(nodes, message.CustomNode(botName, botID, message.Message{message.Video(target)}))
				videoCount++
			}
		case "image":
			if target := mediaImageTarget(meta, item.Index); target != "" {
				nodes = append(nodes, message.CustomNode(botName, botID, message.Message{message.Image(target)}))
				imageCount++
			}
		}
	}
	if text := galleryForwardText(meta); text != "" {
		nodes = append(nodes, message.CustomNode(botName, botID, message.Message{message.Text(text)}))
	}
	if len(nodes) == 0 {
		return nil
	}
	var resID int64
	if ctx.Event.GroupID != 0 {
		resID = ctx.SendGroupForwardMessage(ctx.Event.GroupID, nodes).Get("message_id").Int()
	} else {
		resID = ctx.SendPrivateForwardMessage(ctx.Event.UserID, nodes).Get("message_id").Int()
	}
	logrus.Infof("[mediaparser] sent_combined_media_forward platform=%s title=%q nodes=%d videos=%d images=%d sender=%s(%d) message_id=%d", meta.Platform, meta.Title, len(nodes), videoCount, imageCount, botName, botID, resID)
	return nil
}

func sendImageGalleryForward(ctx *zero.Ctx, meta *mediaMeta) error {
	nodes := message.Message{}
	botName := "视频解析bot"
	botID := ctx.Event.SelfID
	imageNode := message.Message{}
	for i := range meta.ImageURLs {
		target := mediaImageTarget(meta, i)
		if target == "" {
			continue
		}
		imageNode = append(imageNode, message.Image(target))
		logrus.Infof("[mediaparser] prepared_forward_image platform=%s title=%q target=%s", meta.Platform, meta.Title, target)
	}
	if len(imageNode) > 0 {
		nodes = append(nodes, message.CustomNode(botName, botID, imageNode))
	}
	if text := galleryForwardText(meta); text != "" {
		nodes = append(nodes, message.CustomNode(botName, botID, message.Message{message.Text(text)}))
	}
	if len(nodes) == 0 {
		return nil
	}
	var resID int64
	if ctx.Event.GroupID != 0 {
		resID = ctx.SendGroupForwardMessage(ctx.Event.GroupID, nodes).Get("message_id").Int()
	} else {
		resID = ctx.SendPrivateForwardMessage(ctx.Event.UserID, nodes).Get("message_id").Int()
	}
	logrus.Infof("[mediaparser] sent_image_gallery_forward platform=%s title=%q nodes=%d images=%d sender=%s(%d) message_id=%d", meta.Platform, meta.Title, len(nodes), len(imageNode), botName, botID, resID)
	return nil
}

func galleryForwardText(meta *mediaMeta) string {
	parts := []string{}
	if meta.Title != "" {
		parts = append(parts, stripForwardLinks(meta.Title))
	}
	if meta.Desc != "" && meta.Desc != meta.Title {
		parts = append(parts, stripForwardLinks(meta.Desc))
	}
	return strings.Join(uniqueStrings(parts), "\n")
}

func stripForwardLinks(s string) string {
	s = regexp.MustCompile(`https?://\S+`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?i)\b(?:www\.)?(?:xhslink|xiaohongshu|xiaoheihe|twitter|x)\.com/\S+`).ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func processDownloads(cfg config, meta *mediaMeta) error {
	videoCount := len(meta.VideoURLs)
	imageCount := len(meta.ImageURLs)
	meta.FilePaths = make([]string, videoCount+imageCount)
	meta.VideoModes = make([]string, videoCount)
	meta.ImageModes = make([]string, imageCount)
	meta.VideoSizes = make([]float64, videoCount)
	meta.VideoSkipReasons = make([]string, videoCount)
	meta.ImageSkipReasons = make([]string, imageCount)

	for i, group := range meta.VideoURLs {
		if len(group) == 0 {
			meta.VideoModes[i] = "skip"
			meta.VideoSkipReasons[i] = "未找到视频URL"
			continue
		}
		needLocal := meta.ForceLocal || strings.HasPrefix(group[0], "dash:") || strings.HasPrefix(group[0], "m3u8:")
		if !needLocal {
			size, status := probeSize(group[0], meta.VideoHeads)
			if status == 403 {
				meta.HasAccessDenied = true
				meta.VideoModes[i] = "skip"
				meta.VideoSkipReasons[i] = "媒体访问被拒绝(403 Forbidden)"
				continue
			}
			if size > 0 {
				mb := float64(size) / 1024 / 1024
				meta.VideoSizes[i] = mb
				if cfg.MaxVideoMB > 0 && mb > float64(cfg.MaxVideoMB) {
					meta.VideoModes[i] = "skip"
					meta.VideoSkipReasons[i] = fmt.Sprintf("视频大小超过限制：%.1fMB > %dMB", mb, cfg.MaxVideoMB)
					meta.ExceedsMaxSize = true
					continue
				}
			}
			meta.VideoModes[i] = "direct"
			meta.HasValidMedia = true
			continue
		}
		path, sizeMB, err := downloadMediaGroup(cfg, meta, i, group)
		if err != nil {
			meta.VideoModes[i] = "skip"
			meta.VideoSkipReasons[i] = "缓存下载失败: " + err.Error()
			continue
		}
		if cfg.MaxVideoMB > 0 && sizeMB > float64(cfg.MaxVideoMB) {
			_ = os.Remove(path)
			meta.VideoModes[i] = "skip"
			meta.VideoSkipReasons[i] = fmt.Sprintf("下载后视频大小超过限制：%.1fMB > %dMB", sizeMB, cfg.MaxVideoMB)
			meta.ExceedsMaxSize = true
			continue
		}
		meta.FilePaths[i] = path
		meta.VideoSizes[i] = sizeMB
		meta.VideoModes[i] = "local"
		meta.HasValidMedia = true
		scheduleDelete(path, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
	}

	var imageWG sync.WaitGroup
	imageSem := make(chan struct{}, 6)
	var imageMu sync.Mutex
	for i, group := range meta.ImageURLs {
		i, group := i, group
		imageWG.Add(1)
		go func() {
			defer imageWG.Done()
			if len(group) == 0 {
				meta.ImageModes[i] = "skip"
				meta.ImageSkipReasons[i] = "未找到图片URL"
				return
			}
			imageSem <- struct{}{}
			path, _, err := downloadHTTPFile(cfg, group[0], meta.ImageHeads, cacheFile(meta, "image", i, ".jpg"))
			<-imageSem
			if err != nil {
				meta.ImageModes[i] = "skip"
				meta.ImageSkipReasons[i] = "图片下载失败: " + err.Error()
				return
			}
			pos := videoCount + i
			meta.FilePaths[pos] = path
			meta.ImageModes[i] = "local"
			imageMu.Lock()
			meta.HasValidMedia = true
			imageMu.Unlock()
			scheduleDelete(path, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
		}()
	}
	imageWG.Wait()
	return nil
}

func downloadMediaGroup(cfg config, meta *mediaMeta, index int, group []string) (string, float64, error) {
	raw := group[0]
	if strings.HasPrefix(raw, "dash:") {
		parts := strings.SplitN(strings.TrimPrefix(raw, "dash:"), "||", 2)
		if len(parts) == 0 || parts[0] == "" {
			return "", 0, errors.New("DASH 视频流为空")
		}
		videoTmp, _, err := downloadHTTPFile(cfg, stripMediaPrefix(parts[0]), meta.VideoHeads, cacheFile(meta, "video", index, ".m4s"))
		if err != nil {
			return "", 0, err
		}
		if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" {
			return videoTmp, fileSizeMB(videoTmp), nil
		}
		audioTmp, _, err := downloadHTTPFile(cfg, stripMediaPrefix(parts[1]), meta.VideoHeads, cacheFile(meta, "audio", index, ".m4s"))
		if err != nil {
			return "", 0, err
		}
		out := cacheFile(meta, "dash", index, ".mp4")
		if err := ffmpegMerge(cfg, videoTmp, audioTmp, out); err != nil {
			return "", 0, err
		}
		_ = os.Remove(videoTmp)
		_ = os.Remove(audioTmp)
		return out, fileSizeMB(out), nil
	}
	if strings.HasPrefix(raw, "m3u8:") {
		out := cacheFile(meta, "m3u8", index, ".mp4")
		if err := ffmpegM3U8(cfg, strings.TrimPrefix(raw, "m3u8:"), meta.VideoHeads, out); err != nil {
			return "", 0, err
		}
		return out, fileSizeMB(out), nil
	}
	if strings.HasPrefix(raw, "ytdlp:") {
		return downloadWithYTDLP(cfg, meta, strings.TrimPrefix(raw, "ytdlp:"))
	}
	return downloadHTTPFile(cfg, stripMediaPrefix(raw), meta.VideoHeads, cacheFile(meta, "video", index, ".mp4"))
}

func downloadHTTPFile(cfg config, raw string, headers map[string]string, out string) (string, float64, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUA)
	}
	httpClient := client
	if cfg.TimeoutSeconds > 0 {
		httpClient = &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", 0, err
	}
	f, err := os.Create(out)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		_ = os.Remove(out)
		return "", 0, err
	}
	return out, float64(n) / 1024 / 1024, nil
}

func ffmpegMerge(cfg config, video, audio, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", video, "-i", audio, "-c", "copy", "-movflags", "+faststart", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg merge: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func ffmpegM3U8(cfg config, raw string, headers map[string]string, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	args := []string{"-y"}
	headerLines := make([]string, 0, len(headers))
	for k, v := range headers {
		headerLines = append(headerLines, k+": "+v)
	}
	if len(headerLines) > 0 {
		args = append(args, "-headers", strings.Join(headerLines, "\r\n")+"\r\n")
	}
	args = append(args, "-i", raw, "-c", "copy", "-bsf:a", "aac_adtstoasc", "-movflags", "+faststart", out)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg m3u8: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func probeSize(raw string, headers map[string]string) (int64, int) {
	raw = stripMediaPrefix(raw)
	req, err := http.NewRequest(http.MethodHead, raw, nil)
	if err != nil {
		return 0, 0
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUA)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()
	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return size, resp.StatusCode
}

func parseOpenGraph(cfg config, link parsedLink) (mediaMeta, error) {
	req, err := http.NewRequest(http.MethodGet, link.URL, nil)
	if err != nil {
		return mediaMeta{}, err
	}
	req.Header.Set("User-Agent", defaultUA)
	resp, err := client.Do(req)
	if err != nil {
		return mediaMeta{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	html := string(body)
	meta := mediaMeta{
		URL:        link.URL,
		SourceURL:  link.URL,
		Platform:   link.Platform,
		Title:      firstNonEmpty(og(html, "title"), titleTag(html)),
		Author:     og(html, "site_name"),
		Desc:       firstNonEmpty(og(html, "description"), metaName(html, "description")),
		Cover:      og(html, "image"),
		VideoHeads: map[string]string{"User-Agent": defaultUA, "Referer": link.URL},
		ImageHeads: map[string]string{"User-Agent": defaultUA, "Referer": link.URL},
	}
	if v := og(html, "video"); v != "" {
		meta.VideoURLs = [][]string{{absolutize(link.URL, v)}}
		meta.HasValidMedia = true
	}
	if meta.Cover != "" {
		meta.ImageURLs = [][]string{{absolutize(link.URL, meta.Cover)}}
	}
	if meta.Title == "" && len(meta.VideoURLs) == 0 && len(meta.ImageURLs) == 0 {
		return mediaMeta{}, fmt.Errorf("原生解析暂未提取到有效媒体，平台=%s", link.Platform)
	}
	return meta, nil
}

func og(html, prop string) string {
	patterns := []string{
		`(?is)<meta[^>]+property=["']og:` + regexp.QuoteMeta(prop) + `["'][^>]+content=["']([^"']+)["']`,
		`(?is)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:` + regexp.QuoteMeta(prop) + `["']`,
	}
	for _, p := range patterns {
		if m := regexp.MustCompile(p).FindStringSubmatch(html); len(m) > 1 {
			return htmlUnescape(m[1])
		}
	}
	return ""
}

func metaName(html, name string) string {
	p := `(?is)<meta[^>]+name=["']` + regexp.QuoteMeta(name) + `["'][^>]+content=["']([^"']+)["']`
	if m := regexp.MustCompile(p).FindStringSubmatch(html); len(m) > 1 {
		return htmlUnescape(m[1])
	}
	return ""
}

func titleTag(html string) string {
	if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindStringSubmatch(html); len(m) > 1 {
		return strings.TrimSpace(htmlUnescape(regexp.MustCompile(`\s+`).ReplaceAllString(m[1], " ")))
	}
	return ""
}

func applyOutputFlags(cfg config, meta *mediaMeta) {
	if meta.Platform == "" {
		return
	}
	if cfg.OutputMode[meta.Platform] == outputOff {
		meta.VideoURLs = nil
		meta.ImageURLs = nil
	}
}

func wantsText(cfg config, platform string) bool {
	mode := cfg.OutputMode[platform]
	return mode == "" || mode == outputAll || mode == outputTextOnly
}

func wantsRich(cfg config, platform string) bool {
	mode := cfg.OutputMode[platform]
	return mode == "" || mode == outputAll || mode == outputRichOnly
}

func buildText(meta mediaMeta) string {
	parts := []string{}
	if meta.Title != "" {
		parts = append(parts, "标题："+meta.Title)
	}
	if meta.Author != "" {
		parts = append(parts, "作者："+cardDisplayAuthor(meta.Author))
	}
	if meta.Timestamp != "" {
		parts = append(parts, "发布时间："+meta.Timestamp)
	}
	if len(meta.VideoSizes) > 0 {
		maxSize, total := 0.0, 0.0
		for _, s := range meta.VideoSizes {
			if s > maxSize {
				maxSize = s
			}
			total += s
		}
		if maxSize > 0 {
			if len(meta.VideoSizes) == 1 {
				parts = append(parts, fmt.Sprintf("视频大小：%.1f MB", maxSize))
			} else {
				parts = append(parts, fmt.Sprintf("视频大小：最大 %.1f MB (共 %d 个视频, 总计 %.1f MB)", maxSize, len(meta.VideoSizes), total))
			}
		}
	}
	if meta.AccessText != "" {
		parts = append(parts, "时长："+meta.AccessText)
	}
	if meta.Error != "" {
		parts = append(parts, "解析失败："+meta.Error)
	}
	appendSkips := func(label string, reasons []string) {
		for i, reason := range reasons {
			if reason != "" {
				parts = append(parts, fmt.Sprintf("%s[%d]：%s", label, i+1, reason))
			}
		}
	}
	appendSkips("视频", meta.VideoSkipReasons)
	appendSkips("图片", meta.ImageSkipReasons)
	if meta.URL != "" {
		parts = append(parts, "原始链接："+meta.URL)
	}
	if meta.Desc != "" {
		parts = append(parts, "-------------------------------------", "简介/正文：", meta.Desc)
	}
	return strings.Join(parts, "\n")
}

func parseWithYTDLP(cfg config, link parsedLink) (mediaMeta, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	args := []string{"-J", "--no-playlist", "--no-warnings", "--skip-download"}
	if cfg.Proxy != "" {
		args = append(args, "--proxy", cfg.Proxy)
	}
	if cfg.BilibiliUseCookie && cfg.BilibiliCookie != "" {
		args = append(args, "--add-header", "Cookie:"+cfg.BilibiliCookie)
	}
	args = appendYTDLPPlatformArgs(args, cfg, link.Platform)
	args = append(args, link.URL)
	logDebug(cfg, "yt_dlp metadata command=%s args=%q", cfg.YTDLPPath, args)
	cmd := exec.CommandContext(ctx, cfg.YTDLPPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return mediaMeta{}, fmt.Errorf("yt-dlp: %w: %s", err, enrichYTDLPError(link.Platform, cfg, strings.TrimSpace(stderr.String())))
	}
	var info struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		Uploader    string  `json:"uploader"`
		UploaderID  string  `json:"uploader_id"`
		UploaderURL string  `json:"uploader_url"`
		Channel     string  `json:"channel"`
		ChannelID   string  `json:"channel_id"`
		ChannelURL  string  `json:"channel_url"`
		Thumbnail   string  `json:"thumbnail"`
		WebpageURL  string  `json:"webpage_url"`
		Duration    float64 `json:"duration"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return mediaMeta{}, err
	}
	return mediaMeta{
		URL:        firstNonEmpty(info.WebpageURL, link.URL),
		SourceURL:  link.URL,
		Platform:   link.Platform,
		Title:      info.Title,
		Author:     firstNonEmpty(info.Uploader, info.Channel),
		Avatar:     resolveYTDLPAvatar(cfg, link.Platform, info.Title, info.WebpageURL, info.ChannelURL, info.UploaderURL, info.UploaderID, info.ChannelID),
		Desc:       info.Description,
		Cover:      info.Thumbnail,
		VideoURLs:  [][]string{{"ytdlp:" + link.URL}},
		VideoHeads: map[string]string{"User-Agent": defaultUA},
		ImageHeads: map[string]string{"User-Agent": defaultUA},
		ForceLocal: true,
	}, nil
}

func resolveYTDLPAvatar(cfg config, platform, title, webpageURL, channelURL, uploaderURL, uploaderID, channelID string) string {
	switch platform {
	case "youtube":
		return resolveYouTubeAvatar(firstNonEmpty(channelURL, uploaderURL, youtubeChannelURL(uploaderID), youtubeChannelURL(channelID)))
	case "instagram":
		return resolveInstagramAvatar(cfg, instagramUsernameFromYTDLP(title, uploaderURL, uploaderID))
	default:
		return ""
	}
}

func youtubeChannelURL(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "@") {
		return "https://www.youtube.com/" + id
	}
	if strings.HasPrefix(id, "UC") {
		return "https://www.youtube.com/channel/" + id
	}
	return "https://www.youtube.com/@" + id
}

func resolveYouTubeAvatar(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	headers := map[string]string{
		"User-Agent":      defaultUA,
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}
	body, _, _, err := fetchText(raw, headers, true)
	if err != nil {
		return ""
	}
	return firstNonEmpty(
		firstRegexGroup(body, `"avatar"\s*:\s*\{[^{}]*"thumbnails"\s*:\s*\[\s*\{[^{}]*"url"\s*:\s*"([^"]+)`),
		firstRegexGroup(body, `(https://yt3\.ggpht\.com/[^"\\]+)`),
	)
}

func instagramUsernameFromYTDLP(title, uploaderURL, uploaderID string) string {
	for _, raw := range []string{uploaderURL, uploaderID} {
		raw = strings.TrimSpace(raw)
		if raw == "" || regexp.MustCompile(`^\d+$`).MatchString(raw) {
			continue
		}
		if u, err := url.Parse(raw); err == nil && strings.Contains(strings.ToLower(u.Hostname()), "instagram.com") {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) > 0 && parts[0] != "" {
				return strings.TrimPrefix(parts[0], "@")
			}
		}
		return strings.TrimPrefix(raw, "@")
	}
	if m := regexp.MustCompile(`(?i)^(?:post|video|reel) by ([A-Za-z0-9._]+)`).FindStringSubmatch(strings.TrimSpace(title)); len(m) > 1 {
		return m[1]
	}
	return ""
}

func resolveInstagramAvatar(cfg config, username string) string {
	username = strings.Trim(strings.TrimSpace(username), "@")
	if username == "" {
		return ""
	}
	headers := map[string]string{
		"User-Agent":      instagramWebUA,
		"Accept":          "application/json,text/plain,*/*",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		"X-IG-App-ID":     "936619743392459",
		"Referer":         "https://www.instagram.com/" + username + "/",
	}
	if cfg.InstagramCookie != "" {
		headers["Cookie"] = cfg.InstagramCookie
	}
	api := "https://www.instagram.com/api/v1/users/web_profile_info/?username=" + url.QueryEscape(username)
	body, _, status, err := fetchText(api, headers, true)
	if err == nil && status < 400 {
		if avatar := firstNonEmpty(
			firstRegexGroup(body, `"profile_pic_url_hd"\s*:\s*"([^"]+)`),
			firstRegexGroup(body, `"profile_pic_url"\s*:\s*"([^"]+)`),
		); avatar != "" {
			return avatar
		}
	}
	return ""
}

func firstRegexGroup(s, pattern string) string {
	re := regexp.MustCompile(pattern)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	out := strings.ReplaceAll(m[1], `\u0026`, "&")
	out = strings.ReplaceAll(out, `\/`, `/`)
	return html.UnescapeString(out)
}

func downloadWithYTDLP(cfg config, meta *mediaMeta, raw string) (string, float64, error) {
	out := cacheFile(meta, "ytdlp", 0, ".mp4")
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	format := "bv*[vcodec!*=av01]+ba/b[vcodec!*=av01]/bv*+ba/b"
	if !cfg.AvoidAV1 {
		format = "bv*+ba/b"
	}
	if cfg.VideoMaxResolution > 0 {
		if cfg.AvoidAV1 {
			format = fmt.Sprintf("bv*[height<=%d][vcodec!*=av01]+ba/b[height<=%d][vcodec!*=av01]/bv*[height<=%d]+ba/b[height<=%d]", cfg.VideoMaxResolution, cfg.VideoMaxResolution, cfg.VideoMaxResolution, cfg.VideoMaxResolution)
		} else {
			format = fmt.Sprintf("bv*[height<=%d]+ba/b[height<=%d]", cfg.VideoMaxResolution, cfg.VideoMaxResolution)
		}
	}
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"-f", format,
		"--merge-output-format", "mp4",
		"-o", out,
	}
	if cfg.Proxy != "" {
		args = append(args, "--proxy", cfg.Proxy)
	}
	if cfg.BilibiliUseCookie && cfg.BilibiliCookie != "" {
		args = append(args, "--add-header", "Cookie:"+cfg.BilibiliCookie)
	}
	args = appendYTDLPPlatformArgs(args, cfg, meta.Platform)
	args = append(args, raw)
	logDebug(cfg, "yt_dlp download command=%s args=%q", cfg.YTDLPPath, args)
	cmd := exec.CommandContext(ctx, cfg.YTDLPPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("yt-dlp download: %w: %s", err, enrichYTDLPError(meta.Platform, cfg, strings.TrimSpace(stderr.String())))
	}
	return out, fileSizeMB(out), nil
}

func appendYTDLPPlatformArgs(args []string, cfg config, platform string) []string {
	if cookieFile := ytdlpCookieFileForPlatform(cfg, platform); cookieFile != "" {
		args = append(args, "--cookies", cookieFile)
	}
	if platform == "youtube" && strings.TrimSpace(cfg.YouTubeExtractorArgs) != "" {
		args = append(args, "--extractor-args", strings.TrimSpace(cfg.YouTubeExtractorArgs))
	}
	return args
}

func enrichYTDLPError(platform string, cfg config, detail string) string {
	if platform == "instagram" && cfg.InstagramCookie == "" && cfg.InstagramCookieFile == "" {
		return detail + "\nInstagram 经常需要登录态 Cookie；请在 WebUI 的聚合解析 > 下载与 Cookie 里粘贴 Instagram Cookie。"
	}
	return detail
}

func ytdlpCookieFileForPlatform(cfg config, platform string) string {
	switch platform {
	case "youtube":
		if cfg.YouTubeCookie != "" {
			path, err := writeYTDLPCookieFile("youtube", ".youtube.com", cfg.YouTubeCookie)
			if err == nil {
				return path
			}
		}
		return cfg.YouTubeCookieFile
	case "instagram":
		if cfg.InstagramCookie != "" {
			path, err := writeYTDLPCookieFile("instagram", ".instagram.com", cfg.InstagramCookie)
			if err == nil {
				return path
			}
		}
		return cfg.InstagramCookieFile
	default:
		return ""
	}
}

func writeYTDLPCookieFile(platform, domain, cookieHeader string) (string, error) {
	base := cacheDir
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "cookies")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, platform+".txt")
	lines := []string{
		"# Netscape HTTP Cookie File",
		"# Generated by ZeroBot media parser.",
	}
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\tTRUE\t/\tTRUE\t2147483647\t%s\t%s", domain, name, value))
	}
	if len(lines) == 2 {
		return "", fmt.Errorf("empty cookie")
	}
	return path, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func rateLimited(userID, groupID int64) bool {
	key := groupID
	if key == 0 {
		key = -userID
	}
	now := time.Now()
	limitMu.Lock()
	defer limitMu.Unlock()
	last := lastParseAt[key]
	if !last.IsZero() && now.Sub(last) < 10*time.Second {
		return true
	}
	lastParseAt[key] = now
	return false
}

func hasKeyword(text string, keywords []string) bool {
	for _, kw := range keywords {
		if kw != "" && strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func permissionOK(cfg config, userID, groupID int64) (bool, string) {
	if groupID == 0 {
		switch cfg.PrivateAccessMode {
		case accessWhitelist:
			if cfg.UserWhitelist[userID] {
				return true, "private_user_whitelisted"
			}
			return false, "private_not_in_whitelist"
		case accessBlacklist:
			if cfg.UserBlacklist[userID] {
				return false, "private_user_blacklisted"
			}
			return true, "private_not_blacklisted"
		default:
			return true, "private_access_none"
		}
	}
	switch cfg.GroupAccessMode {
	case accessWhitelist:
		if !cfg.GroupWhitelist[groupID] {
			return false, "group_not_in_whitelist"
		}
	case accessBlacklist:
		if cfg.GroupBlacklist[groupID] {
			return false, "group_blacklisted"
		}
	}
	switch cfg.GroupUserAccessMode {
	case accessWhitelist:
		if cfg.GroupUserWhitelist[userID] {
			return true, "group_user_whitelisted"
		}
		return false, "group_user_not_in_whitelist"
	case accessBlacklist:
		if cfg.GroupUserBlacklist[userID] {
			return false, "group_user_blacklisted"
		}
		return true, "group_user_not_blacklisted"
	default:
		return true, "group_access_ok"
	}
}

func extractLinks(text string, cfg config) []parsedLink {
	text = htmlUnescape(text)
	matches := urlRE.FindAllString(text, -1)
	out := make([]parsedLink, 0, len(matches))
	seen := map[string]bool{}
	for _, raw := range matches {
		raw = strings.TrimRight(raw, ".。；;?)）")
		p := platformForURL(raw)
		if p == "" {
			logDebug(cfg, "link ignored unsupported_host url=%s", raw)
			continue
		}
		if !cfg.PlatformEnabled[p] {
			logDebug(cfg, "link ignored platform_disabled platform=%s url=%s", p, raw)
			continue
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, parsedLink{URL: raw, Platform: p})
	}
	return out
}

func platformForURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, p := range platforms {
		for _, h := range p.Hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return p.Name
			}
		}
	}
	return ""
}

func normalizePlatformName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range platforms {
		if p.Name == name {
			return p.Name
		}
		for _, alias := range p.Aliases {
			if strings.ToLower(alias) == name {
				return p.Name
			}
		}
	}
	return ""
}

func setAccessList(listName, scope, action string, id int64) {
	var target map[int64]bool
	if listName == "黑名单" {
		if scope == "群" || scope == "group" {
			target = currentConf.GroupBlacklist
		} else if scope == "群成员" || scope == "群用户" || scope == "group_user" || scope == "member" {
			target = currentConf.GroupUserBlacklist
		} else {
			target = currentConf.UserBlacklist
		}
	} else {
		if scope == "群" || scope == "group" {
			target = currentConf.GroupWhitelist
		} else if scope == "群成员" || scope == "群用户" || scope == "group_user" || scope == "member" {
			target = currentConf.GroupUserWhitelist
		} else {
			target = currentConf.UserWhitelist
		}
	}
	if action == "删除" || action == "del" || action == "remove" {
		delete(target, id)
		return
	}
	target[id] = true
}

func parseAccessMode(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "关", "关闭", "禁用", "none", "off":
		return accessNone, true
	case "黑名单", "blacklist", "black":
		return accessBlacklist, true
	case "白名单", "whitelist", "white":
		return accessWhitelist, true
	default:
		return "", false
	}
}

func validAccessMode(v string) bool {
	return v == accessNone || v == accessBlacklist || v == accessWhitelist
}

func parseOutputMode(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "全部", "全部发送", "all":
		return outputAll, true
	case "文本", "text":
		return outputTextOnly, true
	case "媒体", "富媒体", "rich", "media":
		return outputRichOnly, true
	case "关闭", "off":
		return outputOff, true
	default:
		return "", false
	}
}

func parseVideoResolution(v string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "不限", "不限制", "unlimited", "none", "0", "off":
		return 0, true
	case "360", "360p":
		return 360, true
	case "720", "720p":
		return 720, true
	case "1080", "1080p":
		return 1080, true
	default:
		return 0, false
	}
}

func biliQualityFromResolution(res int) string {
	switch res {
	case 360:
		return "360P"
	case 720:
		return "720P"
	case 1080:
		return "1080P"
	default:
		return "不限制"
	}
}

func isModeWord(v string) bool { return isOn(v) || isOff(v) }

func isOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "开启", "启用", "打开", "on", "true", "1":
		return true
	default:
		return false
	}
}

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "关闭", "禁用", "off", "false", "0":
		return true
	default:
		return false
	}
}

func formatStatus() string {
	cfg := snapshotConfig()
	return formatStatusFor(cfg)
}

func formatStatusLocked() string {
	normalizeConfig(&currentConf)
	return formatStatusFor(currentConf)
}

func formatStatusFor(cfg config) string {
	on := []string{}
	off := []string{}
	for _, p := range platforms {
		if cfg.PlatformEnabled[p.Name] {
			on = append(on, p.Name)
		} else {
			off = append(off, p.Name)
		}
	}
	return fmt.Sprintf(
		"媒体解析状态\n自动解析: %v\n调试日志: %v\n私聊名单模式: %s\n群聊名单模式: %s\n群成员名单模式: %s\n信息图: %v\n媒体: %v\n下载视频: %v\n避开AV1: %v\n备用yt-dlp: %v\nB站Cookie: %v\n全局视频画质: %s\n视频上限: %dMB\n缓存TTL: %d分钟\n配置文件: %s\n启用平台: %s\n关闭平台: %s",
		cfg.AutoParse,
		cfg.Debug,
		cfg.PrivateAccessMode,
		cfg.GroupAccessMode,
		cfg.GroupUserAccessMode,
		cfg.SendInfoCard,
		cfg.SendMedia,
		cfg.DownloadVideo,
		cfg.AvoidAV1,
		cfg.UseYTDLPFallback,
		cfg.BilibiliUseCookie && cfg.BilibiliCookie != "",
		biliQualityFromResolution(cfg.VideoMaxResolution),
		cfg.MaxVideoMB,
		cfg.CacheTTLMinutes,
		configPath,
		strings.Join(on, ", "),
		strings.Join(off, ", "),
	)
}

func cleanCache() (int, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		p := filepath.Join(cacheDir, entry.Name())
		if err := os.RemoveAll(p); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func cacheFile(meta *mediaMeta, kind string, index int, ext string) string {
	return filepath.Join(cacheDir, cacheName(meta.SourceURL, meta.Platform), fmt.Sprintf("%s_%d%s", kind, index, ext))
}

func cacheName(raw, platform string) string {
	sum := sha1.Sum([]byte(raw))
	return platform + "_" + hex.EncodeToString(sum[:])[:16]
}

func fileSizeMB(path string) float64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(st.Size()) / 1024 / 1024
}

func fileURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:///" + strings.TrimPrefix(filepath.ToSlash(abs), "/")
}

func scheduleDelete(path string, delay time.Duration) {
	if path == "" {
		return
	}
	go func() {
		time.Sleep(delay)
		_ = os.Remove(path)
	}()
}

func stripMediaPrefix(raw string) string {
	for _, prefix := range []string{"range:", "m3u8:"} {
		raw = strings.TrimPrefix(raw, prefix)
	}
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func optionalImage(raw string) [][]string {
	if raw == "" {
		return nil
	}
	return [][]string{{raw}}
}

func absolutize(base, ref string) string {
	u, err := url.Parse(ref)
	if err != nil || u.IsAbs() {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	return b.ResolveReference(u).String()
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "..."
}

func logDebug(cfg config, format string, args ...any) {
	if cfg.Debug {
		logrus.Infof("[mediaparser] debug "+format, args...)
	}
}

func htmlUnescape(s string) string {
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return replacer.Replace(s)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
