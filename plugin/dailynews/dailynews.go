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
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Encoding string            `json:"encoding"`
	Headers  map[string]string `json:"headers,omitempty"`
	Timeout  int               `json:"timeout_seconds,omitempty"`
	Builtin  bool              `json:"builtin,omitempty"`
}

type newsSchedule struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Target   string `json:"target"`
	Time     string `json:"time"`
	Format   string `json:"format"`
	Enabled  bool   `json:"enabled"`
	LastRun  string `json:"last_run,omitempty"`
}

type newsConfig struct {
	DefaultSource string         `json:"default_source"`
	DefaultFormat string         `json:"default_format"`
	Sources       []newsSource   `json:"sources"`
	Schedules     []newsSchedule `json:"schedules"`
}

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
	cacheDir = filepath.Join(engine.DataFolder(), "cache")
	_ = os.MkdirAll(cacheDir, 0755)
	loadConfig()
	startScheduler()

	engine.OnFullMatchGroup([]string{"今日早报", "60秒读懂世界", "每天60秒读懂世界"}).SetBlock(true).
		Handle(func(ctx *zero.Ctx) {
			sendNews(ctx, "", "", "", "")
		})
	engine.OnPrefixGroup([]string{"60秒早报", "60s早报", "今日早报"}).SetBlock(true).
		Handle(func(ctx *zero.Ctx) {
			args := strings.Fields(ctx.State["args"].(string))
			sourceID, format, date := parseFetchArgs(args)
			sendNews(ctx, sourceID, format, date, "")
		})
	engine.OnPrefix("60秒接口添加", zero.AdminPermission).SetBlock(true).Handle(handleAddSource)
	engine.OnPrefix("60秒接口删除", zero.AdminPermission).SetBlock(true).Handle(handleDeleteSource)
	engine.OnFullMatch("60秒接口列表", zero.AdminPermission).SetBlock(true).Handle(handleListSources)
	engine.OnPrefix("60秒定时添加", zero.AdminPermission).SetBlock(true).Handle(handleAddSchedule)
	engine.OnPrefix("60秒定时删除", zero.AdminPermission).SetBlock(true).Handle(handleDeleteSchedule)
	engine.OnFullMatch("60秒定时列表", zero.AdminPermission).SetBlock(true).Handle(handleListSchedules)
	engine.OnPrefix("60秒默认接口", zero.AdminPermission).SetBlock(true).Handle(handleDefaultSource)
	engine.OnPrefix("60秒默认格式", zero.AdminPermission).SetBlock(true).Handle(handleDefaultFormat)
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
		"- 60秒定时添加 ID 接口ID 群:123456 08:30 [格式]",
		"- 60秒定时删除 ID",
		"- 60秒定时列表",
	}, "\n")
}

func defaultConfig() newsConfig {
	return newsConfig{
		DefaultSource: defaultSourceID,
		DefaultFormat: "image",
		Sources: []newsSource{
			{ID: defaultSourceID, Name: "60s API", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true},
			{ID: "60s-text", Name: "60s 文本", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "text", Timeout: 20, Builtin: true},
			{ID: "60s-markdown", Name: "60s Markdown", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "markdown", Timeout: 20, Builtin: true},
			{ID: "60s-image", Name: "60s 图片跳转", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "image", Timeout: 20, Builtin: true},
			{ID: "60s-image-proxy", Name: "60s 图片代理", URL: defaultBaseURL, Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true},
			{ID: "legacy-image", Name: "旧版早报图片", URL: legacyImageAPI, Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true},
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
		}
		merged[src.ID] = src
	}
	base.Sources = mapToSources(merged)
	if _, ok := merged[base.DefaultSource]; !ok {
		base.DefaultSource = defaultSourceID
	}
	for _, task := range in.Schedules {
		task = normalizeSchedule(task)
		if task.ID == "" || task.SourceID == "" || task.Target == "" || task.Time == "" {
			continue
		}
		if _, ok := merged[task.SourceID]; !ok {
			continue
		}
		base.Schedules = append(base.Schedules, task)
	}
	return base
}

func normalizeSource(src newsSource) newsSource {
	src.ID = sanitizeID(src.ID)
	src.Name = strings.TrimSpace(src.Name)
	src.URL = strings.TrimSpace(src.URL)
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
	if src.Headers == nil {
		src.Headers = map[string]string{}
	}
	return src
}

