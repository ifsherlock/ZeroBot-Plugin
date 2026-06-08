// Package dailynews 每天60秒读懂世界
package dailynews

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	defaultSourceID = "60s"
	defaultBaseURL  = "https://60s.744524299.xyz/v2/60s"
	legacyImageAPI  = "https://uapis.cn/api/v1/daily/news-image"
)

type newsSource struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Category string            `json:"category,omitempty"`
	Desc     string            `json:"desc,omitempty"`
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Encoding string            `json:"encoding"`
	Headers  map[string]string `json:"headers,omitempty"`
	Params   []newsParam       `json:"params,omitempty"`
	Commands []string          `json:"commands,omitempty"`
	Timeout  int               `json:"timeout_seconds,omitempty"`
	Enabled  bool              `json:"enabled"`
	Disabled bool              `json:"disabled,omitempty"`
	Builtin  bool              `json:"builtin,omitempty"`
}

type newsParam struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Source      string `json:"source,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type newsSchedule struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Target   string `json:"target"`
	Time     string `json:"time"`
	Cron     string `json:"cron,omitempty"`
	Format   string `json:"format"`
	Enabled  bool   `json:"enabled"`
	LastRun  string `json:"last_run,omitempty"`
}

type newsConfig struct {
	DefaultSource string         `json:"default_source"`
	DefaultFormat string         `json:"default_format"`
	Commands      []string       `json:"commands"`
	Sources       []newsSource   `json:"sources"`
	Schedules     []newsSchedule `json:"schedules"`
	Access        newsAccess     `json:"access"`
}

type newsAccess struct {
	Enabled          bool    `json:"enabled"`
	PrivateEnabled   bool    `json:"private_enabled"`
	PrivateMode      string  `json:"private_mode"`
	PrivateWhitelist []int64 `json:"private_whitelist,omitempty"`
	PrivateBlacklist []int64 `json:"private_blacklist,omitempty"`
	GroupMode        string  `json:"group_mode"`
	GroupWhitelist   []int64 `json:"group_whitelist,omitempty"`
	GroupBlacklist   []int64 `json:"group_blacklist,omitempty"`
}

type WebNewsSource = newsSource
type WebNewsSchedule = newsSchedule
type WebNewsConfig = newsConfig

type newsAPIResponse struct {
	Date       string     `json:"date"`
	Day        string     `json:"day_of_week"`
	LunarDate  string     `json:"lunar_date"`
	News       []newsItem `json:"news"`
	Tip        string     `json:"tip"`
	Image      string     `json:"image"`
	Updated    string     `json:"updated"`
	APIUpdated string     `json:"api_updated"`
}

type newsItem struct {
	Title string `json:"title"`
	Link  string `json:"link"`
}

var (
	engine = control.AutoRegister(&ctrl.Options[*zero.Ctx]{
		DisableOnDefault:  false,
		Extra:             60,
		Brief:             "每天60秒读懂世界",
		Help:              dailyNewsHelp(),
		PrivateDataFolder: "dailynews",
	})

	cfgPath       string
	cacheDir      string
	cfgMu         sync.RWMutex
	cfg           newsConfig
	schedulerOnce sync.Once
	httpClient    = &http.Client{Timeout: 20 * time.Second}
)

func init() {
	cfgPath = filepath.Join(engine.DataFolder(), "config.json")
	cacheDir = dailyNewsCacheDir()
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		logrus.Warnf("[dailynews] create cache dir failed: %v", err)
	}
	loadConfig()
	startScheduler()
	logrus.Info("[dailynews] ready")

	zero.OnMessage().SetBlock(false).Handle(func(ctx *zero.Ctx) {
		text := strings.TrimSpace(ctx.Event.Message.ExtractPlainText())
		if text == "" {
			return
		}
		if !allowAccess(ctx) {
			logrus.Debugf("[dailynews] skip access user=%d group=%d", ctx.Event.UserID, ctx.Event.GroupID)
			return
		}
		sourceID, format, args, ok := matchConfiguredCommand(text)
		if !ok {
			return
		}
		logrus.Infof("[dailynews] command matched user=%d group=%d source=%s format=%s", ctx.Event.UserID, ctx.Event.GroupID, firstNonEmpty(sourceID, "default"), firstNonEmpty(format, "auto"))
		sendNews(ctx, sourceID, format, args, "")
	})
	zero.OnPrefix("60秒接口添加", zero.AdminPermission).SetBlock(true).Handle(handleAddSource)
	zero.OnPrefix("60秒接口删除", zero.AdminPermission).SetBlock(true).Handle(handleDeleteSource)
	zero.OnFullMatch("60秒接口列表", zero.AdminPermission).SetBlock(true).Handle(handleListSources)
	zero.OnPrefix("60秒定时添加", zero.AdminPermission).SetBlock(true).Handle(handleAddSchedule)
	zero.OnPrefix("60秒定时删除", zero.AdminPermission).SetBlock(true).Handle(handleDeleteSchedule)
	zero.OnFullMatch("60秒定时列表", zero.AdminPermission).SetBlock(true).Handle(handleListSchedules)
	zero.OnPrefix("60秒默认接口", zero.AdminPermission).SetBlock(true).Handle(handleDefaultSource)
	zero.OnPrefix("60秒默认格式", zero.AdminPermission).SetBlock(true).Handle(handleDefaultFormat)
}

func dailyNewsHelp() string {
	return strings.Join([]string{
		"- 今日早报",
		"- 60秒早报 [接口ID] [image|text|markdown|json] [YYYY-MM-DD]",
		"- 60秒接口列表",
		"- 60秒接口添加 ID 名称 URL [格式]",
		"- 60秒接口删除 ID",
		"- 60秒默认接口 ID",
		"- 60秒默认格式 image|text|markdown|json",
		"- 60秒定时添加 ID 接口ID 群:123456 30 8 * * * [格式]",
		"- 60秒定时删除 ID",
		"- 60秒定时列表",
	}, "\n")
}

func defaultConfig() newsConfig {
	return newsConfig{
		DefaultSource: defaultSourceID,
		DefaultFormat: "image",
		Commands:      []string{"今日早报", "60秒读懂世界", "每天60秒读懂世界", "60秒早报", "60s早报"},
		Access: newsAccess{
			Enabled:        true,
			PrivateEnabled: true,
			PrivateMode:    "none",
			GroupMode:      "none",
		},
		Sources: []newsSource{
			{ID: defaultSourceID, Name: "每天60秒", Category: "news", Desc: "每日新闻、微语和图片早报", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"今日早报", "60秒读懂世界", "每天60秒读懂世界", "60秒早报", "60s早报"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-text", Name: "60s 文本", Category: "news", Desc: "文本格式早报", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "text", Timeout: 20, Builtin: true, Commands: []string{"文字早报", "早报文本"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-markdown", Name: "60s Markdown", Category: "news", Desc: "Markdown 格式早报", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "markdown", Timeout: 20, Builtin: true, Commands: []string{"markdown早报", "md早报"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-image", Name: "60s 图片跳转", Category: "news", Desc: "图片跳转早报", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "image", Timeout: 20, Builtin: true, Commands: []string{"图片早报"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-image-proxy", Name: "60s 图片代理", Category: "news", Desc: "直接下载图片早报", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"早报图片"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "legacy-image", Name: "旧版早报图片", Category: "news", Desc: "旧版图片接口", URL: legacyImageAPI, Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"旧版早报"}},
			{ID: "weather-realtime", Name: "实时天气", Category: "weather", Desc: "查询城市实时天气", URL: "https://60s.viki.moe/v2/weather/realtime", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"天气", "实时天气"}, Params: []newsParam{{Name: "query", Label: "地点", Source: "rest", Required: true, Placeholder: "北京"}}},
			{ID: "weather-forecast", Name: "天气预报", Category: "weather", Desc: "查询城市天气预报", URL: "https://60s.viki.moe/v2/weather/forecast", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"天气预报", "未来天气"}, Params: []newsParam{{Name: "query", Label: "地点", Source: "rest", Required: true, Placeholder: "上海"}}},
			{ID: "weibo", Name: "微博热搜", Category: "hot", Desc: "微博热搜榜", URL: "https://60s.viki.moe/v2/weibo", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"微博热搜", "微博热点"}},
			{ID: "zhihu", Name: "知乎热榜", Category: "hot", Desc: "知乎热门话题", URL: "https://60s.viki.moe/v2/zhihu", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"知乎热榜", "知乎热点"}},
			{ID: "baidu-hot", Name: "百度热搜", Category: "hot", Desc: "百度热搜榜", URL: "https://60s.viki.moe/v2/baidu/hot", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"百度热搜", "百度热点"}},
			{ID: "douyin-hot", Name: "抖音热点", Category: "hot", Desc: "抖音热点榜", URL: "https://60s.viki.moe/v2/douyin", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"抖音热点", "抖音热榜"}},
			{ID: "toutiao", Name: "头条热点", Category: "hot", Desc: "今日头条热点", URL: "https://60s.viki.moe/v2/toutiao", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"头条热点", "今日头条"}},
			{ID: "bili-hot", Name: "B站热榜", Category: "hot", Desc: "哔哩哔哩热门视频", URL: "https://60s.viki.moe/v2/bili", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"B站热榜", "b站热榜", "哔哩热榜"}},
			{ID: "exchange-rate", Name: "汇率查询", Category: "data", Desc: "货币汇率查询", URL: "https://60s.viki.moe/v2/exchange-rate", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"汇率", "汇率查询"}, Params: []newsParam{{Name: "from", Label: "源币种", Source: "arg", Default: "USD", Placeholder: "USD"}, {Name: "to", Label: "目标币种", Source: "arg", Default: "CNY", Placeholder: "CNY"}}},
			{ID: "lunar", Name: "农历查询", Category: "data", Desc: "公历农历与生肖节气", URL: "https://60s.viki.moe/v2/lunar", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"农历", "今日农历"}, Params: []newsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "today-history", Name: "历史上的今天", Category: "data", Desc: "历史事件查询", URL: "https://60s.viki.moe/v2/today-in-history", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"历史上的今天", "历史今天"}, Params: []newsParam{{Name: "month", Label: "月份", Source: "arg", Placeholder: "1"}, {Name: "day", Label: "日期", Source: "arg", Placeholder: "15"}}},
			{ID: "baike", Name: "百科查询", Category: "data", Desc: "中文百科搜索", URL: "https://60s.viki.moe/v2/baike", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"百科", "百科查询"}, Params: []newsParam{{Name: "keyword", Label: "关键词", Source: "rest", Required: true, Placeholder: "Python编程"}}},
			{ID: "fuel-price", Name: "油价查询", Category: "data", Desc: "国内油价", URL: "https://60s.viki.moe/v2/fuel-price", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"油价", "油价查询"}, Params: []newsParam{{Name: "province", Label: "省份", Source: "rest", Required: true, Placeholder: "北京"}}},
			{ID: "gold-price", Name: "金价查询", Category: "data", Desc: "黄金价格", URL: "https://60s.viki.moe/v2/gold-price", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"金价", "黄金价格"}},
			{ID: "chemical", Name: "化学元素", Category: "data", Desc: "元素信息查询", URL: "https://60s.viki.moe/v2/chemical", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"化学元素", "元素查询"}, Params: []newsParam{{Name: "query", Label: "元素", Source: "rest", Required: true, Placeholder: "H"}}},
			{ID: "hitokoto", Name: "一言", Category: "fun", Desc: "随机一句话", URL: "https://60s.viki.moe/v2/hitokoto", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"一言", "来句一言"}, Params: []newsParam{{Name: "category", Label: "分类", Source: "arg", Placeholder: "anime"}}},
			{ID: "dad-joke", Name: "英文冷笑话", Category: "fun", Desc: "Dad joke", URL: "https://60s.viki.moe/v2/dad-joke", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"英文笑话", "dad joke"}},
			{ID: "duanzi", Name: "中文段子", Category: "fun", Desc: "随机中文段子", URL: "https://60s.viki.moe/v2/duanzi", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"讲个笑话", "段子", "笑话"}},
			{ID: "luck", Name: "今日运势", Category: "fun", Desc: "每日运势", URL: "https://60s.viki.moe/v2/luck", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"今日运势", "运势"}},
			{ID: "kfc", Name: "疯狂星期四", Category: "fun", Desc: "KFC 梗文案", URL: "https://60s.viki.moe/v2/kfc", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"疯狂星期四", "kfc"}},
			{ID: "moyu", Name: "摸鱼日历", Category: "fun", Desc: "摸鱼日历图片", URL: "https://60s.viki.moe/v2/moyu", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"摸鱼日历", "摸鱼"}},
			{ID: "ncm-rank-list", Name: "网易云榜单", Category: "media", Desc: "音乐榜单列表", URL: "https://60s.viki.moe/v2/ncm-rank/list", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网易云榜单", "音乐榜单"}},
			{ID: "ncm-rank", Name: "网易云榜单详情", Category: "media", Desc: "音乐榜单歌曲", URL: "https://60s.viki.moe/v2/ncm-rank/{id}", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网易云热歌", "音乐排行"}, Params: []newsParam{{Name: "id", Label: "榜单ID", Source: "arg", Default: "3778678", Placeholder: "3778678"}}},
			{ID: "lyric", Name: "歌词搜索", Category: "media", Desc: "搜索歌词", URL: "https://60s.viki.moe/v2/lyric", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"歌词", "歌词搜索"}, Params: []newsParam{{Name: "keyword", Label: "歌曲", Source: "rest", Required: true, Placeholder: "稻香 周杰伦"}}},
			{ID: "maoyan-all-movie", Name: "电影资料", Category: "media", Desc: "猫眼电影资料", URL: "https://60s.viki.moe/v2/maoyan/all/movie", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电影资料"}},
			{ID: "maoyan-movie", Name: "实时票房", Category: "media", Desc: "电影票房排行", URL: "https://60s.viki.moe/v2/maoyan/realtime/movie", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电影票房", "实时票房"}},
			{ID: "maoyan-tv", Name: "电视剧收视", Category: "media", Desc: "电视剧收视率", URL: "https://60s.viki.moe/v2/maoyan/realtime/tv", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电视剧收视", "收视率"}},
			{ID: "maoyan-web", Name: "网剧热度", Category: "media", Desc: "网剧热度排行", URL: "https://60s.viki.moe/v2/maoyan/realtime/web", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网剧热度", "网剧排行"}},
			{ID: "ip", Name: "IP 查询", Category: "tool", Desc: "IP 归属地和运营商", URL: "https://60s.viki.moe/v2/ip", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"ip查询", "查ip"}, Params: []newsParam{{Name: "ip", Label: "IP", Source: "rest", Placeholder: "8.8.8.8"}}},
			{ID: "fanyi", Name: "文本翻译", Category: "tool", Desc: "多语言翻译", URL: "https://60s.viki.moe/v2/fanyi", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"翻译"}, Params: []newsParam{{Name: "text", Label: "文本", Source: "rest", Required: true, Placeholder: "你好世界"}, {Name: "from", Label: "源语言", Source: "default", Default: "auto"}, {Name: "to", Label: "目标语言", Source: "arg", Default: "zh", Placeholder: "en"}}},
			{ID: "fanyi-langs", Name: "翻译语言", Category: "tool", Desc: "支持语言列表", URL: "https://60s.viki.moe/v2/fanyi/langs", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"翻译语言"}},
			{ID: "qrcode", Name: "二维码", Category: "tool", Desc: "生成二维码图片", URL: "https://60s.viki.moe/v2/qrcode", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"二维码", "生成二维码"}, Params: []newsParam{{Name: "text", Label: "内容", Source: "rest", Required: true, Placeholder: "https://example.com"}, {Name: "size", Label: "尺寸", Source: "arg", Default: "300", Placeholder: "300"}}},
			{ID: "hash", Name: "哈希计算", Category: "tool", Desc: "MD5/SHA 计算", URL: "https://60s.viki.moe/v2/hash", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"哈希", "hash"}, Params: []newsParam{{Name: "text", Label: "文本", Source: "rest", Required: true, Placeholder: "Hello World"}, {Name: "algorithm", Label: "算法", Source: "arg", Default: "md5", Placeholder: "sha256"}}},
			{ID: "og", Name: "网页元信息", Category: "tool", Desc: "提取网页标题描述图片", URL: "https://60s.viki.moe/v2/og", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网页信息", "og"}, Params: []newsParam{{Name: "url", Label: "网址", Source: "rest", Required: true, Placeholder: "https://example.com"}}},
			{ID: "whois", Name: "WHOIS 查询", Category: "tool", Desc: "域名注册信息", URL: "https://60s.viki.moe/v2/whois", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"whois", "域名查询"}, Params: []newsParam{{Name: "domain", Label: "域名", Source: "rest", Required: true, Placeholder: "github.com"}}},
			{ID: "password", Name: "密码生成", Category: "tool", Desc: "生成随机密码", URL: "https://60s.viki.moe/v2/password", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"生成密码", "随机密码"}, Params: []newsParam{{Name: "length", Label: "长度", Source: "arg", Default: "16", Placeholder: "16"}, {Name: "numbers", Label: "数字", Source: "default", Default: "true"}, {Name: "lowercase", Label: "小写", Source: "default", Default: "true"}, {Name: "uppercase", Label: "大写", Source: "default", Default: "true"}, {Name: "symbols", Label: "符号", Source: "default", Default: "true"}}},
		},
	}
}

func loadConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = defaultConfig()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		_ = saveConfigLocked()
		return
	}
	var saved newsConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		logrus.Warnf("[dailynews] load config failed: %v", err)
		return
	}
	cfg = normalizeConfig(saved)
	_ = saveConfigLocked()
}

func normalizeConfig(in newsConfig) newsConfig {
	base := defaultConfig()
	if strings.TrimSpace(in.DefaultSource) != "" {
		base.DefaultSource = sanitizeID(in.DefaultSource)
	}
	if isSupportedFormat(in.DefaultFormat) {
		base.DefaultFormat = strings.ToLower(in.DefaultFormat)
	}
	if commands := normalizeCommands(in.Commands); len(commands) > 0 {
		base.Commands = commands
	}
	base.Access = normalizeAccess(in.Access)
	merged := make(map[string]newsSource)
	for _, src := range base.Sources {
		merged[src.ID] = normalizeSource(src)
	}
	for _, src := range in.Sources {
		src = normalizeSource(src)
		if src.ID == "" || src.URL == "" {
			continue
		}
		if old, ok := merged[src.ID]; ok && old.Builtin {
			src.Builtin = true
			if len(src.Params) == 0 {
				src.Params = old.Params
			}
		}
		merged[src.ID] = src
	}
	base.Sources = mapToSources(merged)
	if _, ok := merged[base.DefaultSource]; !ok {
		base.DefaultSource = defaultSourceID
	}
	schedules := make(map[string]newsSchedule, len(in.Schedules))
	order := make([]string, 0, len(in.Schedules))
	for _, task := range in.Schedules {
		task = normalizeSchedule(task, merged)
		if task.ID == "" || task.SourceID == "" || task.Target == "" || task.Time == "" {
			continue
		}
		if _, ok := merged[task.SourceID]; !ok {
			continue
		}
		if _, ok := schedules[task.ID]; !ok {
			order = append(order, task.ID)
		}
		schedules[task.ID] = task
	}
	for _, id := range order {
		base.Schedules = append(base.Schedules, schedules[id])
	}
	return base
}

func WebConfig() WebNewsConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func SaveWebConfig(next WebNewsConfig) (WebNewsConfig, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = normalizeConfig(next)
	if err := saveConfigLocked(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func normalizeSource(src newsSource) newsSource {
	src.ID = sanitizeID(src.ID)
	src.Name = strings.TrimSpace(src.Name)
	src.Category = sanitizeID(src.Category)
	src.Desc = strings.TrimSpace(src.Desc)
	src.URL = strings.TrimSpace(src.URL)
	if u, err := url.Parse(src.URL); err != nil || u.Scheme == "" || u.Host == "" {
		src.URL = ""
	}
	src.Method = strings.ToUpper(strings.TrimSpace(src.Method))
	if src.Method == "" {
		src.Method = http.MethodGet
	}
	src.Encoding = strings.ToLower(strings.TrimSpace(src.Encoding))
	if src.Encoding == "" {
		src.Encoding = "json"
	}
	if src.Timeout <= 0 || src.Timeout > 120 {
		src.Timeout = 20
	}
	src.Enabled = !src.Disabled
	if src.Headers == nil {
		src.Headers = map[string]string{}
	}
	src.Commands = normalizeCommands(src.Commands)
	src.Params = normalizeParams(src.Params)
	return src
}

func normalizeParams(params []newsParam) []newsParam {
	out := make([]newsParam, 0, len(params))
	for _, param := range params {
		param.Name = sanitizeParamName(param.Name)
		param.Label = strings.TrimSpace(param.Label)
		param.Source = strings.ToLower(strings.TrimSpace(param.Source))
		if param.Source == "" {
			param.Source = "arg"
		}
		if param.Source != "arg" && param.Source != "rest" && param.Source != "default" {
			param.Source = "arg"
		}
		param.Default = strings.TrimSpace(param.Default)
		param.Placeholder = strings.TrimSpace(param.Placeholder)
		if param.Name == "" {
			continue
		}
		out = append(out, param)
	}
	return out
}

func normalizeAccess(in newsAccess) newsAccess {
	groupMode := normalizeAccessMode(in.GroupMode)
	privateMode := normalizeAccessMode(in.PrivateMode)
	zeroValue := !in.Enabled && !in.PrivateEnabled && groupMode == "none" && privateMode == "none" && len(in.GroupWhitelist) == 0 && len(in.GroupBlacklist) == 0 && len(in.PrivateWhitelist) == 0 && len(in.PrivateBlacklist) == 0
	if zeroValue {
		in.Enabled = true
		in.PrivateEnabled = true
	}
	return newsAccess{
		Enabled:          in.Enabled,
		PrivateEnabled:   in.PrivateEnabled,
		PrivateMode:      privateMode,
		PrivateWhitelist: normalizeIDs(in.PrivateWhitelist),
		PrivateBlacklist: normalizeIDs(in.PrivateBlacklist),
		GroupMode:        groupMode,
		GroupWhitelist:   normalizeIDs(in.GroupWhitelist),
		GroupBlacklist:   normalizeIDs(in.GroupBlacklist),
	}
}

func normalizeAccessMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "blacklist" && mode != "whitelist" {
		return "none"
	}
	return mode
}

func normalizeIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func normalizeSchedule(task newsSchedule, sources map[string]newsSource) newsSchedule {
	task.ID = sanitizeID(task.ID)
	task.SourceID = sanitizeID(task.SourceID)
	task.Target = strings.TrimSpace(task.Target)
	task.Time = strings.TrimSpace(task.Time)
	task.Cron = normalizeCronExpr(task.Cron)
	if task.Cron == "" && isClock(task.Time) {
		task.Cron = clockToCron(task.Time)
	}
	if task.Cron == "" || !cronExprOK(task.Cron) {
		task.Cron = ""
	}
	if isClock(task.Time) {
		task.Time = cronToClock(task.Cron, task.Time)
	} else if task.Cron != "" {
		task.Time = cronToClock(task.Cron, "")
	} else {
		task.Time = ""
	}
	if _, _, ok := parseTarget(task.Target); !ok {
		task.Target = ""
	}
	if src, ok := sources[task.SourceID]; ok {
		task.Format = formatFromEncoding(src.Encoding)
	} else {
		task.Format = strings.ToLower(strings.TrimSpace(task.Format))
		if !isSupportedFormat(task.Format) {
			task.Format = "image"
		}
	}
	return task
}

func mapToSources(m map[string]newsSource) []newsSource {
	out := make([]newsSource, 0, len(m))
	for _, src := range m {
		out = append(out, src)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func saveConfigLocked() error {
	_ = os.MkdirAll(filepath.Dir(cfgPath), 0755)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0600)
}

func saveConfig() error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return saveConfigLocked()
}

func parseFetchArgs(args []string) (sourceID, format string, rest []string) {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		switch {
		case arg == "":
		case isSupportedFormat(arg):
			format = strings.ToLower(arg)
		case sourceID == "":
			sourceID = sanitizeID(arg)
		default:
			rest = append(rest, arg)
		}
	}
	return
}

func parseSourceCommandArgs(args []string) (format string, rest []string) {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		switch {
		case arg == "":
		case isSupportedFormat(arg):
			format = strings.ToLower(arg)
		default:
			rest = append(rest, arg)
		}
	}
	return
}

func matchConfiguredCommand(text string) (sourceID, format string, args []string, ok bool) {
	cfgMu.RLock()
	commands := append([]string(nil), cfg.Commands...)
	sources := append([]newsSource(nil), cfg.Sources...)
	cfgMu.RUnlock()
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		if text == cmd {
			return "", "", nil, true
		}
		if strings.HasPrefix(text, cmd+" ") {
			sourceID, format, args = parseGlobalCommandArgs(strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, cmd))), sources)
			return sourceID, format, args, true
		}
	}
	for _, src := range sources {
		if !src.Enabled || src.Disabled {
			continue
		}
		for _, cmd := range src.Commands {
			cmd = strings.TrimSpace(cmd)
			if cmd == "" {
				continue
			}
			if text == cmd {
				return src.ID, "", nil, true
			}
			if strings.HasPrefix(text, cmd+" ") {
				raw := strings.TrimSpace(strings.TrimPrefix(text, cmd))
				fields := strings.Fields(raw)
				format, args = parseSourceCommandArgs(fields)
				return src.ID, format, args, true
			}
		}
	}
	return "", "", nil, false
}

func parseGlobalCommandArgs(fields []string, sources []newsSource) (sourceID, format string, args []string) {
	for _, field := range fields {
		field = strings.TrimSpace(field)
		switch {
		case field == "":
		case isSupportedFormat(field):
			format = strings.ToLower(field)
		case sourceID == "" && sourceIDExists(sources, field):
			sourceID = sanitizeID(field)
		default:
			args = append(args, field)
		}
	}
	return
}

func sourceIDExists(sources []newsSource, id string) bool {
	id = sanitizeID(id)
	for _, src := range sources {
		if src.ID == id {
			return true
		}
	}
	return false
}

func buildSourceParams(src newsSource, args []string) (map[string]string, error) {
	out := map[string]string{}
	rest := strings.TrimSpace(strings.Join(args, " "))
	argIndex := 0
	for _, param := range src.Params {
		name := sanitizeParamName(param.Name)
		if name == "" {
			continue
		}
		value := strings.TrimSpace(param.Default)
		switch strings.ToLower(strings.TrimSpace(param.Source)) {
		case "rest":
			if rest != "" {
				value = rest
				rest = ""
			}
		case "default":
		default:
			if argIndex < len(args) {
				value = strings.TrimSpace(args[argIndex])
				argIndex++
			}
		}
		if param.Required && value == "" {
			label := firstNonEmpty(param.Label, param.Name)
			return nil, fmt.Errorf("缺少参数: %s", label)
		}
		if value != "" {
			out[name] = value
		}
	}
	if len(src.Params) == 0 && rest != "" {
		out["query"] = rest
	}
	return out, nil
}

func sanitizeParamName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sendNews(ctx *zero.Ctx, sourceID, format string, args []string, target string) {
	msg, err := buildNewsMessage(sourceID, format, args)
	if err != nil {
		logrus.Warnf("[dailynews] fetch failed source=%s format=%s error=%v", sourceID, format, err)
		if target == "" {
			ctx.SendChain(message.Text("早报获取失败: ", err))
		}
		return
	}
	if target == "" {
		ctx.SendChain(msg...)
		return
	}
	sendToTarget(ctx, target, msg)
}

func buildNewsMessage(sourceID, format string, args []string) (message.Message, error) {
	cfgMu.RLock()
	local := cfg
	cfgMu.RUnlock()
	explicitSource := sourceID != ""
	if sourceID == "" {
		sourceID = local.DefaultSource
	}
	src, ok := findSource(local.Sources, sourceID)
	if !ok {
		return nil, fmt.Errorf("接口不存在: %s", sourceID)
	}
	if !src.Enabled || src.Disabled {
		return nil, fmt.Errorf("接口已关闭: %s", sourceID)
	}
	if format == "" {
		if explicitSource {
			format = formatFromEncoding(src.Encoding)
		} else {
			format = local.DefaultFormat
		}
	}
	params, err := buildSourceParams(src, args)
	if err != nil {
		return nil, err
	}
	data, contentType, err := fetchSource(src, format, params)
	if err != nil {
		return nil, err
	}
	return renderMessage(data, contentType, src, format)
}

func fetchSource(src newsSource, format string, params map[string]string) ([]byte, string, error) {
	requestURL, err := url.Parse(src.URL)
	if err != nil {
		return nil, "", err
	}
	for key, value := range params {
		placeholder := "{" + key + "}"
		if strings.Contains(requestURL.Path, placeholder) {
			requestURL.Path = strings.ReplaceAll(requestURL.Path, placeholder, url.PathEscape(value))
			delete(params, key)
		}
	}
	query := requestURL.Query()
	for key, value := range params {
		if value != "" && strings.ToUpper(src.Method) != http.MethodPost {
			query.Set(key, value)
		}
	}
	encoding := sourceEncoding(src, format)
	if encoding != "" && strings.Contains(requestURL.Host, "744524299.xyz") {
		query.Set("encoding", encoding)
	}
	requestURL.RawQuery = query.Encode()

	var body io.Reader
	if strings.ToUpper(src.Method) == http.MethodPost {
		payload := map[string]string{}
		for key, value := range params {
			if value != "" {
				payload[key] = value
			}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, "", err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(src.Method, requestURL.String(), body)
	if err != nil {
		return nil, "", err
	}
	if strings.ToUpper(src.Method) == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range src.Headers {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
	client := &http.Client{Timeout: time.Duration(src.Timeout) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func sourceEncoding(src newsSource, format string) string {
	switch strings.ToLower(format) {
	case "image":
		if src.Encoding == "image" {
			return "image"
		}
		return "image-proxy"
	case "text", "markdown", "json":
		return strings.ToLower(format)
	default:
		return src.Encoding
	}
}

func formatFromEncoding(encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "text":
		return "text"
	case "markdown":
		return "markdown"
	case "json":
		return "json"
	case "image", "image-proxy":
		return "image"
	default:
		return "text"
	}
}

func renderMessage(data []byte, contentType string, src newsSource, format string) (message.Message, error) {
	if format == "image" || strings.Contains(contentType, "image/") || looksLikeImage(data) {
		path, err := saveImage(data, src.ID, contentType)
		if err != nil {
			return nil, err
		}
		return message.Message{message.Image(dailyNewsLocalMediaTarget(path))}, nil
	}
	if format == "json" || strings.Contains(contentType, "application/json") || json.Valid(data) {
		text := formatJSONNews(data)
		return message.Message{message.Text(text)}, nil
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, errors.New("接口返回为空")
	}
	return message.Message{message.Text(text)}, nil
}

func saveImage(data []byte, sourceID, contentType string) (string, error) {
	if cacheDir == "" {
		cacheDir = dailyNewsCacheDir()
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	id := sanitizeID(firstNonEmpty(sourceID, "image"))
	name := fmt.Sprintf("%s_%d%s", id, time.Now().UnixNano(), imageExt(contentType, data))
	path := filepath.Join(cacheDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func imageExt(contentType string, data []byte) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch ct {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return ".webp"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return ".gif"
	}
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8 {
		return ".jpg"
	}
	return ".png"
}

func dailyNewsCacheDir() string {
	return filepath.Join(dailyNewsPluginDataRoot(), "mediaparser", "cache", "dailynews")
}

func dailyNewsLocalMediaTarget(path string) string {
	if target := dailyNewsMappedFileURI(path); target != "" {
		return target
	}
	return dailyNewsFileURI(path)
}

func dailyNewsMappedFileURI(path string) string {
	target := dailyNewsMappedLocalPath(path)
	if target == "" {
		return ""
	}
	return dailyNewsFileURI(target)
}

func dailyNewsMappedLocalPath(path string) string {
	dataDir := dailyNewsOneBotDataDir()
	if dataDir == "" {
		return ""
	}
	localAbs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	appDataAbs, err := filepath.Abs(dailyNewsAppDataRoot())
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(appDataAbs, localAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.Join(dataDir, rel)
}

func dailyNewsAppDataRoot() string {
	if cacheDir != "" {
		return filepath.Clean(filepath.Join(cacheDir, "..", "..", ".."))
	}
	return dailyNewsPluginDataRoot()
}

func dailyNewsPluginDataRoot() string {
	dir := filepath.Clean(engine.DataFolder())
	for {
		if filepath.Base(dir) == "data" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Dir(engine.DataFolder())
		}
		dir = parent
	}
}

func dailyNewsOneBotDataDir() string {
	b, err := os.ReadFile(filepath.Join(dailyNewsAppDataRoot(), "mediaparser", "system.json"))
	if err != nil {
		return ""
	}
	var sys struct {
		OneBotDataDir string `json:"onebot_data_dir,omitempty"`
	}
	if err := json.Unmarshal(b, &sys); err != nil {
		return ""
	}
	return strings.TrimSpace(sys.OneBotDataDir)
}

func dailyNewsFileURI(path string) string {
	if slashPath := filepath.ToSlash(path); strings.HasPrefix(slashPath, "/") && !strings.HasPrefix(slashPath, "//") {
		return "file://" + slashPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:///" + strings.TrimPrefix(filepath.ToSlash(abs), "/")
}

func formatJSONNews(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return strings.TrimSpace(string(data))
	}
	if text, ok := formatKnownJSONPayload(v); ok {
		return text
	}
	return formatGenericJSONValue(unwrapAPIData(v))
}

func formatKnownJSONPayload(v any) (string, bool) {
	if m, ok := v.(map[string]any); ok {
		if data, ok := m["data"]; ok {
			if text, ok := formatKnownJSONPayload(data); ok {
				return text, true
			}
		}
		if text, ok := formatDailyNewsMap(m); ok {
			return text, true
		}
		if text := firstJSONText(m, "duanzi", "joke", "hitokoto", "content", "text", "saying", "sentence", "answer", "result"); text != "" {
			return text, true
		}
	}
	return "", false
}

func formatDailyNewsMap(m map[string]any) (string, bool) {
	items := jsonNewsItems(m["news"])
	if len(items) == 0 {
		return "", false
	}
	var b strings.Builder
	if date := valueToText(m["date"]); date != "" {
		b.WriteString(date)
		if day := firstNonEmpty(valueToText(m["day_of_week"]), valueToText(m["day"])); day != "" {
			b.WriteString(" ")
			b.WriteString(day)
		}
		if lunar := valueToText(m["lunar_date"]); lunar != "" {
			b.WriteString(" ")
			b.WriteString(lunar)
		}
		b.WriteString("\n")
	}
	for i, item := range items {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	if tip := valueToText(m["tip"]); tip != "" {
		b.WriteString("\n")
		b.WriteString(tip)
	}
	return strings.TrimSpace(b.String()), true
}

func jsonNewsItems(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch x := item.(type) {
		case string:
			if text := strings.TrimSpace(x); text != "" {
				out = append(out, text)
			}
		case map[string]any:
			if text := firstJSONText(x, "title", "name", "content", "text"); text != "" {
				out = append(out, text)
			}
		default:
			if text := valueToText(item); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func formatGenericJSON(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return strings.TrimSpace(string(data))
	}
	return formatGenericJSONValue(unwrapAPIData(v))
}

func formatGenericJSONValue(v any) string {
	lines := make([]string, 0, 24)
	collectJSONLines("", v, &lines, 0)
	if len(lines) == 0 {
		raw, _ := json.Marshal(v)
		return strings.TrimSpace(string(raw))
	}
	if len(lines) > 24 {
		lines = lines[:24]
		lines = append(lines, "...")
	}
	return strings.Join(lines, "\n")
}

func unwrapAPIData(v any) any {
	if m, ok := v.(map[string]any); ok {
		if data, ok := m["data"]; ok {
			return data
		}
	}
	return v
}

func collectJSONLines(prefix string, v any, lines *[]string, depth int) {
	if len(*lines) >= 24 || depth > 3 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if title := firstJSONText(x, "title", "name", "duanzi", "joke", "hitokoto", "content", "text", "location", "date"); title != "" && prefix == "" {
			*lines = append(*lines, title)
		}
		if data, ok := x["data"]; ok {
			collectJSONLines(prefix, data, lines, depth+1)
			return
		}
		for _, key := range []string{"result", "summary", "weather", "temperature", "humidity", "wind", "air_quality", "tip", "url", "update_time", "updated"} {
			if val := valueToText(x[key]); val != "" {
				label := key
				if prefix != "" {
					label = prefix + "." + key
				}
				*lines = append(*lines, label+": "+val)
			}
		}
		if len(*lines) <= 1 {
			keys := make([]string, 0, len(x))
			for key := range x {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if val := valueToText(x[key]); val != "" {
					*lines = append(*lines, key+": "+val)
				}
				if len(*lines) >= 24 {
					return
				}
			}
		}
	case []any:
		for i, item := range x {
			if len(*lines) >= 24 {
				return
			}
			if m, ok := item.(map[string]any); ok {
				title := firstJSONText(m, "title", "name", "song", "keyword", "content")
				if title == "" {
					title = valueToText(item)
				}
				if title != "" {
					*lines = append(*lines, fmt.Sprintf("%d. %s", i+1, title))
				}
				continue
			}
			if text := valueToText(item); text != "" {
				*lines = append(*lines, fmt.Sprintf("%d. %s", i+1, text))
			}
		}
	default:
		if text := valueToText(v); text != "" {
			*lines = append(*lines, text)
		}
	}
}

func firstJSONText(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := valueToText(m[key]); text != "" {
			return text
		}
	}
	return ""
}

func valueToText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func looksLikeImage(data []byte) bool {
	return len(data) >= 8 && ((data[0] == 0x89 && string(data[1:4]) == "PNG") || (data[0] == 0xff && data[1] == 0xd8))
}

func sendToTarget(ctx *zero.Ctx, target string, msg message.Message) {
	kind, id, ok := parseTarget(target)
	if !ok {
		logrus.Warnf("[dailynews] invalid schedule target=%s", target)
		return
	}
	if kind == "group" {
		ctx.SendGroupMessage(id, msg)
		return
	}
	ctx.SendPrivateMessage(id, msg)
}

func allowAccess(ctx *zero.Ctx) bool {
	cfgMu.RLock()
	access := cfg.Access
	cfgMu.RUnlock()
	if !access.Enabled {
		return false
	}
	if ctx.Event.GroupID == 0 {
		if !access.PrivateEnabled {
			return false
		}
		uid := ctx.Event.UserID
		switch access.PrivateMode {
		case "whitelist":
			return containsID(access.PrivateWhitelist, uid)
		case "blacklist":
			return !containsID(access.PrivateBlacklist, uid)
		default:
			return true
		}
	}
	gid := ctx.Event.GroupID
	switch access.GroupMode {
	case "whitelist":
		return containsID(access.GroupWhitelist, gid)
	case "blacklist":
		return !containsID(access.GroupBlacklist, gid)
	default:
		return true
	}
}

func containsID(ids []int64, id int64) bool {
	for _, item := range ids {
		if item == id {
			return true
		}
	}
	return false
}

func startScheduler() {
	schedulerOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				runDueSchedules()
			}
		}()
	})
}

func runDueSchedules() {
	now := time.Now()
	stamp := now.Format("2006-01-02 15:04")
	today := now.Format("2006-01-02")
	cfgMu.Lock()
	tasks := make([]newsSchedule, 0, len(cfg.Schedules))
	changed := false
	for i := range cfg.Schedules {
		task := cfg.Schedules[i]
		if !task.Enabled || !scheduleMatches(task, now) || scheduleAlreadyRan(task, stamp, today) {
			continue
		}
		cfg.Schedules[i].LastRun = stamp
		task.LastRun = stamp
		tasks = append(tasks, task)
		changed = true
	}
	if changed {
		_ = saveConfigLocked()
	}
	cfgMu.Unlock()
	if len(tasks) == 0 {
		return
	}
	var bot *zero.Ctx
	zero.RangeBot(func(_ int64, ctx *zero.Ctx) bool {
		bot = ctx
		return false
	})
	if bot == nil {
		logrus.Warn("[dailynews] no bot available for schedules")
		return
	}
	for _, task := range tasks {
		cfgMu.RLock()
		src, ok := findSource(cfg.Sources, task.SourceID)
		cfgMu.RUnlock()
		if !ok || !src.Enabled || src.Disabled {
			logrus.Warnf("[dailynews] schedule skip source_unavailable id=%s source=%s target=%s", task.ID, task.SourceID, task.Target)
			continue
		}
		logrus.Infof("[dailynews] schedule due id=%s source=%s format=%s target=%s cron=%s", task.ID, task.SourceID, firstNonEmpty(task.Format, "auto"), task.Target, task.Cron)
		sendNews(bot, task.SourceID, task.Format, nil, task.Target)
	}
}

func handleAddSource(ctx *zero.Ctx) {
	fields := strings.Fields(ctx.State["args"].(string))
	if len(fields) < 3 {
		ctx.SendChain(message.Text("格式: 60秒接口添加 ID 名称 URL [格式]"))
		return
	}
	src := newsSource{ID: fields[0], Name: fields[1], URL: fields[2], Method: http.MethodGet, Encoding: "json", Timeout: 20}
	if len(fields) >= 4 {
		src.Encoding = strings.ToLower(fields[3])
	}
	src = normalizeSource(src)
	if src.ID == "" || src.URL == "" {
		ctx.SendChain(message.Text("接口ID或URL不合法"))
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	replaced := false
	for i := range cfg.Sources {
		if cfg.Sources[i].ID == src.ID {
			if cfg.Sources[i].Builtin {
				ctx.SendChain(message.Text("内置接口不能覆盖"))
				return
			}
			cfg.Sources[i] = src
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Sources = append(cfg.Sources, src)
	}
	cfg = normalizeConfig(cfg)
	if err := saveConfigLocked(); err != nil {
		ctx.SendChain(message.Text("保存失败: ", err))
		return
	}
	ctx.SendChain(message.Text("已保存接口: ", src.ID))
}

func handleDeleteSource(ctx *zero.Ctx) {
	id := sanitizeID(ctx.State["args"].(string))
	if id == "" {
		ctx.SendChain(message.Text("格式: 60秒接口删除 ID"))
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	out := cfg.Sources[:0]
	deleted := false
	for _, src := range cfg.Sources {
		if src.ID == id {
			if src.Builtin {
				ctx.SendChain(message.Text("内置接口不能删除"))
				return
			}
			deleted = true
			continue
		}
		out = append(out, src)
	}
	cfg.Sources = out
	if cfg.DefaultSource == id {
		cfg.DefaultSource = defaultSourceID
	}
	if deleted {
		cfg = normalizeConfig(cfg)
		_ = saveConfigLocked()
		ctx.SendChain(message.Text("已删除接口: ", id))
		return
	}
	ctx.SendChain(message.Text("接口不存在: ", id))
}

func handleListSources(ctx *zero.Ctx) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	var b strings.Builder
	b.WriteString("60秒接口列表:\n")
	for _, src := range cfg.Sources {
		mark := ""
		if src.ID == cfg.DefaultSource {
			mark = " 默认"
		}
		if src.Builtin {
			mark += " 内置"
		}
		b.WriteString(fmt.Sprintf("- %s %s [%s]%s\n", src.ID, src.Name, src.Encoding, mark))
	}
	ctx.SendChain(message.Text(strings.TrimSpace(b.String())))
}

func handleAddSchedule(ctx *zero.Ctx) {
	fields := strings.Fields(ctx.State["args"].(string))
	if len(fields) < 4 {
		ctx.SendChain(message.Text("格式: 60秒定时添加 ID 接口ID 群:123456 30 8 * * * [格式]"))
		return
	}
	scheduleExpr := fields[3]
	formatIndex := 4
	if len(fields) >= 8 && !isSupportedFormat(fields[4]) {
		scheduleExpr = strings.Join(fields[3:8], " ")
		formatIndex = 8
	}
	task := newsSchedule{ID: fields[0], SourceID: fields[1], Target: fields[2], Time: scheduleExpr, Cron: scheduleExpr, Format: "image", Enabled: true}
	if len(fields) > formatIndex {
		task.Format = fields[formatIndex]
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	sources := sourcesByID(cfg.Sources)
	if _, ok := sources[sanitizeID(task.SourceID)]; !ok {
		ctx.SendChain(message.Text("接口不存在: ", task.SourceID))
		return
	}
	task = normalizeSchedule(task, sources)
	if task.ID == "" || task.Cron == "" {
		ctx.SendChain(message.Text("定时ID或时间不合法"))
		return
	}
	if _, _, ok := parseTarget(task.Target); !ok {
		ctx.SendChain(message.Text("目标格式: 群:123456 或 私聊:123456"))
		return
	}
	replaced := false
	for i := range cfg.Schedules {
		if cfg.Schedules[i].ID == task.ID {
			cfg.Schedules[i] = task
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Schedules = append(cfg.Schedules, task)
	}
	_ = saveConfigLocked()
	ctx.SendChain(message.Text("已保存定时: ", task.ID))
}

func handleDeleteSchedule(ctx *zero.Ctx) {
	id := sanitizeID(ctx.State["args"].(string))
	cfgMu.Lock()
	defer cfgMu.Unlock()
	out := cfg.Schedules[:0]
	deleted := false
	for _, task := range cfg.Schedules {
		if task.ID == id {
			deleted = true
			continue
		}
		out = append(out, task)
	}
	cfg.Schedules = out
	if deleted {
		_ = saveConfigLocked()
		ctx.SendChain(message.Text("已删除定时: ", id))
		return
	}
	ctx.SendChain(message.Text("定时不存在: ", id))
}

func handleListSchedules(ctx *zero.Ctx) {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	if len(cfg.Schedules) == 0 {
		ctx.SendChain(message.Text("暂无定时"))
		return
	}
	var b strings.Builder
	b.WriteString("60秒定时列表:\n")
	for _, task := range cfg.Schedules {
		state := "关闭"
		if task.Enabled {
			state = "开启"
		}
		expr := task.Cron
		if expr == "" {
			expr = task.Time
		}
		b.WriteString(fmt.Sprintf("- %s %s %s %s %s\n", task.ID, task.SourceID, task.Target, expr, state))
	}
	ctx.SendChain(message.Text(strings.TrimSpace(b.String())))
}

func handleDefaultSource(ctx *zero.Ctx) {
	id := sanitizeID(ctx.State["args"].(string))
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if _, ok := findSource(cfg.Sources, id); !ok {
		ctx.SendChain(message.Text("接口不存在: ", id))
		return
	}
	cfg.DefaultSource = id
	_ = saveConfigLocked()
	ctx.SendChain(message.Text("默认接口已设为: ", id))
}

func handleDefaultFormat(ctx *zero.Ctx) {
	format := strings.ToLower(strings.TrimSpace(ctx.State["args"].(string)))
	if !isSupportedFormat(format) {
		ctx.SendChain(message.Text("格式支持: image text markdown json"))
		return
	}
	cfgMu.Lock()
	cfg.DefaultFormat = format
	_ = saveConfigLocked()
	cfgMu.Unlock()
	ctx.SendChain(message.Text("默认格式已设为: ", format))
}

func findSource(sources []newsSource, id string) (newsSource, bool) {
	id = sanitizeID(id)
	for _, src := range sources {
		if src.ID == id {
			return src, true
		}
	}
	return newsSource{}, false
}

func sourcesByID(sources []newsSource) map[string]newsSource {
	out := make(map[string]newsSource, len(sources))
	for _, src := range sources {
		out[src.ID] = src
	}
	return out
}

func parseTarget(target string) (string, int64, bool) {
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	switch strings.TrimSpace(parts[0]) {
	case "群", "group":
		return "group", id, true
	case "私聊", "private":
		return "private", id, true
	default:
		return "", 0, false
	}
}

func normalizeCommands(commands []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(commands))
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append(out, cmd)
	}
	return out
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isSupportedFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "image", "text", "markdown", "json":
		return true
	default:
		return false
	}
}

func isDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func isClock(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}

func clockToCron(clock string) string {
	t, err := time.Parse("15:04", strings.TrimSpace(clock))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour())
}

func cronToClock(expr, fallback string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		if isClock(fallback) {
			return fallback
		}
		return ""
	}
	minute, err1 := strconv.Atoi(fields[0])
	hour, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		if isClock(fallback) {
			return fallback
		}
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func normalizeCronExpr(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	if isClock(expr) {
		return clockToCron(expr)
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return ""
	}
	for i, field := range fields {
		field = strings.TrimSpace(field)
		if field == "?" {
			field = "*"
		}
		fields[i] = field
	}
	return strings.Join(fields, " ")
}

func cronExprOK(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if !cronFieldOK(field, ranges[i][0], ranges[i][1]) {
			return false
		}
	}
	return true
}

func cronFieldOK(field string, min, max int) bool {
	if field == "" {
		return false
	}
	for _, part := range strings.Split(field, ",") {
		if _, ok := cronPartValues(part, min, max); !ok {
			return false
		}
	}
	return true
}

func scheduleMatches(task newsSchedule, now time.Time) bool {
	expr := task.Cron
	if expr == "" {
		expr = clockToCron(task.Time)
	}
	expr = normalizeCronExpr(expr)
	if !cronExprOK(expr) {
		return false
	}
	fields := strings.Fields(expr)
	values := []int{now.Minute(), now.Hour(), now.Day(), int(now.Month()), int(now.Weekday())}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if !cronFieldMatches(field, ranges[i][0], ranges[i][1], values[i]) {
			if i == 4 && values[i] == 0 && cronFieldMatches(field, ranges[i][0], ranges[i][1], 7) {
				continue
			}
			return false
		}
	}
	return true
}

func scheduleAlreadyRan(task newsSchedule, stamp, today string) bool {
	if task.LastRun == stamp {
		return true
	}
	return task.LastRun == today && task.Cron == clockToCron(task.Time)
}

func cronFieldMatches(field string, min, max, value int) bool {
	for _, part := range strings.Split(field, ",") {
		values, ok := cronPartValues(part, min, max)
		if !ok {
			return false
		}
		if values[value] {
			return true
		}
	}
	return false
}

func cronPartValues(part string, min, max int) (map[int]bool, bool) {
	part = strings.TrimSpace(part)
	if part == "" {
		return nil, false
	}
	step := 1
	if strings.Contains(part, "/") {
		pieces := strings.Split(part, "/")
		if len(pieces) != 2 {
			return nil, false
		}
		part = pieces[0]
		n, err := strconv.Atoi(pieces[1])
		if err != nil || n <= 0 {
			return nil, false
		}
		step = n
	}
	start, end := min, max
	switch {
	case part == "*" || part == "?":
	case strings.Contains(part, "-"):
		pieces := strings.Split(part, "-")
		if len(pieces) != 2 {
			return nil, false
		}
		a, errA := strconv.Atoi(pieces[0])
		b, errB := strconv.Atoi(pieces[1])
		if errA != nil || errB != nil || a > b {
			return nil, false
		}
		start, end = a, b
	default:
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		start, end = n, n
	}
	if start < min || end > max {
		return nil, false
	}
	values := make(map[int]bool, end-start+1)
	for i := start; i <= end; i += step {
		values[i] = true
	}
	return values, true
}