func normalizeSchedule(task newsSchedule) newsSchedule {
	task.ID = sanitizeID(task.ID)
	task.SourceID = sanitizeID(task.SourceID)
	task.Target = strings.TrimSpace(task.Target)
	task.Time = strings.TrimSpace(task.Time)
	task.Format = strings.ToLower(strings.TrimSpace(task.Format))
	if !isSupportedFormat(task.Format) {
		task.Format = "image"
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

func parseFetchArgs(args []string) (sourceID, format, date string) {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		switch {
		case arg == "":
		case isSupportedFormat(arg):
			format = strings.ToLower(arg)
		case isDate(arg):
			date = arg
		default:
			sourceID = sanitizeID(arg)
		}
	}
	return
}

func sendNews(ctx *zero.Ctx, sourceID, format, date, target string) {
	msg, err := buildNewsMessage(sourceID, format, date)
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

func buildNewsMessage(sourceID, format, date string) (message.Message, error) {
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
	if format == "" {
		if explicitSource {
			format = formatFromEncoding(src.Encoding)
		} else {
			format = local.DefaultFormat
		}
	}
	data, contentType, err := fetchSource(src, format, date)
	if err != nil {
		return nil, err
	}
	return renderMessage(data, contentType, src, format)
}

func fetchSource(src newsSource, format, date string) ([]byte, string, error) {
	requestURL, err := url.Parse(src.URL)
	if err != nil {
		return nil, "", err
	}
	query := requestURL.Query()
	if date != "" {
		query.Set("date", date)
	}
	encoding := sourceEncoding(src, format)
	if encoding != "" && strings.Contains(requestURL.Host, "744524299.xyz") {
		query.Set("encoding", encoding)
	}
	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequest(src.Method, requestURL.String(), nil)
	if err != nil {
		return nil, "", err
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
		path, err := saveImage(data, src.ID)
		if err != nil {
			return nil, err
		}
		return message.Message{message.Image("file:///" + filepath.ToSlash(path))}, nil
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

func formatJSONNews(data []byte) string {
	var resp newsAPIResponse
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.News) == 0 {
		return string(data)
	}
	var b strings.Builder
	if resp.Date != "" {
		b.WriteString(resp.Date)
		if resp.Day != "" {
			b.WriteString(" ")
			b.WriteString(resp.Day)
		}
		if resp.LunarDate != "" {
			b.WriteString(" ")
			b.WriteString(resp.LunarDate)
		}
		b.WriteString("\n")
	}
	for i, item := range resp.News {
		if strings.TrimSpace(item.Title) == "" {
			continue
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(strings.TrimSpace(item.Title))
		b.WriteString("\n")
	}
	if resp.Tip != "" {
		b.WriteString("\n")
		b.WriteString(resp.Tip)
	}
	return strings.TrimSpace(b.String())
}

func saveImage(data []byte, sourceID string) (string, error) {
	ext := ".png"
	if len(data) > 2 && data[0] == 0xff && data[1] == 0xd8 {
		ext = ".jpg"
	}
	name := fmt.Sprintf("%s_%d%s", sanitizeID(sourceID), time.Now().UnixNano(), ext)
	path := filepath.Join(cacheDir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
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
	hm := now.Format("15:04")
	today := now.Format("2006-01-02")
	cfgMu.Lock()
	tasks := make([]newsSchedule, 0, len(cfg.Schedules))
	changed := false
	for i := range cfg.Schedules {
		task := cfg.Schedules[i]
		if !task.Enabled || task.Time != hm || task.LastRun == today {
			continue
		}
		cfg.Schedules[i].LastRun = today
		task.LastRun = today
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
		sendNews(bot, task.SourceID, task.Format, "", task.Target)
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
		ctx.SendChain(message.Text("格式: 60秒定时添加 ID 接口ID 群:123456 08:30 [格式]"))
		return
	}
	task := newsSchedule{ID: fields[0], SourceID: fields[1], Target: fields[2], Time: fields[3], Format: "image", Enabled: true}
	if len(fields) >= 5 {
		task.Format = fields[4]
	}
	task = normalizeSchedule(task)
	if task.ID == "" || !isClock(task.Time) {
		ctx.SendChain(message.Text("定时ID或时间不合法"))
		return
	}
	if _, _, ok := parseTarget(task.Target); !ok {
		ctx.SendChain(message.Text("目标格式: 群:123456 或 私聊:123456"))
		return
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if _, ok := findSource(cfg.Sources, task.SourceID); !ok {
		ctx.SendChain(message.Text("接口不存在: ", task.SourceID))
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
		b.WriteString(fmt.Sprintf("- %s %s %s %s %s\n", task.ID, task.SourceID, task.Target, task.Time, state))
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
