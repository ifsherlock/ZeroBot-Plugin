package mediaparser

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FloatTech/ZeroBot-Plugin/plugin/dailynews"
	"github.com/FloatTech/ZeroBot-Plugin/plugin/deerpipe"
	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
)

type WebStatusProvider func() map[string]any

type webAuthConfig struct {
	User      string
	Password  string
	StorePath string
	Enabled   bool
	Store     webAuthStore
}

type webAuthStore struct {
	User       string `json:"user"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

type webPlatform struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Local string `json:"local"`
}

type webGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type webDailyNewsSource struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Category string              `json:"category,omitempty"`
	Desc     string              `json:"desc,omitempty"`
	URL      string              `json:"url"`
	Method   string              `json:"method"`
	Encoding string              `json:"encoding"`
	Headers  map[string]string   `json:"headers,omitempty"`
	Params   []webDailyNewsParam `json:"params,omitempty"`
	Commands []string            `json:"commands,omitempty"`
	Timeout  int                 `json:"timeout_seconds,omitempty"`
	Enabled  bool                `json:"enabled"`
	Disabled bool                `json:"disabled,omitempty"`
	Builtin  bool                `json:"builtin,omitempty"`
}

type webDailyNewsParam struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Source      string `json:"source,omitempty"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type webDailyNewsSchedule struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Target   string `json:"target"`
	Time     string `json:"time"`
	Cron     string `json:"cron,omitempty"`
	Format   string `json:"format"`
	Enabled  bool   `json:"enabled"`
	LastRun  string `json:"last_run,omitempty"`
}

type webDailyNewsConfig struct {
	DefaultSource string                 `json:"default_source"`
	DefaultFormat string                 `json:"default_format"`
	Commands      []string               `json:"commands"`
	Sources       []webDailyNewsSource   `json:"sources"`
	Schedules     []webDailyNewsSchedule `json:"schedules"`
	Access        webDailyNewsAccess     `json:"access"`
}

type webDailyNewsAccess struct {
	Enabled          bool    `json:"enabled"`
	PrivateEnabled   bool    `json:"private_enabled"`
	PrivateMode      string  `json:"private_mode"`
	PrivateWhitelist []int64 `json:"private_whitelist,omitempty"`
	PrivateBlacklist []int64 `json:"private_blacklist,omitempty"`
	GroupMode        string  `json:"group_mode"`
	GroupWhitelist   []int64 `json:"group_whitelist,omitempty"`
	GroupBlacklist   []int64 `json:"group_blacklist,omitempty"`
}

type webLogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type webLogHook struct {
	mu      sync.Mutex
	entries []webLogEntry
	limit   int
}

var (
	webLogs     = &webLogHook{limit: 240}
	webLogsOnce sync.Once
	webUIMu     sync.Mutex
	webUIActive bool
)

func (h *webLogHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *webLogHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, webLogEntry{
		Time:    entry.Time.Format("2006-01-02 15:04:05"),
		Level:   entry.Level.String(),
		Message: redactWebLog(entry.Message),
	})
	if len(h.entries) > h.limit {
		h.entries = append([]webLogEntry(nil), h.entries[len(h.entries)-h.limit:]...)
	}
	return nil
}

func (h *webLogHook) Snapshot(limit int) []webLogEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit <= 0 || limit > len(h.entries) {
		limit = len(h.entries)
	}
	out := make([]webLogEntry, 0, limit)
	for i := len(h.entries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, h.entries[i])
	}
	return out
}

func redactWebLog(s string) string {
	for _, key := range []string{"Cookie:", "cookie:", "SESSDATA=", "sessionid=", "Authorization:", "Bearer "} {
		if idx := strings.Index(s, key); idx >= 0 {
			return strings.TrimSpace(s[:idx]) + key + "[redacted]"
		}
	}
	s = regexp.MustCompile(`(?i)(access_token|token|secret|appsecret|openid|sessionid|sessdata)=([^&\s]+)`).ReplaceAllString(s, "$1=[redacted]")
	s = regexp.MustCompile(`(?i)\b(user_id|group_id|uin|qq)=\d+`).ReplaceAllString(s, "$1=[redacted]")
	return s
}

func StartWebUI(addr string, extra WebStatusProvider) {
	addr = strings.TrimSpace(addr)
	if addr == "" || addr == "off" || addr == "0" {
		return
	}
	webUIMu.Lock()
	if webUIActive {
		webUIMu.Unlock()
		logrus.Infof("[webui] already running, skip start addr=%s", addr)
		return
	}
	webUIActive = true
	webUIMu.Unlock()
	webLogsOnce.Do(func() { logrus.AddHook(webLogs) })
	mux := http.NewServeMux()
	mux.HandleFunc("/", serveWebIndex)
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		payload := map[string]any{
			"ok":             true,
			"now":            time.Now(),
			"go":             runtime.Version(),
			"runtime_status": snapshotRuntime(),
		}
		if extra != nil {
			payload["bot"] = extra()
		} else {
			payload["bot"] = defaultWebStatus()
		}
		writeJSON(w, payload)
	})
	mux.HandleFunc("/api/mediaparser/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"config": configForWeb(), "platforms": webPlatforms(), "safety_builtins": safetyBuiltinPayload()})
		case http.MethodPost:
			var next config
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			preserveSecretConfigFields(&next, snapshotConfig())
			normalizeConfig(&next)
			stateMu.Lock()
			currentConf = next
			err := saveConfigLocked()
			stateMu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "config": configForWeb()})
		default:
			writeMethodNotAllowed(w)
		}
	})
	mux.HandleFunc("/api/system/settings", serveSystemSettingsAPI)
	mux.HandleFunc("/api/system/auth", serveWebAuthAPI)
	mux.HandleFunc("/api/system/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": "restarting"})
		logrus.Infof("[webui] restart requested from WebUI")
		go func() {
			time.Sleep(350 * time.Millisecond)
			os.Exit(0)
		}()
	})
	mux.HandleFunc("/api/mediaparser/cache/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		n, err := cleanCache()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "removed": n})
	})
	mux.HandleFunc("/api/mediaparser/cache/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w)
			return
		}
		files, bytes, err := cacheStats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "files": files, "bytes": bytes})
	})
	mux.HandleFunc("/api/mediaparser/cookiecloud/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeMethodNotAllowed(w)
			return
		}
		result, err := syncCookieCloudNow(true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "result": result, "config": configForWeb()})
	})
	mux.HandleFunc("/api/dailynews/config", serveDailyNewsConfigAPI)
	mux.HandleFunc("/api/deerpipe/config", serveDeerPipeConfigAPI)
	mux.HandleFunc("/api/mediaparser/logos", serveLogoAPI)
	mux.HandleFunc("/api/mediaparser/logos/image", serveLogoImageAPI)
	mux.HandleFunc("/api/onebot/groups", serveGroupListAPI)
	mux.HandleFunc("/api/logs", serveLogsAPI)
	auth := loadWebAuthConfig()
	handler := http.Handler(mux)
	if auth.Enabled {
		handler = withWebAuth(handler, auth)
		logrus.Infof("[webui] auth enabled user=%s", auth.User)
	}
	go func() {
		logrus.Infof("[webui] listening on %s", addr)
		if err := http.ListenAndServe(addr, handler); err != nil {
			logrus.Errorf("[webui] stopped: %v", err)
			webUIMu.Lock()
			webUIActive = false
			webUIMu.Unlock()
		}
	}()
}

func webUIStatusText() string {
	webUIMu.Lock()
	active := webUIActive
	webUIMu.Unlock()
	if active {
		return "运行中"
	}
	return "未运行"
}

func loadWebAuthConfig() webAuthConfig {
	user := strings.TrimSpace(os.Getenv("WEBUI_USER"))
	if user == "" {
		user = "admin"
	}
	pass := strings.TrimSpace(os.Getenv("WEBUI_PASSWORD"))
	storePath := webAuthStorePath()
	if pass == "" {
		pass = strings.TrimSpace(os.Getenv("WEBUI_TOKEN"))
	}
	if strings.EqualFold(pass, "off") || strings.EqualFold(pass, "disabled") {
		return webAuthConfig{Enabled: false}
	}
	if pass != "" {
		store, err := newWebAuthStore(user, pass)
		if err != nil {
			logrus.Warnf("[webui] create env auth store failed: %v", err)
			return webAuthConfig{Enabled: true, User: user, Password: pass, StorePath: storePath}
		}
		return webAuthConfig{Enabled: true, User: store.User, Store: store, StorePath: storePath}
	}

	if store, err := readWebAuthStore(); err == nil && store.Hash != "" {
		removeLegacyWebAuthToken()
		return webAuthConfig{Enabled: true, User: store.User, Store: store, StorePath: storePath}
	}

	legacyToken := ""
	legacyPath := filepath.Join(engine.DataFolder(), "webui_auth_token")
	if data, err := os.ReadFile(legacyPath); err == nil {
		legacyToken = strings.TrimSpace(string(data))
	}
	if legacyToken == "" {
		legacyToken = newWebAuthToken()
	}
	store, err := newWebAuthStore(user, legacyToken)
	if err != nil {
		logrus.Warnf("[webui] create auth store failed: %v", err)
		return webAuthConfig{Enabled: true, User: user, Password: legacyToken, StorePath: storePath}
	}
	if err := writeWebAuthStore(store); err != nil {
		logrus.Warnf("[webui] save auth store failed: %v", err)
	} else {
		removeLegacyWebAuthToken()
	}
	return webAuthConfig{Enabled: true, User: store.User, Store: store, StorePath: storePath}
}

func webAuthStorePath() string {
	return filepath.Join(engine.DataFolder(), "webui_auth.json")
}

func removeLegacyWebAuthToken() {
	legacyPath := filepath.Join(engine.DataFolder(), "webui_auth_token")
	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		logrus.Warnf("[webui] remove legacy auth token failed: %v", err)
	}
}

func readWebAuthStore() (webAuthStore, error) {
	data, err := os.ReadFile(webAuthStorePath())
	if err != nil {
		return webAuthStore{}, err
	}
	var store webAuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return webAuthStore{}, err
	}
	store.User = strings.TrimSpace(store.User)
	if store.User == "" {
		store.User = "admin"
	}
	if store.Iterations <= 0 {
		store.Iterations = 120000
	}
	return store, nil
}

func writeWebAuthStore(store webAuthStore) error {
	if err := os.MkdirAll(filepath.Dir(webAuthStorePath()), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := webAuthStorePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, webAuthStorePath())
}

func newWebAuthStore(user, password string) (webAuthStore, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		user = "admin"
	}
	password = strings.TrimSpace(password)
	if password == "" {
		return webAuthStore{}, fmt.Errorf("password is empty")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return webAuthStore{}, err
	}
	iterations := 120000
	sum := pbkdf2SHA256([]byte(password), salt, iterations, 32)
	return webAuthStore{
		User:       user,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Hash:       base64.StdEncoding.EncodeToString(sum),
		Iterations: iterations,
	}, nil
}

func verifyWebAuthStore(store webAuthStore, user, password string) bool {
	if !webAuthEqual(user, store.User) {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(store.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(store.Hash)
	if err != nil || len(want) == 0 {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, store.Iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	if iterations <= 0 {
		iterations = 120000
	}
	var out []byte
	block := uint32(1)
	for len(out) < keyLen {
		h := hmac.New(sha256.New, password)
		h.Write(salt)
		h.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := h.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			h = hmac.New(sha256.New, password)
			h.Write(u)
			u = h.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
		block++
	}
	return out[:keyLen]
}

func newWebAuthToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func withWebAuth(next http.Handler, auth webAuthConfig) http.Handler {
	realm := `Basic realm="ZeroBot WebUI", charset="UTF-8"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || !verifyWebAuth(auth, user, pass) {
			w.Header().Set("WWW-Authenticate", realm)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func verifyWebAuth(auth webAuthConfig, user, pass string) bool {
	if auth.Store.Hash != "" {
		return verifyWebAuthStore(auth.Store, user, pass)
	}
	if auth.Password != "" {
		return webAuthEqual(user, auth.User) && webAuthEqual(pass, auth.Password)
	}
	if store, err := readWebAuthStore(); err == nil && store.Hash != "" {
		return verifyWebAuthStore(store, user, pass)
	}
	return webAuthEqual(user, auth.User) && webAuthEqual(pass, auth.Password)
}

func webAuthEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func configForWeb() map[string]any {
	data, err := json.Marshal(snapshotConfig())
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	cfg := snapshotConfig()
	for _, key := range []string{
		"bilibili_cookie",
		"xiaohongshu_cookie",
		"youtube_cookie",
		"instagram_cookie",
		"keylol_cookie",
		"linuxdo_cookie",
		"cookiecloud_password",
		"yt_dlp_cookie_file",
		"youtube_cookie_file",
		"instagram_cookie_file",
	} {
		if raw, _ := out[key].(string); strings.TrimSpace(raw) != "" {
			out[key+"_set"] = true
		}
		out[key] = ""
	}
	out["bilibili_cookie_set"] = strings.TrimSpace(cfg.BilibiliCookie) != ""
	out["xiaohongshu_cookie_set"] = strings.TrimSpace(cfg.XiaohongshuCookie) != ""
	out["youtube_cookie_set"] = strings.TrimSpace(cfg.YouTubeCookie) != ""
	out["instagram_cookie_set"] = strings.TrimSpace(cfg.InstagramCookie) != ""
	out["keylol_cookie_set"] = strings.TrimSpace(cfg.KeylolCookie) != ""
	out["linuxdo_cookie_set"] = strings.TrimSpace(cfg.LinuxdoCookie) != ""
	out["cookiecloud_password_set"] = strings.TrimSpace(cfg.CookieCloudPassword) != ""
	out["yt_dlp_cookie_file_set"] = strings.TrimSpace(cfg.YTDLPCookieFile) != ""
	out["youtube_cookie_file_set"] = strings.TrimSpace(cfg.YouTubeCookieFile) != ""
	out["instagram_cookie_file_set"] = strings.TrimSpace(cfg.InstagramCookieFile) != ""
	out["cookiecloud_platform_options"] = cookieCloudPlatformOptions()
	return out
}

func preserveSecretConfigFields(next *config, old config) {
	if strings.TrimSpace(next.BilibiliCookie) == "" {
		next.BilibiliCookie = old.BilibiliCookie
	}
	if strings.TrimSpace(next.XiaohongshuCookie) == "" {
		next.XiaohongshuCookie = old.XiaohongshuCookie
	}
	if strings.TrimSpace(next.YouTubeCookie) == "" {
		next.YouTubeCookie = old.YouTubeCookie
	}
	if strings.TrimSpace(next.InstagramCookie) == "" {
		next.InstagramCookie = old.InstagramCookie
	}
	if strings.TrimSpace(next.KeylolCookie) == "" {
		next.KeylolCookie = old.KeylolCookie
	}
	if strings.TrimSpace(next.LinuxdoCookie) == "" {
		next.LinuxdoCookie = old.LinuxdoCookie
	}
	if strings.TrimSpace(next.CookieCloudPassword) == "" {
		next.CookieCloudPassword = old.CookieCloudPassword
	}
	if strings.TrimSpace(next.YTDLPCookieFile) == "" {
		next.YTDLPCookieFile = old.YTDLPCookieFile
	}
	if strings.TrimSpace(next.YouTubeCookieFile) == "" {
		next.YouTubeCookieFile = old.YouTubeCookieFile
	}
	if strings.TrimSpace(next.InstagramCookieFile) == "" {
		next.InstagramCookieFile = old.InstagramCookieFile
	}
	if next.BilibiliCookie == "" {
		next.BilibiliUseCookie = false
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func defaultWebStatus() map[string]any {
	accounts := webBotAccounts()
	var selfID int64
	zero.RangeBot(func(id int64, ctx *zero.Ctx) bool {
		selfID = id
		return false
	})
	return map[string]any{
		"self_id":        selfID,
		"accounts":       accounts,
		"nickname":       zero.BotConfig.NickName,
		"super_users":    zero.BotConfig.SuperUsers,
		"drivers":        len(zero.BotConfig.Driver),
		"command_prefix": zero.BotConfig.CommandPrefix,
	}
}

func webBotAccounts() []map[string]any {
	ids := make([]int64, 0, 4)
	zero.RangeBot(func(id int64, ctx *zero.Ctx) bool {
		ids = append(ids, id)
		return true
	})
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	systemMu.RLock()
	settings := runtimeSystem
	systemMu.RUnlock()
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		kind, label := webBotAccountKindLabel(id, settings)
		out = append(out, map[string]any{
			"id":    id,
			"kind":  kind,
			"label": label,
		})
	}
	return out
}

func webBotAccountKindLabel(id int64, settings SystemSettings) (string, string) {
	settings = normalizeSystemSettings(settings)
	if settings.QQBotEnabled && settings.QQBotAppID != "" && id == webQQBotStableID("self:"+settings.QQBotAppID) {
		return "qqbot", "官方 QQBot"
	}
	if settings.TGBotEnabled && settings.TGBotToken != "" && id == tgBotStableID("self:"+settings.TGBotToken) {
		return "tgbot", "Telegram Bot"
	}
	return "onebot", "OneBot / llbot"
}

func webQQBotStableID(s string) int64 {
	if strings.TrimSpace(s) == "" {
		s = time.Now().String()
	}
	table := crc64.MakeTable(crc64.ISO)
	sum := crc64.Checksum([]byte(s), table) & 0x7fffffffffffffff
	if sum <= 0xffffffff {
		h := sha1.Sum([]byte(s))
		n, _ := strconv.ParseInt(hex.EncodeToString(h[:8])[:15], 16, 64)
		sum = uint64(n & 0x7fffffffffffffff)
	}
	return int64(sum)
}

func qqBotDriverAvailable() bool {
	for _, drv := range zero.BotConfig.Driver {
		t := reflect.TypeOf(drv)
		if t == nil {
			continue
		}
		if strings.Contains(strings.ToLower(t.String()), "qqbotdriver") {
			return true
		}
	}
	return false
}

func tgBotDriverAvailable() bool {
	return true
}

func serveLogsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	limit := 120
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 240 {
			limit = n
		}
	}
	writeJSON(w, map[string]any{"ok": true, "logs": webLogs.Snapshot(limit)})
}

func serveGroupListAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	groups, selfID, err := fetchOneBotGroups()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "self_id": selfID, "groups": groups})
}

func fetchOneBotGroups() ([]webGroup, int64, error) {
	var (
		groups []webGroup
		selfID int64
		err    error
	)
	zero.RangeBot(func(id int64, ctx *zero.Ctx) bool {
		selfID = id
		c, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		rsp := ctx.CallActionWithContext(c, "get_group_list", zero.Params{})
		if rsp.RetCode != 0 {
			err = fmt.Errorf("get_group_list retcode=%d message=%s", rsp.RetCode, rsp.Message)
			return false
		}
		for _, item := range rsp.Data.Array() {
			gid := item.Get("group_id").Int()
			if gid == 0 {
				continue
			}
			name := strings.TrimSpace(firstNonEmpty(item.Get("group_name").String(), item.Get("group_remark").String()))
			if name == "" {
				name = strconv.FormatInt(gid, 10)
			}
			groups = append(groups, webGroup{ID: gid, Name: name})
		}
		return false
	})
	if selfID == 0 {
		return nil, 0, fmt.Errorf("bot is not connected")
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Name == groups[j].Name {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].Name < groups[j].Name
	})
	return groups, selfID, err
}

func serveSystemSettingsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"settings": systemSettingsForWeb()})
	case http.MethodPost:
		var payload SystemSettings
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		current := systemSettingsForSave()
		if strings.TrimSpace(payload.WSToken) == "" {
			payload.WSToken = current.WSToken
		}
		if strings.TrimSpace(payload.QQBotSecret) == "" {
			payload.QQBotSecret = current.QQBotSecret
		}
		if strings.TrimSpace(payload.TGBotToken) == "" {
			payload.TGBotToken = current.TGBotToken
		}
		if !qqBotDriverAvailable() {
			payload.QQBotEnabled = current.QQBotEnabled
			payload.QQBotName = current.QQBotName
			payload.QQBotAppID = current.QQBotAppID
			payload.QQBotSecret = current.QQBotSecret
			payload.QQBotOpenID = current.QQBotOpenID
			payload.QQBotGroupOpenID = current.QQBotGroupOpenID
			payload.QQBotPublicBase = current.QQBotPublicBase
			payload.QQBotCardDisabled = current.QQBotCardDisabled
			payload.QQBotMediaEnabled = current.QQBotMediaEnabled
			payload.QQBotMarkdown = current.QQBotMarkdown
		}
		if !tgBotDriverAvailable() {
			payload.TGBotEnabled = current.TGBotEnabled
			payload.TGBotName = current.TGBotName
			payload.TGBotToken = current.TGBotToken
			payload.TGBotAPIBase = current.TGBotAPIBase
			payload.TGBotProxy = current.TGBotProxy
			payload.TGBotMediaEnabled = current.TGBotMediaEnabled
			payload.TGBotSuperUsers = append([]int64{}, current.TGBotSuperUsers...)
			payload.TGBotPrivateMode = current.TGBotPrivateMode
			payload.TGBotGroupMode = current.TGBotGroupMode
			payload.TGBotGroupUserMode = current.TGBotGroupUserMode
			payload.TGBotUserWhitelist = append([]int64{}, current.TGBotUserWhitelist...)
			payload.TGBotUserBlacklist = append([]int64{}, current.TGBotUserBlacklist...)
			payload.TGBotGroupWhitelist = append([]int64{}, current.TGBotGroupWhitelist...)
			payload.TGBotGroupBlacklist = append([]int64{}, current.TGBotGroupBlacklist...)
			payload.TGBotGroupUserWhitelist = append([]int64{}, current.TGBotGroupUserWhitelist...)
			payload.TGBotGroupUserBlacklist = append([]int64{}, current.TGBotGroupUserBlacklist...)
		}
		payload = normalizeSystemSettings(payload)
		if err := saveSystemSettings(payload); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		applyRuntimeSystemSettings(payload)
		writeJSON(w, map[string]any{"ok": true, "settings": systemSettingsForWeb()})
	default:
		writeMethodNotAllowed(w)
	}
}

func serveDailyNewsConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"ok": true, "config": dailynews.WebConfig()})
	case http.MethodPost:
		var next dailynews.WebNewsConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := dailynews.SaveWebConfig(next)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "config": saved, "restart_required": false})
	default:
		writeMethodNotAllowed(w)
	}
}

func serveDeerPipeConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"ok": true, "config": deerpipe.WebConfig(), "stats": deerpipe.WebStats()})
	case http.MethodPost:
		var next deerpipe.WebDeerConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		saved, err := deerpipe.SaveWebConfig(next)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "config": saved, "stats": deerpipe.WebStats(), "restart_required": false})
	default:
		writeMethodNotAllowed(w)
	}
}

func webDailyNewsConfigPath() string {
	return filepath.Join(webDataRoot(), "dailynews", "config.json")
}

func webLegacyDailyNewsConfigPath() string {
	return filepath.Join(filepath.Dir(engine.DataFolder()), "dailynews", "config.json")
}

func webDataRoot() string {
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

func defaultWebDailyNewsConfig() webDailyNewsConfig {
	return webDailyNewsConfig{
		DefaultSource: "60s",
		DefaultFormat: "image",
		Commands:      []string{"今日早报", "60秒读懂世界", "每天60秒读懂世界", "60秒早报", "60s早报"},
		Access: webDailyNewsAccess{
			Enabled:        true,
			PrivateEnabled: true,
			PrivateMode:    "none",
			GroupMode:      "none",
		},
		Sources: []webDailyNewsSource{
			{ID: "60s", Name: "每天60秒", Category: "news", Desc: "每日新闻、微语和图片早报", URL: "https://60s.744524299.xyz/v2/60s", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"今日早报", "60秒读懂世界", "每天60秒读懂世界", "60秒早报", "60s早报"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-text", Name: "60s 文本", Category: "news", Desc: "文本格式早报", URL: "https://60s.744524299.xyz/v2/60s", Method: http.MethodGet, Encoding: "text", Timeout: 20, Builtin: true, Commands: []string{"文字早报", "早报文本"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-markdown", Name: "60s Markdown", Category: "news", Desc: "Markdown 格式早报", URL: "https://60s.744524299.xyz/v2/60s", Method: http.MethodGet, Encoding: "markdown", Timeout: 20, Builtin: true, Commands: []string{"markdown早报", "md早报"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-image", Name: "60s 图片跳转", Category: "news", Desc: "图片跳转早报", URL: "https://60s.744524299.xyz/v2/60s", Method: http.MethodGet, Encoding: "image", Timeout: 20, Builtin: true, Commands: []string{"图片早报"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "60s-image-proxy", Name: "60s 图片代理", Category: "news", Desc: "直接下载图片早报", URL: "https://60s.744524299.xyz/v2/60s", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"早报图片"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "legacy-image", Name: "旧版早报图片", Category: "news", Desc: "旧版图片接口", URL: "https://uapis.cn/api/v1/daily/news-image", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"旧版早报"}},
			{ID: "weather-realtime", Name: "实时天气", Category: "weather", Desc: "查询城市实时天气", URL: "https://60s.viki.moe/v2/weather/realtime", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"天气", "实时天气"}, Params: []webDailyNewsParam{{Name: "query", Label: "地点", Source: "rest", Required: true, Placeholder: "北京"}}},
			{ID: "weather-forecast", Name: "天气预报", Category: "weather", Desc: "查询城市天气预报", URL: "https://60s.viki.moe/v2/weather/forecast", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"天气预报", "未来天气"}, Params: []webDailyNewsParam{{Name: "query", Label: "地点", Source: "rest", Required: true, Placeholder: "上海"}}},
			{ID: "weibo", Name: "微博热搜", Category: "hot", Desc: "微博热搜榜", URL: "https://60s.viki.moe/v2/weibo", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"微博热搜", "微博热点"}},
			{ID: "zhihu", Name: "知乎热榜", Category: "hot", Desc: "知乎热门话题", URL: "https://60s.viki.moe/v2/zhihu", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"知乎热榜", "知乎热点"}},
			{ID: "baidu-hot", Name: "百度热搜", Category: "hot", Desc: "百度热搜榜", URL: "https://60s.viki.moe/v2/baidu/hot", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"百度热搜", "百度热点"}},
			{ID: "douyin-hot", Name: "抖音热点", Category: "hot", Desc: "抖音热点榜", URL: "https://60s.viki.moe/v2/douyin", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"抖音热点", "抖音热榜"}},
			{ID: "toutiao", Name: "头条热点", Category: "hot", Desc: "今日头条热点", URL: "https://60s.viki.moe/v2/toutiao", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"头条热点", "今日头条"}},
			{ID: "bili-hot", Name: "B站热榜", Category: "hot", Desc: "哔哩哔哩热门视频", URL: "https://60s.viki.moe/v2/bili", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"B站热榜", "b站热榜", "哔哩热榜"}},
			{ID: "exchange-rate", Name: "汇率查询", Category: "data", Desc: "货币汇率查询", URL: "https://60s.viki.moe/v2/exchange-rate", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"汇率", "汇率查询"}, Params: []webDailyNewsParam{{Name: "from", Label: "源币种", Source: "arg", Default: "USD", Placeholder: "USD"}, {Name: "to", Label: "目标币种", Source: "arg", Default: "CNY", Placeholder: "CNY"}}},
			{ID: "lunar", Name: "农历查询", Category: "data", Desc: "公历农历与生肖节气", URL: "https://60s.viki.moe/v2/lunar", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"农历", "今日农历"}, Params: []webDailyNewsParam{{Name: "date", Label: "日期", Source: "arg", Placeholder: "YYYY-MM-DD"}}},
			{ID: "today-history", Name: "历史上的今天", Category: "data", Desc: "历史事件查询", URL: "https://60s.viki.moe/v2/today-in-history", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"历史上的今天", "历史今天"}, Params: []webDailyNewsParam{{Name: "month", Label: "月份", Source: "arg", Placeholder: "1"}, {Name: "day", Label: "日期", Source: "arg", Placeholder: "15"}}},
			{ID: "baike", Name: "百科查询", Category: "data", Desc: "中文百科搜索", URL: "https://60s.viki.moe/v2/baike", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"百科", "百科查询"}, Params: []webDailyNewsParam{{Name: "keyword", Label: "关键词", Source: "rest", Required: true, Placeholder: "Python编程"}}},
			{ID: "fuel-price", Name: "油价查询", Category: "data", Desc: "国内油价", URL: "https://60s.viki.moe/v2/fuel-price", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"油价", "油价查询"}, Params: []webDailyNewsParam{{Name: "province", Label: "省份", Source: "rest", Required: true, Placeholder: "北京"}}},
			{ID: "gold-price", Name: "金价查询", Category: "data", Desc: "黄金价格", URL: "https://60s.viki.moe/v2/gold-price", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"金价", "黄金价格"}},
			{ID: "chemical", Name: "化学元素", Category: "data", Desc: "元素信息查询", URL: "https://60s.viki.moe/v2/chemical", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"化学元素", "元素查询"}, Params: []webDailyNewsParam{{Name: "query", Label: "元素", Source: "rest", Required: true, Placeholder: "H"}}},
			{ID: "hitokoto", Name: "一言", Category: "fun", Desc: "随机一句话", URL: "https://60s.viki.moe/v2/hitokoto", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"一言", "来句一言"}, Params: []webDailyNewsParam{{Name: "category", Label: "分类", Source: "arg", Placeholder: "anime"}}},
			{ID: "dad-joke", Name: "英文冷笑话", Category: "fun", Desc: "Dad joke", URL: "https://60s.viki.moe/v2/dad-joke", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"英文笑话", "dad joke"}},
			{ID: "duanzi", Name: "中文段子", Category: "fun", Desc: "随机中文段子", URL: "https://60s.viki.moe/v2/duanzi", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"讲个笑话", "段子", "笑话"}},
			{ID: "luck", Name: "今日运势", Category: "fun", Desc: "每日运势", URL: "https://60s.viki.moe/v2/luck", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"今日运势", "运势"}},
			{ID: "kfc", Name: "疯狂星期四", Category: "fun", Desc: "KFC 梗文案", URL: "https://60s.viki.moe/v2/kfc", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"疯狂星期四", "kfc"}},
			{ID: "moyu", Name: "摸鱼日历", Category: "fun", Desc: "摸鱼日历图片", URL: "https://60s.viki.moe/v2/moyu", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"摸鱼日历", "摸鱼"}},
			{ID: "ncm-rank-list", Name: "网易云榜单", Category: "media", Desc: "音乐榜单列表", URL: "https://60s.viki.moe/v2/ncm-rank/list", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网易云榜单", "音乐榜单"}},
			{ID: "ncm-rank", Name: "网易云榜单详情", Category: "media", Desc: "音乐榜单歌曲", URL: "https://60s.viki.moe/v2/ncm-rank/{id}", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网易云热歌", "音乐排行"}, Params: []webDailyNewsParam{{Name: "id", Label: "榜单ID", Source: "arg", Default: "3778678", Placeholder: "3778678"}}},
			{ID: "lyric", Name: "歌词搜索", Category: "media", Desc: "搜索歌词", URL: "https://60s.viki.moe/v2/lyric", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"歌词", "歌词搜索"}, Params: []webDailyNewsParam{{Name: "keyword", Label: "歌曲", Source: "rest", Required: true, Placeholder: "稻香 周杰伦"}}},
			{ID: "maoyan-all-movie", Name: "电影资料", Category: "media", Desc: "猫眼电影资料", URL: "https://60s.viki.moe/v2/maoyan/all/movie", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电影资料"}},
			{ID: "maoyan-movie", Name: "实时票房", Category: "media", Desc: "电影票房排行", URL: "https://60s.viki.moe/v2/maoyan/realtime/movie", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电影票房", "实时票房"}},
			{ID: "maoyan-tv", Name: "电视剧收视", Category: "media", Desc: "电视剧收视率", URL: "https://60s.viki.moe/v2/maoyan/realtime/tv", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"电视剧收视", "收视率"}},
			{ID: "maoyan-web", Name: "网剧热度", Category: "media", Desc: "网剧热度排行", URL: "https://60s.viki.moe/v2/maoyan/realtime/web", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网剧热度", "网剧排行"}},
			{ID: "ip", Name: "IP 查询", Category: "tool", Desc: "IP 归属地和运营商", URL: "https://60s.viki.moe/v2/ip", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"ip查询", "查ip"}, Params: []webDailyNewsParam{{Name: "ip", Label: "IP", Source: "rest", Placeholder: "8.8.8.8"}}},
			{ID: "fanyi", Name: "文本翻译", Category: "tool", Desc: "多语言翻译", URL: "https://60s.viki.moe/v2/fanyi", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"翻译"}, Params: []webDailyNewsParam{{Name: "text", Label: "文本", Source: "rest", Required: true, Placeholder: "你好世界"}, {Name: "from", Label: "源语言", Source: "default", Default: "auto"}, {Name: "to", Label: "目标语言", Source: "arg", Default: "zh", Placeholder: "en"}}},
			{ID: "fanyi-langs", Name: "翻译语言", Category: "tool", Desc: "支持语言列表", URL: "https://60s.viki.moe/v2/fanyi/langs", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"翻译语言"}},
			{ID: "qrcode", Name: "二维码", Category: "tool", Desc: "生成二维码图片", URL: "https://60s.viki.moe/v2/qrcode", Method: http.MethodGet, Encoding: "image-proxy", Timeout: 20, Builtin: true, Commands: []string{"二维码", "生成二维码"}, Params: []webDailyNewsParam{{Name: "text", Label: "内容", Source: "rest", Required: true, Placeholder: "https://example.com"}, {Name: "size", Label: "尺寸", Source: "arg", Default: "300", Placeholder: "300"}}},
			{ID: "hash", Name: "哈希计算", Category: "tool", Desc: "MD5/SHA 计算", URL: "https://60s.viki.moe/v2/hash", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"哈希", "hash"}, Params: []webDailyNewsParam{{Name: "text", Label: "文本", Source: "rest", Required: true, Placeholder: "Hello World"}, {Name: "algorithm", Label: "算法", Source: "arg", Default: "md5", Placeholder: "sha256"}}},
			{ID: "og", Name: "网页元信息", Category: "tool", Desc: "提取网页标题描述图片", URL: "https://60s.viki.moe/v2/og", Method: http.MethodPost, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"网页信息", "og"}, Params: []webDailyNewsParam{{Name: "url", Label: "网址", Source: "rest", Required: true, Placeholder: "https://example.com"}}},
			{ID: "whois", Name: "WHOIS 查询", Category: "tool", Desc: "域名注册信息", URL: "https://60s.viki.moe/v2/whois", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"whois", "域名查询"}, Params: []webDailyNewsParam{{Name: "domain", Label: "域名", Source: "rest", Required: true, Placeholder: "github.com"}}},
			{ID: "password", Name: "密码生成", Category: "tool", Desc: "生成随机密码", URL: "https://60s.viki.moe/v2/password", Method: http.MethodGet, Encoding: "json", Timeout: 20, Builtin: true, Commands: []string{"生成密码", "随机密码"}, Params: []webDailyNewsParam{{Name: "length", Label: "长度", Source: "arg", Default: "16", Placeholder: "16"}, {Name: "numbers", Label: "数字", Source: "default", Default: "true"}, {Name: "lowercase", Label: "小写", Source: "default", Default: "true"}, {Name: "uppercase", Label: "大写", Source: "default", Default: "true"}, {Name: "symbols", Label: "符号", Source: "default", Default: "true"}}},
		},
	}
}

func loadWebDailyNewsConfig() (webDailyNewsConfig, error) {
	cfg := defaultWebDailyNewsConfig()
	path := webDailyNewsConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if legacy, ok := readLegacyWebDailyNewsConfig(nil); ok {
				return saveWebDailyNewsConfig(legacy)
			}
			return saveWebDailyNewsConfig(cfg)
		}
		return cfg, err
	}
	if legacy, ok := readLegacyWebDailyNewsConfig(data); ok {
		return saveWebDailyNewsConfig(legacy)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return defaultWebDailyNewsConfig(), err
	}
	return normalizeWebDailyNewsConfig(cfg), nil
}

func readLegacyWebDailyNewsConfig(current []byte) (webDailyNewsConfig, bool) {
	var cfg webDailyNewsConfig
	path := webDailyNewsConfigPath()
	legacyPath := webLegacyDailyNewsConfigPath()
	if filepath.Clean(path) == filepath.Clean(legacyPath) {
		return cfg, false
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return cfg, false
	}
	if webDailyNewsScheduleCount(data) <= webDailyNewsScheduleCount(current) {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false
	}
	return normalizeWebDailyNewsConfig(cfg), true
}

func webDailyNewsScheduleCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	var cfg struct {
		Schedules []webDailyNewsSchedule `json:"schedules"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	return len(cfg.Schedules)
}

func saveWebDailyNewsConfig(next webDailyNewsConfig) (webDailyNewsConfig, error) {
	cfg := normalizeWebDailyNewsConfig(next)
	path := webDailyNewsConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return cfg, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return cfg, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return cfg, err
	}
	return cfg, os.Rename(tmp, path)
}

func normalizeWebDailyNewsConfig(in webDailyNewsConfig) webDailyNewsConfig {
	base := defaultWebDailyNewsConfig()
	if id := webDailyNewsSanitizeID(in.DefaultSource); id != "" {
		base.DefaultSource = id
	}
	if webDailyNewsFormatOK(in.DefaultFormat) {
		base.DefaultFormat = strings.ToLower(strings.TrimSpace(in.DefaultFormat))
	}
	if commands := normalizeWebDailyNewsCommands(in.Commands); len(commands) > 0 {
		base.Commands = commands
	}
	base.Access = normalizeWebDailyNewsAccess(in.Access)
	merged := make(map[string]webDailyNewsSource)
	for _, src := range base.Sources {
		merged[src.ID] = normalizeWebDailyNewsSource(src)
	}
	for _, src := range in.Sources {
		src = normalizeWebDailyNewsSource(src)
		if src.ID == "" || src.URL == "" {
			continue
		}
		if old, ok := merged[src.ID]; ok && old.Builtin {
			src.Builtin = true
			if src.Category == "" {
				src.Category = old.Category
			}
			if src.Desc == "" {
				src.Desc = old.Desc
			}
			if src.URL == "" {
				src.URL = old.URL
			}
			if src.Method == "" {
				src.Method = old.Method
			}
			if src.Encoding == "" {
				src.Encoding = old.Encoding
			}
			if len(src.Params) == 0 {
				src.Params = old.Params
			}
		}
		merged[src.ID] = src
	}
	base.Sources = webDailyNewsSourcesFromMap(merged)
	if _, ok := merged[base.DefaultSource]; !ok {
		base.DefaultSource = "60s"
	}
	for _, task := range in.Schedules {
		task = normalizeWebDailyNewsSchedule(task, merged)
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

func normalizeWebDailyNewsSource(src webDailyNewsSource) webDailyNewsSource {
	src.ID = webDailyNewsSanitizeID(src.ID)
	src.Name = strings.TrimSpace(src.Name)
	src.Category = webDailyNewsSanitizeID(src.Category)
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
	src.Commands = normalizeWebDailyNewsCommands(src.Commands)
	src.Params = normalizeWebDailyNewsParams(src.Params)
	return src
}

func normalizeWebDailyNewsParams(params []webDailyNewsParam) []webDailyNewsParam {
	out := make([]webDailyNewsParam, 0, len(params))
	for _, param := range params {
		param.Name = webDailyNewsSanitizeParamName(param.Name)
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

func normalizeWebDailyNewsAccess(in webDailyNewsAccess) webDailyNewsAccess {
	groupMode := normalizeWebDailyNewsAccessMode(in.GroupMode)
	privateMode := normalizeWebDailyNewsAccessMode(in.PrivateMode)
	zeroValue := !in.Enabled && !in.PrivateEnabled && groupMode == "none" && privateMode == "none" && len(in.GroupWhitelist) == 0 && len(in.GroupBlacklist) == 0 && len(in.PrivateWhitelist) == 0 && len(in.PrivateBlacklist) == 0
	if zeroValue {
		in.Enabled = true
		in.PrivateEnabled = true
	}
	return webDailyNewsAccess{
		Enabled:          in.Enabled,
		PrivateEnabled:   in.PrivateEnabled,
		PrivateMode:      privateMode,
		PrivateWhitelist: normalizeWebDailyNewsIDs(in.PrivateWhitelist),
		PrivateBlacklist: normalizeWebDailyNewsIDs(in.PrivateBlacklist),
		GroupMode:        groupMode,
		GroupWhitelist:   normalizeWebDailyNewsIDs(in.GroupWhitelist),
		GroupBlacklist:   normalizeWebDailyNewsIDs(in.GroupBlacklist),
	}
}

func normalizeWebDailyNewsAccessMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "blacklist" && mode != "whitelist" {
		return "none"
	}
	return mode
}

func normalizeWebDailyNewsIDs(ids []int64) []int64 {
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

func normalizeWebDailyNewsSchedule(task webDailyNewsSchedule, sources map[string]webDailyNewsSource) webDailyNewsSchedule {
	task.ID = webDailyNewsSanitizeID(task.ID)
	task.SourceID = webDailyNewsSanitizeID(task.SourceID)
	task.Target = strings.TrimSpace(task.Target)
	task.Time = strings.TrimSpace(task.Time)
	task.Cron = webDailyNewsNormalizeCronExpr(task.Cron)
	if task.Cron == "" && webDailyNewsClockOK(task.Time) {
		task.Cron = webDailyNewsClockToCron(task.Time)
	}
	if task.Cron == "" || !webDailyNewsCronOK(task.Cron) {
		task.Cron = ""
	}
	if webDailyNewsClockOK(task.Time) {
		task.Time = webDailyNewsCronToClock(task.Cron, task.Time)
	} else if task.Cron != "" {
		task.Time = webDailyNewsCronToClock(task.Cron, "")
	} else {
		task.Time = ""
	}
	if !webDailyNewsTargetOK(task.Target) {
		task.Target = ""
	}
	if src, ok := sources[task.SourceID]; ok {
		task.Format = webDailyNewsFormatFromEncoding(src.Encoding)
	} else {
		task.Format = strings.ToLower(strings.TrimSpace(task.Format))
		if !webDailyNewsFormatOK(task.Format) {
			task.Format = "image"
		}
	}
	return task
}

func webDailyNewsSourcesFromMap(m map[string]webDailyNewsSource) []webDailyNewsSource {
	out := make([]webDailyNewsSource, 0, len(m))
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

func normalizeWebDailyNewsCommands(commands []string) []string {
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

func webDailyNewsSanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func webDailyNewsSanitizeParamName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func webDailyNewsFormatOK(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "image", "text", "markdown", "json":
		return true
	default:
		return false
	}
}

func webDailyNewsFormatFromEncoding(encoding string) string {
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

func webDailyNewsClockOK(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}

func webDailyNewsClockToCron(clock string) string {
	t, err := time.Parse("15:04", strings.TrimSpace(clock))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour())
}

func webDailyNewsCronToClock(expr, fallback string) string {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		if webDailyNewsClockOK(fallback) {
			return fallback
		}
		return ""
	}
	minute, err1 := strconv.Atoi(fields[0])
	hour, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		if webDailyNewsClockOK(fallback) {
			return fallback
		}
		return ""
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

func webDailyNewsNormalizeCronExpr(expr string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return ""
	}
	if webDailyNewsClockOK(expr) {
		return webDailyNewsClockToCron(expr)
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return ""
	}
	for i, field := range fields {
		if field == "?" {
			field = "*"
		}
		fields[i] = field
	}
	return strings.Join(fields, " ")
}

func webDailyNewsCronOK(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if !webDailyNewsCronFieldOK(field, ranges[i][0], ranges[i][1]) {
			return false
		}
	}
	return true
}

func webDailyNewsCronFieldOK(field string, min, max int) bool {
	if field == "" {
		return false
	}
	for _, part := range strings.Split(field, ",") {
		if _, ok := webDailyNewsCronPartValues(part, min, max); !ok {
			return false
		}
	}
	return true
}

func webDailyNewsCronPartValues(part string, min, max int) (map[int]bool, bool) {
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

func webDailyNewsTargetOK(target string) bool {
	parts := strings.SplitN(target, ":", 2)
	if len(parts) != 2 {
		return false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || id <= 0 {
		return false
	}
	switch strings.TrimSpace(parts[0]) {
	case "群", "group", "私聊", "private":
		return true
	default:
		return false
	}
}

func serveWebAuthAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		auth := loadWebAuthConfig()
		writeJSON(w, map[string]any{
			"ok":            true,
			"enabled":       auth.Enabled,
			"user":          auth.User,
			"stored":        auth.Store.Hash != "",
			"env_managed":   strings.TrimSpace(os.Getenv("WEBUI_PASSWORD")) != "" || strings.TrimSpace(os.Getenv("WEBUI_TOKEN")) != "",
			"auth_file_set": fileExists(webAuthStorePath()),
		})
	case http.MethodPost:
		var payload struct {
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		user := strings.TrimSpace(payload.User)
		password := strings.TrimSpace(payload.Password)
		if user == "" {
			http.Error(w, "user is required", http.StatusBadRequest)
			return
		}
		if password == "" {
			http.Error(w, "password is required", http.StatusBadRequest)
			return
		}
		store, err := newWebAuthStore(user, password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := writeWebAuthStore(store); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "user": store.User, "stored": true})
	default:
		writeMethodNotAllowed(w)
	}
}

func systemSettingsForSave() SystemSettings {
	saved, _ := readSystemSettings()
	systemMu.RLock()
	current := runtimeSystem
	systemMu.RUnlock()
	if saved.WebUIAddr == "" {
		saved.WebUIAddr = current.WebUIAddr
	}
	if saved.WSURL == "" {
		saved.WSURL = current.WSURL
	}
	if saved.WSToken == "" {
		saved.WSToken = current.WSToken
	}
	if saved.OneBotDataDir == "" {
		saved.OneBotDataDir = current.OneBotDataDir
	}
	if saved.QQBotSecret == "" {
		saved.QQBotSecret = current.QQBotSecret
	}
	if saved.TGBotToken == "" {
		saved.TGBotToken = current.TGBotToken
	}
	if saved.Nickname == "" {
		saved.Nickname = firstNonEmpty(firstString(zero.BotConfig.NickName), current.Nickname)
	}
	if saved.CommandPrefix == "" {
		saved.CommandPrefix = zero.BotConfig.CommandPrefix
	}
	if len(saved.SuperUsers) == 0 {
		saved.SuperUsers = append([]int64{}, zero.BotConfig.SuperUsers...)
	}
	return normalizeSystemSettings(saved)
}

func systemSettingsForWeb() systemSettingsResponse {
	settings := systemSettingsForSave()
	systemMu.RLock()
	current := runtimeSystem
	systemMu.RUnlock()
	pending := []string{}
	if settings.WebUIAddr != "" && current.WebUIAddr != "" && settings.WebUIAddr != current.WebUIAddr {
		pending = append(pending, "WebUI 监听地址")
	}
	if settings.WSURL != "" && current.WSURL != "" && settings.WSURL != current.WSURL {
		pending = append(pending, "OneBot WS 地址")
	}
	if settings.WSToken != "" && current.WSToken != "" && settings.WSToken != current.WSToken {
		pending = append(pending, "OneBot Token")
	}
	if settings.QQBotEnabled != current.QQBotEnabled ||
		settings.QQBotName != current.QQBotName ||
		settings.QQBotAppID != current.QQBotAppID ||
		settings.QQBotSecret != current.QQBotSecret ||
		settings.QQBotOpenID != current.QQBotOpenID ||
		settings.QQBotGroupOpenID != current.QQBotGroupOpenID ||
		settings.QQBotPublicBase != current.QQBotPublicBase ||
		settings.QQBotCardDisabled != current.QQBotCardDisabled ||
		settings.QQBotMediaEnabled != current.QQBotMediaEnabled ||
		settings.QQBotMarkdown != current.QQBotMarkdown {
		pending = append(pending, "官方 QQBot 通道")
	}
	if settings.TGBotEnabled != current.TGBotEnabled ||
		settings.TGBotName != current.TGBotName ||
		settings.TGBotToken != current.TGBotToken ||
		settings.TGBotAPIBase != current.TGBotAPIBase ||
		settings.TGBotProxy != current.TGBotProxy ||
		settings.TGBotMediaEnabled != current.TGBotMediaEnabled {
		pending = append(pending, "Telegram Bot 通道")
	}
	return systemSettingsResponse{
		WebUIAddr:               firstNonEmpty(settings.WebUIAddr, current.WebUIAddr),
		WSURL:                   firstNonEmpty(settings.WSURL, current.WSURL),
		WSTokenSet:              settings.WSToken != "",
		OneBotDataDir:           firstNonEmpty(settings.OneBotDataDir, current.OneBotDataDir),
		Nickname:                firstNonEmpty(settings.Nickname, firstString(zero.BotConfig.NickName)),
		CommandPrefix:           firstNonEmpty(settings.CommandPrefix, zero.BotConfig.CommandPrefix),
		SuperUsers:              uniqueInt64(settings.SuperUsers),
		QQBotEnabled:            settings.QQBotEnabled,
		QQBotName:               firstNonEmpty(settings.QQBotName, "qqbot"),
		QQBotAppID:              settings.QQBotAppID,
		QQBotSecretSet:          settings.QQBotSecret != "",
		QQBotOpenID:             settings.QQBotOpenID,
		QQBotGroupOpenID:        settings.QQBotGroupOpenID,
		QQBotPublicBase:         settings.QQBotPublicBase,
		QQBotCardEnabled:        !settings.QQBotCardDisabled,
		QQBotMediaEnabled:       settings.QQBotMediaEnabled,
		QQBotMarkdown:           settings.QQBotMarkdown,
		QQBotAvailable:          qqBotDriverAvailable(),
		TGBotEnabled:            settings.TGBotEnabled,
		TGBotName:               firstNonEmpty(settings.TGBotName, "telegram"),
		TGBotTokenSet:           settings.TGBotToken != "",
		TGBotAPIBase:            firstNonEmpty(settings.TGBotAPIBase, tgBotDefaultAPIBase),
		TGBotProxy:              settings.TGBotProxy,
		TGBotMediaEnabled:       settings.TGBotMediaEnabled,
		TGBotAvailable:          tgBotDriverAvailable(),
		TGBotSuperUsers:         append([]int64{}, settings.TGBotSuperUsers...),
		TGBotPrivateMode:        settings.TGBotPrivateMode,
		TGBotGroupMode:          settings.TGBotGroupMode,
		TGBotGroupUserMode:      settings.TGBotGroupUserMode,
		TGBotUserWhitelist:      append([]int64{}, settings.TGBotUserWhitelist...),
		TGBotUserBlacklist:      append([]int64{}, settings.TGBotUserBlacklist...),
		TGBotGroupWhitelist:     append([]int64{}, settings.TGBotGroupWhitelist...),
		TGBotGroupBlacklist:     append([]int64{}, settings.TGBotGroupBlacklist...),
		TGBotGroupUserWhitelist: append([]int64{}, settings.TGBotGroupUserWhitelist...),
		TGBotGroupUserBlacklist: append([]int64{}, settings.TGBotGroupUserBlacklist...),
		PendingRestart:          pending,
	}
}

func applyRuntimeSystemSettings(settings SystemSettings) {
	settings = normalizeSystemSettings(settings)
	if settings.Nickname != "" {
		zero.BotConfig.NickName = uniqueWebStrings(append([]string{settings.Nickname}, zero.BotConfig.NickName...))
	}
	if settings.CommandPrefix != "" {
		zero.BotConfig.CommandPrefix = settings.CommandPrefix
	}
	if len(settings.SuperUsers) > 0 {
		zero.BotConfig.SuperUsers = uniqueInt64(settings.SuperUsers)
	}
	systemMu.Lock()
	runtimeSystem.OneBotDataDir = settings.OneBotDataDir
	runtimeSystem.Nickname = firstNonEmpty(settings.Nickname, runtimeSystem.Nickname)
	runtimeSystem.CommandPrefix = firstNonEmpty(settings.CommandPrefix, runtimeSystem.CommandPrefix)
	runtimeSystem.SuperUsers = uniqueInt64(settings.SuperUsers)
	runtimeSystem.TGBotSuperUsers = append([]int64{}, settings.TGBotSuperUsers...)
	runtimeSystem.TGBotPrivateMode = settings.TGBotPrivateMode
	runtimeSystem.TGBotGroupMode = settings.TGBotGroupMode
	runtimeSystem.TGBotGroupUserMode = settings.TGBotGroupUserMode
	runtimeSystem.TGBotUserWhitelist = append([]int64{}, settings.TGBotUserWhitelist...)
	runtimeSystem.TGBotUserBlacklist = append([]int64{}, settings.TGBotUserBlacklist...)
	runtimeSystem.TGBotGroupWhitelist = append([]int64{}, settings.TGBotGroupWhitelist...)
	runtimeSystem.TGBotGroupBlacklist = append([]int64{}, settings.TGBotGroupBlacklist...)
	runtimeSystem.TGBotGroupUserWhitelist = append([]int64{}, settings.TGBotGroupUserWhitelist...)
	runtimeSystem.TGBotGroupUserBlacklist = append([]int64{}, settings.TGBotGroupUserBlacklist...)
	systemMu.Unlock()
}

func firstString(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return in[0]
}

func uniqueWebStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func serveLogoAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"logos": snapshotLogos()})
	case http.MethodPost:
		platform := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("platform")))
		if !isKnownPlatform(platform) {
			http.Error(w, "unknown platform", http.StatusBadRequest)
			return
		}
		img, err := readLogoImage(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, err = savePlatformLogo(platform, img)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		platformLogoCache.Delete(platform)
		writeJSON(w, map[string]any{"ok": true, "url": "/api/mediaparser/logos/image?platform=" + url.QueryEscape(platform)})
	default:
		writeMethodNotAllowed(w)
	}
}

func serveLogoImageAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	platform := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("platform")))
	if !isKnownPlatform(platform) {
		http.Error(w, "unknown platform", http.StatusBadRequest)
		return
	}
	path, ok := validPlatformLogoPath(platform)
	w.Header().Set("Cache-Control", "no-cache")
	if ok {
		http.ServeFile(w, r, path)
		return
	}
	img, err := renderDefaultPlatformLogoImage(platform)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, img)
}

func readLogoImage(r *http.Request) (image.Image, error) {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") {
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&payload); err != nil {
			return nil, err
		}
		return fetchLogoImage(payload.URL)
	}
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		return fetchLogoImage(r.Form.Get("url"))
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(r.FormValue("url")); raw != "" {
		return fetchLogoImage(raw)
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return nil, fmt.Errorf("missing logo file or url")
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decode logo: %w", err)
	}
	return img, nil
}

func fetchLogoImage(raw string) (image.Image, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("logo url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid logo url")
	}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	req.Header.Set("Accept", "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	resp, err := (&http.Client{Timeout: 18 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download logo status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decode logo: %w", err)
	}
	return img, nil
}

func isKnownPlatform(name string) bool {
	for _, p := range platforms {
		if p.Name == name {
			return true
		}
	}
	return false
}

func snapshotLogos() map[string]any {
	out := make(map[string]any, len(platforms))
	for _, p := range platforms {
		path := filepath.Join(engine.DataFolder(), "logos", p.Name+".png")
		st, err := os.Stat(path)
		exists := err == nil && decodeLogoFile(path) != nil
		item := map[string]any{
			"exists":  exists,
			"builtin": true,
			"url":     "/api/mediaparser/logos/image?platform=" + url.QueryEscape(p.Name),
		}
		if exists {
			item["url"] = item["url"].(string) + "&t=" + strconv.FormatInt(st.ModTime().Unix(), 10)
		}
		out[p.Name] = item
	}
	return out
}

func validPlatformLogoPath(platform string) (string, bool) {
	path := filepath.Join(engine.DataFolder(), "logos", platform+".png")
	if decodeLogoFile(path) == nil {
		return "", false
	}
	return path, true
}

func savePlatformLogo(platform string, src image.Image) (string, error) {
	dir := filepath.Join(engine.DataFolder(), "logos")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 238, 88))
	for y := 0; y < 88; y++ {
		for x := 0; x < 238; x++ {
			canvas.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	fit := fitLogoImage(src, 214, 64)
	b := fit.Bounds()
	canvas = imaging.Paste(canvas, fit, image.Pt((238-b.Dx())/2, (88-b.Dy())/2))
	path := filepath.Join(dir, platform+".png")
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if err := png.Encode(f, canvas); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func fitLogoImage(src image.Image, maxW, maxH int) image.Image {
	if src == nil {
		return nil
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return src
	}
	scale := float64(maxW) / float64(sw)
	if hScale := float64(maxH) / float64(sh); hScale < scale {
		scale = hScale
	}
	nw := clampInt(int(float64(sw)*scale), 1, maxW)
	nh := clampInt(int(float64(sh)*scale), 1, maxH)
	return imaging.Resize(src, nw, nh, imaging.Lanczos)
}

func webPlatforms() []webPlatform {
	out := make([]webPlatform, 0, len(platforms))
	for _, p := range platforms {
		out = append(out, webPlatform{Name: p.Name, Label: platformLabel(p.Name), Local: platformLocalName(p.Name)})
	}
	return out
}

func safetyBuiltinPayload() []map[string]any {
	out := make([]map[string]any, 0, len(safetyCategoryDefs))
	for _, def := range safetyCategoryDefs {
		keywords := visibleSafetyBuiltinWords(def)
		hidden := def.ID == safetyCategoryPolitics
		if hidden {
			keywords = nil
		}
		out = append(out, map[string]any{
			"id":       def.ID,
			"label":    def.Label,
			"keywords": keywords,
			"hidden":   hidden,
		})
	}
	return out
}

func visibleSafetyBuiltinWords(def safetyCategoryDef) []string {
	words := safetyBuiltinWords(def)
	out := make([]string, 0, len(words))
	for _, word := range words {
		if strings.HasPrefix(word, "__mediaparser_") {
			continue
		}
		out = append(out, word)
	}
	return out
}

func platformLabel(name string) string {
	switch name {
	case "bilibili":
		return "Bilibili"
	case "douyin":
		return "Douyin"
	case "tiktok":
		return "TikTok"
	case "kuaishou":
		return "Kuaishou"
	case "weibo":
		return "Weibo"
	case "xiaohongshu":
		return "Xiaohongshu"
	case "xianyu":
		return "Xianyu"
	case "acfun":
		return "AcFun"
	case "youtube":
		return "YouTube"
	case "instagram":
		return "Instagram"
	case "toutiao":
		return "Toutiao"
	case "xiaoheihe":
		return "Heybox"
	case "twitter":
		return "X / Twitter"
	case "keylol":
		return "Keylol"
	case "steam":
		return "Steam"
	default:
		return name
	}
}

func platformLocalName(name string) string {
	switch name {
	case "bilibili":
		return "哔哩哔哩"
	case "douyin":
		return "抖音"
	case "tiktok":
		return "TikTok"
	case "kuaishou":
		return "快手"
	case "weibo":
		return "微博"
	case "xiaohongshu":
		return "小红书"
	case "xianyu":
		return "闲鱼"
	case "acfun":
		return "A站"
	case "youtube":
		return "油管"
	case "instagram":
		return "照片墙"
	case "toutiao":
		return "今日头条"
	case "xiaoheihe":
		return "小黑盒"
	case "twitter":
		return "X / Twitter"
	case "keylol":
		return "其乐"
	default:
		return name
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func serveWebIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(webIndexHTML))
}

const webIndexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ZeroBot 控制台</title>
<style>
:root{--bg:#eef3f8;--shell:#101827;--panel:#fff;--soft:#f7f9fc;--text:#1f2937;--muted:#6b7280;--line:#dde5ef;--blue:#2563eb;--blue2:#dceafe;--green:#16a34a;--red:#dc2626;--amber:#d97706;--shadow:0 16px 36px rgba(15,23,42,.08)}
*{box-sizing:border-box}html{scroll-behavior:smooth}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif}
body:before{content:"";position:fixed;inset:0 0 auto 0;height:220px;background:linear-gradient(135deg,#dbeafe,#f8fafc 58%,#e0f2fe);z-index:-1}
header{height:64px;display:flex;align-items:center;justify-content:space-between;padding:0 28px;background:rgba(255,255,255,.78);backdrop-filter:blur(16px);border-bottom:1px solid rgba(148,163,184,.28);position:sticky;top:0;z-index:20}
h1{font-size:18px;margin:0;font-weight:760}.app{display:grid;grid-template-columns:220px minmax(0,1fr);min-height:calc(100vh - 64px)}.sidebar{padding:20px 14px;border-right:1px solid rgba(148,163,184,.25);background:rgba(255,255,255,.45)}.brand{font-weight:800;font-size:20px;margin:0 0 18px}.nav{display:flex;flex-direction:column;gap:6px}.nav a{color:#334155;text-decoration:none;padding:10px 12px;border-radius:8px}.nav a:hover,.nav a.active{background:#fff;color:var(--blue);box-shadow:0 1px 0 rgba(15,23,42,.04)}
.pluginHead{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.crumb{font-size:13px;color:var(--muted);margin-bottom:6px}.subnav{display:flex;gap:8px;flex-wrap:wrap;margin-top:12px}.subnav button{height:32px;border-radius:999px}.subnav button.active{background:var(--blue);border-color:var(--blue);color:#fff}.plugin-section{display:none!important}.plugin-section.active{display:block!important}
.wrap{max-width:1240px;width:100%;padding:22px 24px 40px}.hero{display:flex;align-items:flex-end;justify-content:space-between;gap:16px;margin-bottom:16px}.hero h2{font-size:28px;line-height:1.1;margin:0}.hero p{margin:8px 0 0;color:var(--muted)}.toolbar{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.grid{display:grid;grid-template-columns:repeat(4,1fr);gap:14px}.panel{background:rgba(255,255,255,.94);border:1px solid rgba(203,213,225,.9);border-radius:10px;padding:16px;box-shadow:var(--shadow)}.sectionTitle{display:flex;gap:10px;align-items:center;margin-bottom:12px}.sectionTitle b{font-size:16px;white-space:nowrap;flex:0 0 auto}.sectionTitle .muted{min-width:0;line-height:1.35;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}.sectionTitle .right{flex:0 0 auto}.span4{grid-column:span 4}.span2{grid-column:span 2}
.metric{display:flex;flex-direction:column;gap:6px;min-height:94px}.metric span:first-child{font-size:12px;text-transform:uppercase;letter-spacing:.04em}.metric b{font-size:26px}.muted{color:var(--muted)}.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.right{margin-left:auto}
table{width:100%;border-collapse:separate;border-spacing:0;background:white;border:1px solid var(--line);border-radius:10px;overflow:hidden}th,td{padding:12px;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}th{background:#f8fafc;font-size:12px;color:#64748b;font-weight:700;text-transform:uppercase;letter-spacing:.04em}tr:last-child td{border-bottom:0}tbody tr:hover{background:#fbfdff}
button,select,input,textarea{border:1px solid var(--line);border-radius:8px;background:white;color:var(--text);padding:0 10px}button,select,input{height:34px}textarea{width:100%;min-height:110px;padding:9px 10px;resize:vertical;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace}button{cursor:pointer;background:#fff;font-weight:650}button:hover{border-color:#b8c5d6}button.primary{background:var(--blue);border-color:var(--blue);color:#fff}button.danger{border-color:#fecdd3;color:var(--red);background:#fff7f7}
.hidden,.page{display:none!important}.page.active{display:block!important}.page.active.metric{display:flex!important}.controlPills{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.controlPills>label{background:var(--soft);border:1px solid var(--line);border-radius:999px;padding:7px 10px}
.field{display:flex;flex-direction:column;gap:6px;min-width:180px}.accessGrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(360px,1fr));gap:12px;margin-top:12px}.accessGrid .field{min-width:0}.accessGrid label{font-weight:650}.accessGrid textarea{font-weight:400}
.settingsGrid{display:grid;grid-template-columns:1.05fr .95fr;gap:14px;margin-top:12px}.settingsCard{border:1px solid var(--line);border-radius:10px;background:#fbfdff;padding:14px;display:flex;flex-direction:column;gap:12px}.settingsCard .sectionTitle{margin-bottom:0}.settingsCard .field{min-width:0}.settingsCard textarea{min-height:132px}.settingsFields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.settingsFields.single{grid-template-columns:1fr}.fieldInline{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px;align-items:center}.fieldInline button{height:40px;padding:0 12px}.settingsStack{display:grid;gap:14px}.reviewGrid{display:grid;grid-template-columns:minmax(300px,.9fr) minmax(420px,1.1fr);gap:14px;align-items:stretch}.reviewColumn{display:grid;gap:14px;grid-auto-rows:minmax(0,auto)}.reviewCard{min-height:172px;overflow:hidden}.reviewCard.tall{min-height:376px}.reviewCard .groupList{max-height:236px;border:1px solid #edf2f7;border-radius:8px;background:#fff}.reviewCard.tall .groupList{max-height:520px}.reviewCard textarea{min-height:116px}.reviewCard .sectionTitle{min-height:36px}.safetyControlCard{min-height:0;padding:12px 14px}.safetyControlRow{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}.safetyControlTitle{display:grid;gap:3px}.safetyControlTitle b{font-size:15px}.safetyNoticeField input{width:100%}.proxySummary{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px}.proxyBadge{border:1px solid #e8eef6;background:#fff;border-radius:8px;padding:9px 10px}.proxyBadge b{display:block;font-size:12px;margin-bottom:3px}.proxyBadge span{color:var(--muted);font-size:12px}.cacheCard{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;align-items:center}.cacheStat{border:1px solid #e8eef6;background:#fff;border-radius:8px;padding:10px}.cacheStat span{display:block;font-size:12px;color:var(--muted)}.cacheStat b{font-size:20px}.runtimeNote{border:1px solid #e8eef6;background:#fff;border-radius:8px;padding:10px;color:var(--muted)}
.safetyGrid{display:grid;grid-template-columns:minmax(280px,.72fr) minmax(460px,1.14fr) minmax(300px,.82fr);gap:14px;align-items:start}.safetyColumn{display:grid;gap:14px;align-content:start}.safetyEditorHead{display:grid;grid-template-columns:minmax(220px,1fr) auto auto;gap:10px;align-items:end}.safetyEditorHead .field{gap:5px}.safetyEditorHead select{width:100%}.safetyEditorCard textarea{min-height:96px}.safetyEditorCard #safetyBuiltinPreview{min-height:170px}.safetyWordGrid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.safetyWordGrid .wide{grid-column:1/-1}.safetyWordGrid textarea{min-height:148px}.safetyEnableCard{padding:12px}.safetyEnableCard .sectionTitle{align-items:flex-start}.safetyEnableCard .groupList{max-height:210px}.safetyEnableCard select{width:100%}
.groupTools{display:grid;grid-template-columns:1fr;gap:12px;margin-top:12px}.groupBox{border:1px solid var(--line);border-radius:10px;padding:12px;background:#fbfdff}.groupList{max-height:260px;overflow:auto;margin-top:8px}.groupItem{display:flex;gap:8px;align-items:flex-start;padding:7px 3px;border-bottom:1px solid #eef2f7}.groupItem:last-child{border-bottom:0}.groupItem span{font-size:13px}.groupItem small{display:block;color:var(--muted)}
.overviewList,.logList{display:grid;gap:8px}.infoLine,.logLine,.commandItem{border:1px solid #eef2f7;background:#fbfdff;border-radius:8px;padding:9px 10px}.infoLine b,.logLine b{display:block;margin-bottom:3px}.logLine{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;white-space:pre-wrap;word-break:break-word}.commandGrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:10px}.commandItem code{display:block;margin-top:5px;color:#0f172a}
.dailyGrid{display:grid;grid-template-columns:minmax(360px,1fr) minmax(360px,1fr);gap:12px;align-items:stretch}.dailyGrid.three{grid-template-columns:minmax(280px,.85fr) minmax(320px,1.05fr) minmax(300px,1fr);align-items:start}.dailyGrid.dailyBottom{grid-template-columns:minmax(620px,1.35fr) minmax(320px,.65fr);align-items:start}.dailyList{display:grid;gap:7px;max-height:330px;overflow:auto;padding-right:2px}.dailyList.compact{max-height:255px}.dailyItem{border:1px solid #e8eef6;background:#fff;border-radius:8px;padding:9px;display:grid;gap:7px}.dailyItemHead{display:flex;align-items:flex-start;justify-content:space-between;gap:10px}.dailyItemHead b{display:block}.dailyItemMeta{font-size:12px;color:var(--muted);word-break:break-all}.dailyActions{display:flex;gap:8px;flex-wrap:wrap}.dailyActions button{height:30px;padding:0 10px}.dailyActions .primary{padding:0 12px}.dailyBadge{display:inline-flex;align-items:center;height:22px;border:1px solid #dbeafe;background:#eff6ff;color:#1d4ed8;border-radius:999px;padding:0 8px;font-size:12px}.dailyBadge.readonly{border-color:#e5e7eb;background:#f8fafc;color:#64748b}.dailyFilters{display:grid;grid-template-columns:130px minmax(0,1fr);gap:8px;align-items:center;margin-bottom:8px}.dailyFilters select,.dailyFilters input{min-width:0;width:100%}.dailyCommandBox textarea{min-height:82px}.dailyParamList{font-size:12px;color:var(--muted);display:flex;gap:6px;flex-wrap:wrap}.dailyParamList span{border:1px solid #e5e7eb;border-radius:999px;padding:2px 7px;background:#f8fafc}.dailyAccessCard{gap:10px}.dailyAccessCard .settingsFields{grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}.dailyAccessLists{display:grid;grid-template-columns:1fr;gap:8px}.dailyAccessLists .groupBox{padding:9px}.dailyAccessLists .groupList{max-height:132px}.dailyAccessLists textarea{min-height:64px}.dailyTaskCard .settingsFields{grid-template-columns:1fr 1fr 1.35fr 1.35fr;align-items:end}.dailyTaskCard .field{min-width:0}.dailyTaskCard .field.span2{grid-column:span 2}.dailyTaskActions{display:flex;justify-content:space-between;gap:10px;align-items:center;margin-top:2px}.dailyTaskActions .row{gap:8px}.dailySaveCard{position:sticky;top:76px}.dailySaveCard textarea{min-height:92px}.dailySaveActions{display:grid;grid-template-columns:1fr auto;gap:10px;align-items:center}.dailySaveActions button{height:38px}.dailyHint{border:1px solid #e8eef6;background:#fff;border-radius:8px;padding:9px 10px;color:#64748b;font-size:12px}
.dailyTaskTarget{display:grid;grid-template-columns:88px minmax(0,1fr);gap:8px}.dailyFormatView{height:34px;display:flex;align-items:center;border:1px solid var(--line);border-radius:8px;background:#f8fafc;padding:0 10px;color:#475569;font-weight:650;white-space:nowrap}
.logoWrap{display:grid;grid-template-columns:92px minmax(240px,1fr);gap:10px;align-items:center}.logoPreview{width:92px;height:42px;object-fit:contain;border:1px solid var(--line);border-radius:8px;background:#fff}.logoEmpty{width:92px;height:42px;display:flex;align-items:center;justify-content:center;border:1px dashed var(--line);border-radius:8px;color:var(--muted);background:#fafbfc;font-size:12px}.logoTools{display:grid;grid-template-columns:auto minmax(160px,1fr) auto;gap:8px;align-items:center}.logoTools input[type=text]{width:100%}
.lastMsg{max-height:76px;overflow:hidden;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;word-break:break-all;overflow-wrap:anywhere;margin-bottom:0;background:#f8fafc;border:1px solid var(--line);border-radius:8px;padding:10px}.lastMsg.expanded{max-height:220px;overflow:auto;display:block;-webkit-line-clamp:unset}
.switch{position:relative;display:inline-block;width:42px;height:24px;flex:0 0 auto}.switch input{display:none}.slider{position:absolute;inset:0;background:#cbd5e1;border-radius:999px;transition:.15s}.slider:before{content:"";position:absolute;width:20px;height:20px;left:2px;top:2px;background:white;border-radius:50%;transition:.15s;box-shadow:0 1px 3px #0002}.switch input:checked+.slider{background:var(--blue)}.switch input:checked+.slider:before{transform:translateX(18px)}
.ok{color:var(--green);font-weight:700}.bad{color:var(--red);font-weight:700}.msg{min-height:20px;color:var(--muted)}.statusDot{display:inline-flex;align-items:center;gap:6px}.statusDot:before{content:"";width:8px;height:8px;background:var(--green);border-radius:50%;box-shadow:0 0 0 4px #dcfce7}
@media(max-width:980px){header{height:auto;min-height:58px;padding:10px 14px;gap:10px;align-items:flex-start}header .toolbar{justify-content:flex-end}h1{font-size:18px}.app{display:block;min-height:calc(100vh - 58px)}.sidebar{position:sticky;top:58px;z-index:15;padding:8px 10px;border-right:0;border-bottom:1px solid rgba(148,163,184,.28);background:rgba(255,255,255,.86);backdrop-filter:blur(14px)}.brand{display:none}.nav{display:flex;flex-direction:row;gap:8px;overflow-x:auto;overscroll-behavior-x:contain;padding:2px 2px 6px;scrollbar-width:thin}.nav a{flex:0 0 auto;white-space:nowrap;padding:8px 12px;background:rgba(255,255,255,.68);border:1px solid rgba(203,213,225,.75)}.nav a.active{background:var(--blue);color:#fff;border-color:var(--blue)}.subnav{flex-wrap:nowrap;overflow-x:auto;overscroll-behavior-x:contain;padding-bottom:4px}.subnav button{flex:0 0 auto}.pluginHead{display:block}.grid,.accessGrid,.groupTools,.logoWrap,.logoTools,.settingsGrid,.settingsFields,.dailyTaskCard .settingsFields,.reviewGrid,.safetyGrid,.safetyEditorHead,.proxySummary,.cacheCard,.dailyGrid{grid-template-columns:1fr}.reviewCard,.reviewCard.tall{min-height:auto}.span2,.span4,.dailyTaskCard .field.span2{grid-column:span 1}.dailyTaskActions,.dailySaveActions{display:flex;align-items:stretch;flex-direction:column}.dailySaveCard{position:static}.wrap{padding:14px}.hero{align-items:flex-start;flex-direction:column}.hero h2{font-size:24px}.toolbar .primary{height:34px;padding:0 12px}table{font-size:12px;display:block;overflow-x:auto}th,td{padding:8px}.panel{border-radius:9px;padding:14px}.metric b{font-size:24px}}
</style>
</head>
<body>
<header><h1>ZeroBot 控制台</h1><div class="toolbar"><button class="primary hidden" id="restartTop" onclick="restartSystem()">重启进程</button><span class="statusDot" id="topState">加载中</span><button class="primary hidden" id="topSaveButton" onclick="save()">保存配置</button></div></header>
<div class="app">
<aside class="sidebar">
<div class="brand">ZBP Console</div>
<nav class="nav">
<a class="active" href="#overview" data-page-link="overview" onclick="showPage('overview')">总览</a>
<a href="#system" data-page-link="system" onclick="showPage('system')">全局设置</a>
<a href="#plugins" data-page-link="plugins" onclick="showPage('plugins')">插件中心</a>
<a href="#logs" data-page-link="logs" onclick="showPage('logs')">日志诊断</a>
<a href="#maintenance" data-page-link="maintenance" onclick="showPage('maintenance')">数据维护</a>
</nav>
</aside>
<main class="wrap">
<div class="hero"><div><h2>机器人控制台</h2><p>查看运行状态，管理 OneBot、QQBot 与 Telegram 通道。</p></div><div class="toolbar"><button onclick="refreshStatus()">刷新状态</button><button class="danger" onclick="clearCache()">清理缓存</button></div></div>
<section class="grid">
<div class="span4" id="overview"></div>
<div class="panel metric page active" data-page="overview"><span class="muted">服务状态</span><b id="svc">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">机器人账号</span><b id="self">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">解析成功</span><b id="okn">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">解析失败</span><b id="failn">-</b></div>
<div class="panel span2 page active" data-page="overview"><div class="sectionTitle"><b>最近消息</b><button class="right" onclick="toggleLastMsg()">展开</button></div><p class="muted lastMsg" id="lastMsg">-</p></div>
<div class="panel span2 page active" data-page="overview"><div class="sectionTitle"><b>运行信息</b></div><div class="overviewList" id="runtimeSummary">-</div></div>
<div class="panel span4 page active" data-page="overview"><div class="sectionTitle"><b>通道与插件</b><span class="muted">连接、策略和图片访问。</span></div><div class="overviewList" id="overviewDetails">-</div></div>
<div class="panel span4 page" data-page="system" id="system">
<div class="sectionTitle"><b>全局设置</b><span class="muted">端口和 WS 需重启。</span><span class="right msg" id="systemMsg"></span></div>
<div class="accessGrid">
<label class="field">WebUI 监听地址 <input id="sysWebui" placeholder="0.0.0.0:3000"></label>
<label class="field">OneBot WS 地址 <input id="sysWS" placeholder="ws://127.0.0.1:3001"></label>
<label class="field">OneBot Token <input id="sysToken" type="password" placeholder="留空表示不修改"></label>
<label class="field">OneBot 可见数据目录 <input id="onebotDataDir" placeholder="/home/jay/apps/mediaparser/data"><span class="muted">用于 file:// 路径映射。</span></label>
<label class="field">机器人昵称 <input id="sysNick" placeholder="ZeroBot"></label>
<label class="field">命令前缀 <input id="sysPrefix" placeholder="/"></label>
<label class="field">超级管理员 QQ <textarea id="sysSuperUsers" placeholder="一行一个 QQ"></textarea></label>
</div>
<div class="row" style="margin-top:12px"><button class="primary" onclick="saveSystemSettings()">保存全局设置</button><span class="muted" id="sysPending"></span></div>
<div class="settingsCard" style="margin-top:14px">
<div class="sectionTitle"><b>WebUI 登录账户</b><span class="muted">密码加盐哈希保存。</span></div>
<div class="settingsFields">
<label class="field">用户名 <input id="webAuthUser" autocomplete="username" placeholder="admin"></label>
<label class="field">新密码 <input id="webAuthPass" type="password" autocomplete="new-password" placeholder="输入新密码"></label>
<label class="field">确认新密码 <input id="webAuthPass2" type="password" autocomplete="new-password" placeholder="再次输入新密码"></label>
</div>
<div class="row"><button class="primary" onclick="saveWebAuth()">保存登录账户</button><span class="muted" id="webAuthMsg"></span></div>
</div>
</div>
<div class="panel span4 page" data-page="plugins" id="plugins">
<div class="sectionTitle"><b>插件中心</b><span class="muted">插件入口集中管理。</span></div>
<table><thead><tr><th>插件</th><th>状态</th><th>说明</th><th>操作</th></tr></thead><tbody><tr><td><b>聚合解析</b><div class="muted">Media Parser</div></td><td><span class="ok">已启用</span></td><td>短视频、图文、动态、商品链接解析</td><td><button onclick="showPage('mediaparser:basic')">进入配置</button></td></tr><tr><td><b>MediaShield</b><div class="muted">X Media Shield</div></td><td><span id="mediaShieldPluginStatus" class="muted">未启用</span></td><td>X 平台媒体打码预览与加密打包</td><td><button onclick="showPage('mediashield')">进入配置</button></td></tr><tr><td><b>官方 QQBot</b><div class="muted">Official QQBot</div></td><td><span id="qqbotPluginStatus" class="muted">检测中</span></td><td id="qqbotPluginDesc">QQ 官方机器人通道，第一阶段接入媒体解析</td><td><button id="qqbotPluginButton" onclick="showPage('qqbot')">进入配置</button></td></tr><tr><td><b>Telegram Bot</b><div class="muted">Telegram Channel</div></td><td><span id="tgbotPluginStatus" class="muted">检测中</span></td><td id="tgbotPluginDesc">Telegram 长轮询通道，可接入全部插件消息</td><td><button id="tgbotPluginButton" onclick="showPage('tgbot')">进入配置</button></td></tr><tr><td><b>60s 技能中心</b><div class="muted">60s Skills</div></td><td><span class="ok">已启用</span></td><td>新闻、热榜、天气、查询和工具接口</td><td><button onclick="showPage('dailynews')">查看配置</button></td></tr><tr><td><b>🦌管签到</b><div class="muted">Deer Pipe</div></td><td><span id="deerPipePluginStatus" class="ok">已启用</span></td><td>每月🦌管签到、帮🦌、补🦌、🦌历与群🦌榜</td><td><button onclick="showPage('deerpipe')">进入配置</button></td></tr><tr><td><b>控制功能</b><div class="muted">Manager</div></td><td><span class="ok">已启用</span></td><td>基础群管理和机器人控制能力</td><td><button onclick="showPage('manager')">查看功能</button></td></tr></tbody></table>
</div>
<div class="panel span4 page" data-page="manager" id="manager">
<div class="pluginHead"><div><div class="crumb">插件中心 / 控制功能</div><div class="sectionTitle"><b>控制功能</b><span class="muted">群管理和提醒。</span></div></div><button onclick="showPage('plugins')">返回插件中心</button></div>
<div class="commandGrid" style="margin-top:12px">
<div class="commandItem"><b>群管理</b><code>踢出群聊 QQ</code><code>开启全员禁言 / 解除全员禁言</code><code>禁言 @成员 10分钟 / 解除禁言 QQ</code></div>
<div class="commandItem"><b>成员资料</b><code>修改名片 QQ 名片</code><code>修改头衔 QQ 头衔</code><code>申请头衔 头衔</code></div>
<div class="commandItem"><b>欢迎与告别</b><code>设置欢迎语 文本</code><code>测试欢迎语</code><code>设置告别辞 文本</code><code>测试告别辞</code></div>
<div class="commandItem"><b>入群与精华</b><code>开启入群验证 / 关闭入群验证</code><code>开启gist加群自动审批</code><code>精华列表 / 取消精华 消息ID</code></div>
<div class="commandItem"><b>提醒</b><code>在&quot;cron&quot;时提醒大家 内容</code><code>列出所有提醒</code><code>取消在&quot;cron&quot;的提醒</code></div>
<div class="commandItem"><b>轻量功能</b><code>翻牌</code><code>赞我</code><code>群签到</code><code>回应表情 表情</code></div>
</div>
<p class="muted" style="margin-bottom:0">沿用聊天内权限判断。</p>
</div>
<div class="panel span4 page" data-page="dailynews" id="dailynews">
<div class="pluginHead"><div><div class="crumb">插件中心 / 60s 技能中心</div><div class="sectionTitle"><b>60s 技能中心</b><span class="muted">新闻、热榜、天气、查询和工具接口。</span></div></div><div class="row"><button onclick="loadDailyNewsConfig(true)">刷新本页</button><button onclick="showPage('plugins')">返回插件中心</button></div></div>
<div class="dailyGrid three">
<div class="settingsCard dailyAccessCard">
<div class="sectionTitle"><b>访问控制</b><span class="muted" id="dailyNewsMsg"></span></div>
<div class="controlPills"><label class="row">总开关 <span id="dailyEnabledSwitch"></span></label><label class="row">私聊响应 <span id="dailyPrivateSwitch"></span></label></div>
<div class="settingsFields">
<label class="field">旧命令兜底接口 <select id="dailyDefaultSource"></select></label>
<label class="field">旧命令格式 <select id="dailyDefaultFormat"><option value="image">图片</option><option value="text">文本</option><option value="markdown">Markdown</option><option value="json">JSON</option></select></label>
<label class="field">群模式 <select id="dailyGroupMode" onchange="renderDailyGroupPickers()"><option value="none">所有群开启</option><option value="whitelist">只开白名单群</option><option value="blacklist">关闭黑名单群</option></select></label>
<label class="field">个人模式 <select id="dailyPrivateMode" onchange="renderDailyPrivatePickers()"><option value="none">所有人开启</option><option value="whitelist">只开白名单</option><option value="blacklist">关闭黑名单</option></select></label>
</div>
<div class="row"><button onclick="loadGroups(true)">刷新群列表</button><span class="muted">勾选后保存生效</span></div>
<div class="dailyAccessLists">
<div class="groupBox" id="dailyWhiteBox"><div class="row"><b>群白名单</b><input id="dailyGroupWhiteSearch" placeholder="搜索群" oninput="renderDailyGroupPickers()"></div><div class="groupList" id="dailyGroupWhitePicker"></div></div>
<div class="groupBox" id="dailyBlackBox"><div class="row"><b>群黑名单</b><input id="dailyGroupBlackSearch" placeholder="搜索群" oninput="renderDailyGroupPickers()"></div><div class="groupList" id="dailyGroupBlackPicker"></div></div>
<div class="groupBox" id="dailyPrivateWhiteBox"><div class="row"><b>个人白名单</b><span class="muted">每行一个 QQ</span></div><textarea id="dailyPrivateWhitelist" placeholder="123456&#10;234567" oninput="markDailyPrivateChanged()"></textarea></div>
<div class="groupBox" id="dailyPrivateBlackBox"><div class="row"><b>个人黑名单</b><span class="muted">每行一个 QQ</span></div><textarea id="dailyPrivateBlacklist" placeholder="123456&#10;234567" oninput="markDailyPrivateChanged()"></textarea></div>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>技能列表</b><span class="muted">选择后编辑命令和定时。</span></div>
<div class="dailyFilters"><select id="dailyCategoryFilter" onchange="renderDailySources()"></select><input id="dailySourceSearch" placeholder="搜索技能/命令" oninput="renderDailySources()"></div>
<div id="dailySources" class="dailyList compact"></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>当前技能</b><span class="muted" id="dailySelectedMeta">选择一个技能。</span></div>
<label class="field dailyCommandBox">监听命令 <textarea id="dailySourceCommands" placeholder="每行一个命令"></textarea></label>
<div class="dailyParamList" id="dailySourceParams"></div>
<div class="row"><button onclick="saveDailySourceCommands()">更新命令</button><button onclick="toggleDailySelectedSource()">切换开关</button></div>
<div class="sectionTitle" style="margin-top:10px"><b>添加接口</b><span class="muted">自定义接口可编辑。</span></div>
<div class="settingsFields">
<label class="field">ID <input id="dailySourceID" placeholder="my-news"></label>
<label class="field">名称 <input id="dailySourceName" placeholder="自定义接口"></label>
<label class="field">分类 <input id="dailySourceCategory" placeholder="custom"></label>
<label class="field span2">URL <input id="dailySourceURL" placeholder="https://example.com/api"></label>
<label class="field">格式 <select id="dailySourceEncoding"><option value="json">json</option><option value="text">text</option><option value="markdown">markdown</option><option value="image">image</option><option value="image-proxy">image-proxy</option></select></label>
<label class="field">超时秒数 <input id="dailySourceTimeout" type="number" min="1" max="120" value="20"></label>
</div>
<div class="row"><button onclick="addDailySource()">添加/更新接口</button><button onclick="clearDailySourceForm()">清空</button></div>
</div>
</div>
<div class="dailyGrid dailyBottom" style="margin-top:14px">
<div class="settingsCard dailyTaskCard">
<div class="sectionTitle"><b>定时任务</b><span class="muted">编辑后保存，立即写入配置并生效。</span></div>
<div class="settingsFields">
<label class="field">任务ID <input id="dailyTaskID" placeholder="morning"></label>
<label class="field">接口 <select id="dailyTaskSource" onchange="updateDailyTaskFormat()"></select></label>
<label class="field span2">目标 <span class="dailyTaskTarget"><select id="dailyTaskTargetType" onchange="updateDailyTaskTargetMode()"><option value="group">群聊</option><option value="private">私聊</option></select><select id="dailyTaskGroup"></select><input id="dailyTaskPrivate" class="hidden" placeholder="输入 QQ 号"></span></label>
<label class="field span2">Cron <input id="dailyTaskCron" placeholder="30 8 * * *"></label>
<label class="field">发送格式 <span id="dailyTaskFormatView" class="dailyFormatView">随接口</span><input id="dailyTaskFormat" type="hidden" value="image"></label>
<label class="field">启用 <span class="row"><label class="switch"><input id="dailyTaskEnabled" type="checkbox" checked><span class="slider"></span></label></span></label>
</div>
<div class="dailyTaskActions"><span class="dailyHint">编辑已有任务会填入上方表单；保存后按任务 ID 覆盖。</span><span class="row"><button class="primary" onclick="saveDailyTask()">保存任务并生效</button><button onclick="clearDailyTaskForm()">清空</button></span></div>
<div id="dailyTasks" class="dailyList" style="margin-top:10px"></div>
</div>
<div class="settingsCard dailySaveCard">
<div class="sectionTitle"><b>全局保存</b><span class="muted">保存访问控制、技能命令和自定义接口。</span></div>
<label class="field">兼容命令 <textarea id="dailyCommands" placeholder="每行一个，例如：今日早报"></textarea></label>
<div class="dailySaveActions"><button class="primary" onclick="saveDailyNews()">保存全部配置</button><button onclick="testDailyNews()">怎么测试</button></div>
<div class="dailyHint">任务卡片可单独保存；其它开关、名单和命令在这里统一保存。</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="deerpipe" id="deerpipe">
<div class="pluginHead"><div><div class="crumb">插件中心 / 🦌管签到</div><div class="sectionTitle"><b>🦌管签到</b><span class="muted">每月🦌管签到，移植自 nonebot-plugin-deer-pipe。</span></div></div><div class="row"><button class="primary" onclick="saveDeerPipe()">保存配置</button><button onclick="loadDeerPipeConfig(true)">刷新本页</button><button onclick="showPage('plugins')">返回插件中心</button></div></div>
<div class="dailyGrid three">
<div class="settingsCard dailyAccessCard">
<div class="sectionTitle"><b>访问控制</b><span class="muted" id="deerPipeMsg"></span></div>
<div class="controlPills"><label class="row">总开关 <span id="deerEnabledSwitch"></span></label><label class="row">私聊响应 <span id="deerPrivateSwitch"></span></label></div>
<div class="settingsFields">
<label class="field">个人模式 <select id="deerPrivateMode" onchange="onDeerAccessModeChange()"><option value="none">所有人开启</option><option value="whitelist">只开白名单</option><option value="blacklist">关闭黑名单</option></select></label>
<label class="field">群模式 <select id="deerGroupMode" onchange="onDeerAccessModeChange()"><option value="none">所有群开启</option><option value="whitelist">只开白名单群</option><option value="blacklist">关闭黑名单群</option></select></label>
<label class="field span2">群成员模式 <select id="deerGroupUserMode" onchange="onDeerAccessModeChange()"><option value="none">不限制</option><option value="whitelist">只允许白名单成员</option><option value="blacklist">屏蔽黑名单成员</option></select></label>
</div>
<div class="row"><button onclick="loadGroups(true)">刷新群列表</button><span class="muted">勾选后保存生效</span></div>
<div class="dailyAccessLists">
<div class="groupBox deerAccessField" data-mode="deerGroupMode" data-kind="whitelist" id="deerGroupWhiteBox"><div class="row"><b>群白名单</b><input id="deerGroupWhiteSearch" placeholder="搜索群" oninput="renderDeerGroupPickers()"></div><div class="groupList" id="deerGroupWhitePicker"></div></div>
<div class="groupBox deerAccessField" data-mode="deerGroupMode" data-kind="blacklist" id="deerGroupBlackBox"><div class="row"><b>群黑名单</b><input id="deerGroupBlackSearch" placeholder="搜索群" oninput="renderDeerGroupPickers()"></div><div class="groupList" id="deerGroupBlackPicker"></div></div>
<div class="groupBox deerAccessField" data-mode="deerPrivateMode" data-kind="whitelist"><div class="row"><b>个人白名单</b><span class="muted">每行一个 QQ</span></div><textarea id="deerPrivateWhitelist" placeholder="123456&#10;234567" oninput="markDeerChanged()"></textarea></div>
<div class="groupBox deerAccessField" data-mode="deerPrivateMode" data-kind="blacklist"><div class="row"><b>个人黑名单</b><span class="muted">每行一个 QQ</span></div><textarea id="deerPrivateBlacklist" placeholder="123456&#10;234567" oninput="markDeerChanged()"></textarea></div>
<div class="groupBox deerAccessField" data-mode="deerGroupUserMode" data-kind="whitelist"><div class="row"><b>群成员白名单</b><span class="muted">只在群聊里检查发言人 QQ</span></div><textarea id="deerGroupUserWhitelist" placeholder="123456&#10;234567" oninput="markDeerChanged()"></textarea></div>
<div class="groupBox deerAccessField" data-mode="deerGroupUserMode" data-kind="blacklist"><div class="row"><b>群成员黑名单</b><span class="muted">想屏蔽某人就填这里</span></div><textarea id="deerGroupUserBlacklist" placeholder="123456&#10;234567" oninput="markDeerChanged()"></textarea></div>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>功能开关</b><span class="muted">立即生效，无需重启。</span></div>
<div class="controlPills"><label class="row">帮别人🦌 <span id="deerHelpSwitch"></span></label><label class="row">补🦌 <span id="deerMakeupSwitch"></span></label><label class="row">🦌榜 <span id="deerRankSwitch"></span></label></div>
<div class="settingsFields">
<label class="field">禁🦌最长天数 <input id="deerBanMaxDays" type="number" min="1" max="365" value="30" oninput="markDeerChanged()"></label>
</div>
<div class="sectionTitle" style="margin-top:10px"><b>运行统计</b><span class="muted">本月数据。</span></div>
<div class="proxySummary" id="deerStats"><div class="proxyBadge"><b>-</b><span>加载中</span></div></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>指令说明</b><span class="muted">“🦌”均可换成“鹿”字。</span></div>
<div class="commandGrid">
<div class="commandItem"><b>签到</b><code>🦌</code><code>🦌 @xxx（帮xxx🦌，仅群聊）</code></div>
<div class="commandItem"><b>补签</b><code>补🦌 x（补本月x日）</code></div>
<div class="commandItem"><b>日历</b><code>🦌历</code><code>🦌历 @xxx（仅群聊）</code></div>
<div class="commandItem"><b>排行</b><code>🦌榜（本月本群 Top5，仅群聊）</code></div>
<div class="commandItem"><b>帮🦌开关</b><code>帮🦌 on|off</code><code>帮🦌 on|off @xxx（仅群管理）</code></div>
<div class="commandItem"><b>禁🦌</b><code>禁🦌 @xxx 30m|2h|1天</code><code>禁🦌 @xxx（解禁，仅群管理）</code></div>
<div class="commandItem"><b>帮助</b><code>🦌帮助</code></div>
</div>
<div class="dailyHint">签到数据按群独立统计，每月自动清理上月记录。</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="qqbot" id="qqbot">
<div class="pluginHead"><div><div class="crumb">插件中心 / 官方 QQBot</div><div class="sectionTitle"><b>官方 QQBot</b><span class="muted">官方通道能力有限。</span></div></div><button onclick="showPage('plugins')">返回插件中心</button></div>
<div class="settingsGrid">
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>通道配置</b><span class="muted">保存后需重启。</span></div>
<div class="runtimeNote hidden" id="qqbotUnavailable">官方包无此功能。</div>
<div class="settingsFields">
<label class="field">启用官方 QQBot <span class="row"><label class="switch"><input id="qqbotEnabled" type="checkbox"><span class="slider"></span></label><span class="muted">与 OneBot 并行。</span></span></label>
<label class="field">通道名称 <input id="qqbotName" placeholder="qqbot"></label>
<label class="field">AppID <input id="qqbotAppID" autocomplete="off"></label>
<label class="field">AppSecret <input id="qqbotSecret" type="password" autocomplete="new-password" placeholder="留空表示不修改"></label>
<label class="field">默认用户 OpenID <input id="qqbotOpenID" autocomplete="off" placeholder="私聊主动发送兜底目标，可留空"></label>
<label class="field">默认群 OpenID <input id="qqbotGroupOpenID" autocomplete="off" placeholder="群聊主动发送兜底目标，可留空"></label>
<label class="field">图片公网根地址 <input id="qqbotPublicBase" autocomplete="off" placeholder="例如：https://你的域名/cache/"></label>
<label class="field">解析卡片 <span class="row"><label class="switch"><input id="qqbotCardEnabled" type="checkbox"><span class="slider"></span></label><span class="muted">发送卡片 PNG。</span></span></label>
<label class="field">媒体图片下载 <span class="row"><label class="switch"><input id="qqbotMediaEnabled" type="checkbox"><span class="slider"></span></label><span class="muted">只发图片。</span></span></label>
<label class="field">Markdown 发送 <span class="row"><label class="switch"><input id="qqbotMarkdown" type="checkbox"><span class="slider"></span></label><span class="muted">文本走 Markdown。</span></span></label>
</div>
<div class="row"><button class="primary" id="qqbotSaveButton" onclick="saveSystemSettings()">保存 QQBot 配置</button><span class="muted" id="qqbotSaveHint">公网根地址用于 Markdown 图片。</span></div>
</div>
</div>
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>能力范围</b><span class="muted">官方通道表现。</span></div>
<div class="commandGrid">
<div class="commandItem"><b>聚合解析</b><small class="ok">可用</small><code>文本链接解析</code><code>卡片 PNG 转公网 URL 后回发</code></div>
<div class="commandItem"><b>媒体图片</b><small class="ok">可选</small><code>开启后逐张发送图片</code><code>不发送视频，不合并转发</code></div>
<div class="commandItem"><b>控制功能</b><small class="muted">暂不接入</small><code>官方通道权限与事件模型差异较大</code></div>
</div>
<p class="muted" style="margin-bottom:0">白名单使用日志里的映射 user_id。</p>
</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="tgbot" id="tgbot">
<div class="pluginHead"><div><div class="crumb">插件中心 / Telegram Bot</div><div class="sectionTitle"><b>Telegram Bot</b><span class="muted">长轮询通道，保存后需重启。</span></div></div><div class="row"><button class="primary" id="tgbotSaveButton" onclick="saveSystemSettings()">保存 Telegram 配置</button><button onclick="showPage('plugins')">返回插件中心</button></div></div>
<div class="settingsGrid">
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>通道配置</b><span class="muted">使用 Bot Token 接入。</span></div>
<div class="runtimeNote hidden" id="tgbotUnavailable">Telegram 驱动未加载。</div>
<div class="settingsFields">
<label class="field">启用 Telegram Bot <span class="row"><label class="switch"><input id="tgbotEnabled" type="checkbox"><span class="slider"></span></label><span class="muted">与 OneBot 并行。</span></span></label>
<label class="field">通道名称 <input id="tgbotName" placeholder="telegram"></label>
<label class="field">Bot Token <input id="tgbotToken" type="password" autocomplete="new-password" placeholder="留空表示不修改"></label>
<label class="field">API Base <input id="tgbotAPIBase" autocomplete="off" placeholder="https://api.telegram.org"></label>
<label class="field">代理 <input id="tgbotProxy" autocomplete="off" placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:1080"></label>
<label class="field">媒体上传 <span class="row"><label class="switch"><input id="tgbotMediaEnabled" type="checkbox"><span class="slider"></span></label><span class="muted">图片、视频、文件走 Telegram 上传。</span></span></label>
</div>
<p class="muted" id="tgbotSaveHint" style="margin-bottom:0">首次启用需重启。</p>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>访问控制</b><span class="muted">只拦截 Telegram 通道。</span></div>
<div class="settingsFields">
<label class="field span2">TG 超级管理员 <textarea id="tgbotSuperUsers" placeholder="每行一个 Telegram 原始 user_id 或映射 ID"></textarea><span class="muted">只管理 TG 通道；不写入全局 QQ 管理员。</span></label>
<label class="field">私聊模式 <select id="tgbotPrivateMode" onchange="onTGBotAccessModeChange()"><option value="none">不限制</option><option value="whitelist">只允许白名单</option><option value="blacklist">屏蔽黑名单</option></select></label>
<label class="field">群/频道模式 <select id="tgbotGroupMode" onchange="onTGBotAccessModeChange()"><option value="none">不限制</option><option value="whitelist">只允许白名单</option><option value="blacklist">屏蔽黑名单</option></select></label>
<label class="field span2">群内发言人模式 <select id="tgbotGroupUserMode" onchange="onTGBotAccessModeChange()"><option value="none">不限制</option><option value="whitelist">只允许白名单</option><option value="blacklist">屏蔽黑名单</option></select></label>
</div>
<div class="accessGrid">
<label class="field tgbotAccessField" data-mode="tgbotPrivateMode" data-kind="whitelist">私聊白名单<textarea id="tgbotUserWhitelist" placeholder="每行一个 Telegram 原始 user_id 或映射 ID"></textarea></label>
<label class="field tgbotAccessField" data-mode="tgbotPrivateMode" data-kind="blacklist">私聊黑名单<textarea id="tgbotUserBlacklist" placeholder="每行一个 Telegram 原始 user_id 或映射 ID"></textarea></label>
<label class="field tgbotAccessField" data-mode="tgbotGroupMode" data-kind="whitelist">群/频道白名单<textarea id="tgbotGroupWhitelist" placeholder="每行一个 Telegram 原始 chat_id/group_id，支持负数"></textarea></label>
<label class="field tgbotAccessField" data-mode="tgbotGroupMode" data-kind="blacklist">群/频道黑名单<textarea id="tgbotGroupBlacklist" placeholder="每行一个 Telegram 原始 chat_id/group_id，支持负数"></textarea></label>
<label class="field tgbotAccessField" data-mode="tgbotGroupUserMode" data-kind="whitelist">群内发言人白名单<textarea id="tgbotGroupUserWhitelist" placeholder="每行一个 Telegram 原始 user_id 或映射 ID"></textarea></label>
<label class="field tgbotAccessField" data-mode="tgbotGroupUserMode" data-kind="blacklist">群内发言人黑名单<textarea id="tgbotGroupUserBlacklist" placeholder="每行一个 Telegram 原始 user_id 或映射 ID"></textarea></label>
</div>
<p class="muted" style="margin-bottom:0">优先填写 Telegram 原始 ID；旧的映射 ID 仍兼容。不写入 QQ 名单。</p>
</div>
</div>
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>使用提醒</b><span class="muted">群聊需注意隐私模式。</span></div>
<div class="commandGrid">
<div class="commandItem"><b>收消息</b><code>默认 getUpdates 长轮询</code><code>不需要公网 Webhook</code></div>
<div class="commandItem"><b>发媒体</b><code>sendPhoto / sendVideo / sendDocument</code><code>本地文件 multipart 上传</code></div>
<div class="commandItem"><b>群解析</b><code>BotFather 关闭 privacy mode 后可接收普通群消息</code><code>TG 名单只影响 TG 通道</code></div>
</div>
</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="mediaparser" id="mediaparserHead">
<div class="pluginHead"><div><div class="crumb">插件中心 / 聚合解析</div><div class="sectionTitle"><b>聚合解析</b><span class="muted">链接解析配置。</span></div></div><button onclick="showPage('plugins')">返回插件中心</button></div>
<div class="subnav" id="mediaparserTabs">
<button class="active" data-plugin-tab="basic" onclick="showPluginSection('basic')">基础开关</button>
<button data-plugin-tab="platforms" onclick="showPluginSection('platforms')">平台设置</button>
<button data-plugin-tab="access" onclick="showPluginSection('access')">黑白名单</button>
<button data-plugin-tab="group-platform" onclick="showPluginSection('group-platform')">群平台开关</button>
<button data-plugin-tab="safety" onclick="showPluginSection('safety')">内容安全</button>
<button data-plugin-tab="runtime" onclick="showPluginSection('runtime')">运行配置</button>
</div>
</div>
<div class="panel span2 page plugin-section active" data-page="mediaparser" data-plugin-section="basic" id="global"><div class="sectionTitle"><b>聚合解析总开关</b><span class="right msg" id="saveMsg"></span></div><div class="controlPills" id="globalControls"></div></div>
<div class="panel span2 page plugin-section active" data-page="mediaparser" data-plugin-section="basic"><div class="sectionTitle"><b>解析状态</b></div><p class="muted">解析成功 <b id="okn2">-</b> 次，失败 <b id="failn2">-</b> 次。</p></div>
<div class="span4 page plugin-section" data-page="mediaparser" data-plugin-section="platforms" id="platforms">
<div class="sectionTitle"><b>平台开关与 Logo</b><span class="muted">控制卡片和媒体发送。</span></div>
<table><thead><tr><th>平台</th><th>解析卡片</th><th>媒体下载</th><th>下载画质</th><th>Logo</th></tr></thead><tbody id="platformRows"></tbody></table>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="access" id="access">
<div class="sectionTitle"><b>访问控制</b><span class="muted">私聊、群、发言人分开。</span></div>
<div class="row" style="margin-top:12px">
<label>私聊模式 <select id="pmode" onchange="onAccessModeChange()"><option value="none">不限制</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
<label>群聊模式 <select id="gmode" onchange="onAccessModeChange()"><option value="none">不限制</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
<label>群成员模式 <select id="gumode" onchange="onAccessModeChange()"><option value="none">不限制</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
</div>
<div class="accessGrid">
<label class="field accessField" data-mode="pmode" data-kind="whitelist">私聊白名单<textarea id="userWhitelist" placeholder="每行一个。OneBot 填 QQ；QQBot 填映射 user_id。"></textarea><span class="muted">QQBot user_id 看日志。</span></label>
<label class="field accessField" data-mode="pmode" data-kind="blacklist">用户黑名单<textarea id="userBlacklist" placeholder="一行一个 QQ 号"></textarea></label>
<label class="field accessField" data-mode="gmode" data-kind="whitelist">群白名单<textarea id="groupWhitelist" placeholder="一行一个群号"></textarea></label>
<label class="field accessField" data-mode="gmode" data-kind="blacklist">群黑名单<textarea id="groupBlacklist" placeholder="一行一个群号"></textarea></label>
<label class="field accessField" data-mode="gumode" data-kind="whitelist">群成员白名单<textarea id="groupUserWhitelist" placeholder="只在群聊里检查发言人 QQ"></textarea></label>
<label class="field accessField" data-mode="gumode" data-kind="blacklist">群成员黑名单<textarea id="groupUserBlacklist" placeholder="想屏蔽某人就填这里"></textarea></label>
</div>
<div class="row" style="margin-top:12px">
<button onclick="loadGroups(true)">刷新群列表</button><span class="muted" id="groupMsg">用于快速勾选群名单</span>
</div>
<div class="groupTools">
<div class="groupBox accessField" data-mode="gmode" data-kind="whitelist"><div class="row"><b>勾选到群白名单</b><input id="groupWhiteSearch" placeholder="搜索群名或群号" oninput="renderGroupPickers()"></div><div class="groupList" id="groupWhitePicker"></div></div>
<div class="groupBox accessField" data-mode="gmode" data-kind="blacklist"><div class="row"><b>勾选到群黑名单</b><input id="groupBlackSearch" placeholder="搜索群名或群号" oninput="renderGroupPickers()"></div><div class="groupList" id="groupBlackPicker"></div></div>
</div>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="group-platform" id="group-platform">
<div class="sectionTitle"><b>群平台开关</b><span class="muted">只影响当前群。</span></div>
<div class="row" style="margin-top:12px">
<label>群 <select id="platformBlockGroupSelect" onchange="renderPlatformGroupBlock()"></select></label>
<button class="danger" onclick="clearGroupPlatformBlock()">清空当前群屏蔽</button>
<span class="muted" id="platformBlockMsg">勾选表示在当前群屏蔽该平台解析</span>
</div>
<div class="groupBox" style="margin-top:12px"><div class="row"><b>当前群的平台屏蔽</b><input id="platformBlockSearch" placeholder="搜索平台" oninput="renderPlatformGroupBlock()"></div><div class="groupList" id="platformBlockPicker"></div></div>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="safety" id="safety">
<div class="sectionTitle"><b>内容安全</b><span class="muted">分类词库和平台启用。</span><button class="primary right" onclick="save()">保存</button></div>
<div class="safetyGrid">
<div class="safetyColumn">
<div class="settingsCard safetyControlCard">
<div class="safetyControlRow">
<div class="safetyControlTitle"><b>开关</b><span class="muted">命中后停止发送。</span></div>
<div class="controlPills"><label class="row">启用屏蔽 <span id="safetyEnabledSwitch"></span></label><label class="row">X 敏感标记 <span id="safetyTwitterSensitiveSwitch"></span></label><label class="row">命中提示 <span id="safetyNoticeSwitch"></span></label></div>
</div>
<label class="field safetyNoticeField">命中提示文案<input id="safetyNoticeText" placeholder="内容触发安全屏蔽，已停止解析。" oninput="cfg.safety_filter_notice_text=this.value;markDirty()"></label>
</div>
</div>
<div class="safetyColumn">
<div class="settingsCard safetyEditorCard">
<div class="sectionTitle"><b id="safetyCategoryTitle">分类词库</b><span class="muted" id="safetyCategoryMeta">选择后预览和编辑。</span></div>
<div class="safetyEditorHead">
<label class="field">选择分类<select id="safetyCategorySelect" onchange="selectSafetyCategory(this.value)"></select></label>
<button onclick="addSafetyCustomCategory()">新建分类</button>
<button class="danger" id="safetyDeleteCategory" onclick="deleteSafetyCustomCategory()">删除分类</button>
</div>
<div class="settingsFields" id="safetyCategoryIdentity">
<label class="field">分类 ID <input id="safetyCategoryID" placeholder="custom_adult_extra" oninput="editSafetyCustomCategoryID()"></label>
<label class="field">分类名称 <input id="safetyCategoryLabel" placeholder="成人扩展" oninput="collectSafetyCategoryEditor();markDirty()"></label>
</div>
<div class="safetyWordGrid">
<label class="field wide" id="safetyBuiltinPreviewWrap">内置词预览<textarea id="safetyBuiltinPreview" readonly placeholder="内置分类会在这里显示只读词库"></textarea></label>
<label class="field"><span id="safetyCustomWordsLabel">自定义屏蔽词</span><textarea id="safetyCustomWords" placeholder="一行一个；支持普通词、* / ? 通配、re:正则" oninput="collectSafetyCategoryEditor();markDirty()"></textarea></label>
<label class="field"><span id="safetyCustomExcludesLabel">排除词 / 白名单</span><textarea id="safetyCustomExcludes" placeholder="一行一个；支持普通词、* / ? 通配、re:正则；命中本分类时优先放行" oninput="collectSafetyCategoryEditor();markDirty()"></textarea></label>
</div>
</div>
</div>
<div class="safetyColumn">
<div class="settingsCard safetyEnableCard">
<div class="sectionTitle"><b>全局启用分类</b><span class="muted">对所有平台生效。</span></div>
<div class="groupList" id="safetyGlobalCategories"></div>
</div>
<div class="settingsCard safetyEnableCard">
<div class="sectionTitle"><b>平台启用分类</b><span class="muted">选择平台额外分类。</span></div>
<select id="safetyPlatformSelect" onchange="renderSafetyPlatformCategories()"></select>
<div class="groupList" id="safetyPlatformCategories"></div>
</div>
</div>
</div>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="runtime" id="runtime">
<div class="sectionTitle"><b>运行配置</b><span class="muted">下载、代理、缓存、卡片和登录态。</span><button class="primary right" onclick="save()">保存</button></div>
<div class="settingsGrid">
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>下载规则</b><span class="muted">平台画质优先；超限只发卡片。</span></div>
<div class="settingsFields">
<label class="field">全局视频画质 <select id="res"><option value="0">不限</option><option value="360">360p</option><option value="720">720p</option><option value="1080">1080p</option></select></label>
<label class="field">最大发送体积 MB <input id="maxmb" type="number" min="1"></label>
<label class="field">缓存分钟 <input id="ttl" type="number" min="1"></label>
<label class="field">解析回应 <input id="reactionEmoji" maxlength="8" placeholder="🍉"></label>
<label class="field">失败回应 <input id="failReactionEmoji" maxlength="8" placeholder="❌"></label>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>代理与提取器</b><span class="muted">仅海外平台和 Linux.do 使用。</span></div>
<div class="settingsFields single">
<label class="field">YouTube extractor 参数 <input id="youtubeExtractorArgs" placeholder="youtube:player_client=default,android;formats=missing_pot"></label>
<label class="field">海外平台代理 <input id="proxy" placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:7890"><span class="muted">X/TikTok/YouTube/Instagram/Linux.do。</span></label>
</div>
<div class="proxySummary">
<div class="proxyBadge"><b>适用平台</b><span>X / TikTok / YouTube / Instagram / Linux.do</span></div>
<div class="proxyBadge"><b>连通提示</b><span>403 可能是站点风控</span></div>
<div class="proxyBadge"><b>当前配置</b><span id="proxySummaryText">未配置</span></div>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>浏览器截图</b><span class="muted">小黑盒当前使用，其他平台可复用。</span></div>
<div class="settingsFields single">
<label class="field">CDP 地址 <input id="browserCDPURL" placeholder="http://browser-host:9222"><span class="muted">填写 Chrome DevTools HTTP 地址；会自动读取动态调试 WebSocket。</span></label>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>缓存管理</b><span class="muted">查看或清理媒体缓存。</span></div>
<div class="cacheCard">
<div class="cacheStat"><span>缓存文件</span><b id="cacheFiles">-</b></div>
<div class="cacheStat"><span>占用空间</span><b id="cacheSize">-</b></div>
<button class="danger" onclick="clearCache()">清理缓存</button><button onclick="loadCacheStats()">刷新统计</button>
</div>
<div class="runtimeNote" id="cacheMsg">清理只影响媒体缓存，不会删除配置和 Cookie。</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>BBS 卡片样式</b><span class="muted">Keylol / Linux.do 共用。</span></div>
<div class="settingsFields single">
<label class="field">Keylol 底部文案 <input id="keylolFooter" placeholder="Keylol · {author} 发布 · {time}"></label>
</div>
<div class="controlPills"><label class="row">自动日夜 <span id="keylolThemeAutoSwitch"></span></label><label class="row">黑夜模式 <span id="keylolThemeDarkSwitch"></span></label><span class="muted">自动按北京时间切换。</span></div>
<div class="settingsFields">
<label class="field">日间配色 <span class="fieldInline"><select id="keylolLightTheme" onchange="cfg.keylol_light_theme=this.value;markDirty()"><option value="classic">经典</option><option value="blue">蓝调</option><option value="green">绿野</option><option value="white">纯白</option></select><button type="button" onclick="randomKeylolLightTheme()">随机</button></span></label>
<label class="field">夜间配色 <span class="fieldInline"><select id="keylolDarkTheme" onchange="cfg.keylol_dark_theme=this.value;markDirty()"><option value="black">纯黑</option><option value="dark">深色</option></select><button type="button" onclick="randomKeylolDarkTheme()">随机</button></span></label>
</div>
<div class="controlPills"><label class="row">Keylol ASF 合并转发 <span id="keylolASFForwardSwitch"></span></label><span class="muted">追加封面、名称、AppID。</span></div>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>平台登录态</b><span class="muted">留空保存会保留旧 Cookie。</span></div>
<div class="settingsFields">
<label class="field">B站 Cookie <textarea id="bilibiliCookie" placeholder="SESSDATA=...; bili_jct=..."></textarea></label>
<label class="field">小红书 Cookie <textarea id="xiaohongshuCookie" placeholder="a1=...; web_session=..."></textarea></label>
<label class="field">YouTube Cookie <textarea id="youtubeCookie" placeholder="VISITOR_INFO1_LIVE=...; SID=..."></textarea></label>
<label class="field">Instagram Cookie <textarea id="instagramCookie" placeholder="sessionid=...; ds_user_id=..."></textarea></label>
<label class="field">Keylol Cookie <textarea id="keylolCookie" placeholder="key=value; key2=value2"></textarea></label>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>CookieCloud 同步</b><span class="muted">只同步勾选平台。</span></div>
<div class="controlPills"><label class="row">启用同步 <span id="cookieCloudEnabledSwitch"></span></label><button type="button" onclick="syncCookieCloudNow()">立即同步</button><span class="muted" id="cookieCloudSyncMsg">默认关闭</span></div>
<div class="settingsFields">
<label class="field">服务端地址 <input id="cookieCloudServer" placeholder="http://10.10.10.x:8088"></label>
<label class="field">UUID <input id="cookieCloudUUID" placeholder="CookieCloud UUID"></label>
<label class="field">密码 <input id="cookieCloudPassword" type="password" placeholder="CookieCloud 密码"></label>
<label class="field">同步间隔分钟 <input id="cookieCloudInterval" type="number" min="5" placeholder="60"></label>
</div>
<div class="groupList compact" id="cookieCloudPlatforms"></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>Linux.do 过盾参数</b><span class="muted">Cookie 和 UA 需同源。</span></div>
<div class="settingsFields">
<label class="field">Linux.do Cookie <textarea id="linuxdoCookie" placeholder="_t=...; cf_clearance=..."></textarea></label>
<label class="field">Linux.do User-Agent <textarea id="linuxdoUA" placeholder="同一浏览器 UA；留空用默认"></textarea></label>
<label class="field">FlareSolverr 地址 <input id="linuxdoFlaresolverrURL" placeholder="http://10.10.10.x:8191"></label>
<label class="field">FlareSolverr 等待秒数 <input id="linuxdoFlaresolverrWait" type="number" min="0" placeholder="2"></label>
<label class="field">FlareSolverr 超时毫秒 <input id="linuxdoFlaresolverrTimeout" type="number" min="1000" placeholder="60000"></label>
</div>
<div class="controlPills"><label class="row">FlareSolverr 使用代理 <span id="linuxdoFlaresolverrProxySwitch"></span></label><span class="muted">Linux.do 优先使用 FlareSolverr。地址为空则使用普通请求。</span></div>
<div class="runtimeNote">日志会显示 403、页面标题和 Cloudflare 信号。</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="mediashield" id="mediashield">
<div class="pluginHead"><div><div class="crumb">插件中心 / MediaShield</div><div class="sectionTitle"><b>MediaShield</b><span class="muted">X 媒体打码和加密包。</span></div></div><button onclick="showPage('plugins')">返回插件中心</button></div>
<div class="settingsGrid">
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>触发开关</b><span class="muted">默认关闭。</span></div>
<div class="controlPills"><label class="row">总开关 <span id="mediaShieldEnabledSwitch"></span></label><label class="row">被动检测 <span id="mediaShieldPassiveSwitch"></span></label><label class="row">主动触发 <span id="mediaShieldActiveSwitch"></span></label></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>群开关</b><span class="muted">只在勾选群启用。</span></div>
<div class="row"><input id="mediaShieldGroupSearch" placeholder="搜索群名或群号" oninput="renderMediaShieldGroups()"><button onclick="loadGroups(true)">刷新群列表</button></div>
<div class="groupList compact" id="mediaShieldGroupPicker"></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>私聊白名单</b><span class="muted">开启后按名单触发。</span></div>
<div class="controlPills"><label class="row">允许私聊 <span id="mediaShieldPrivateSwitch"></span></label></div>
<label class="field">用户白名单<textarea id="mediaShieldUserEnabled" placeholder="每行一个用户 ID；OneBot 填 QQ 号，官方 QQBot 填日志里的映射 user_id" oninput="cfg.media_shield_user_enabled=parseList(this.value);markDirty()"></textarea></label>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>回复文案</b><span class="muted">{password} 会替换密码。</span></div>
<div class="settingsFields single">
<label class="field">密码回复文案 <input id="mediaShieldReplyText" placeholder="已打包，解压密码：{password}" oninput="cfg.media_shield_reply_text=this.value;markDirty()"></label>
<label class="field">主动回应 emoji <input id="mediaShieldEmoji" maxlength="12" placeholder="😏" oninput="cfg.media_shield_emoji=this.value;markDirty()"></label>
</div>
</div>
</div>
<div class="settingsStack">
<div class="settingsCard">
<div class="sectionTitle"><b>主动监听词</b><span class="muted">一行一个，支持通配和正则。</span></div>
<label class="field">关键词<textarea id="mediaShieldKeywords" placeholder="一行一个；例如 色色、打包、setu、大雷" oninput="cfg.media_shield_keywords=splitWords(this.value);markDirty()"></textarea></label>
<div class="row"><button class="primary" onclick="save()">保存 MediaShield 配置</button><span class="muted">仅作用于 X / Twitter 平台。</span></div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>被动检测词库</b><span class="muted">独立词库，互不影响。</span></div>
<div class="settingsFields">
<label class="field">被动检测词<textarea id="mediaShieldPassiveWords" placeholder="一行一个；支持普通词、* / ? 通配、re:正则" oninput="cfg.media_shield_passive_words=splitWords(this.value);markDirty()"></textarea></label>
<label class="field">排除词 / 白名单<textarea id="mediaShieldPassiveExcludes" placeholder="一行一个；命中这些词时不触发 MediaShield" oninput="cfg.media_shield_passive_excludes=splitWords(this.value);markDirty()"></textarea></label>
</div>
</div>
<div class="settingsCard">
<div class="sectionTitle"><b>当前链路</b><span class="muted">复用 X 解析和缓存。</span></div>
<div class="overviewList">
<div class="infoLine"><b>被动检测</b><span class="muted">成人命中后接管。</span></div>
<div class="infoLine"><b>主动触发</b><span class="muted">安全通过后触发。</span></div>
</div>
</div>
</div>
</div>
</div>
<div class="panel span4 page" data-page="logs" id="logs"><div class="sectionTitle"><b>日志诊断</b><span class="muted">Cookie 会脱敏。</span><button class="right" onclick="loadLogs()">刷新日志</button></div><div class="logList" id="logSummary">-</div></div>
<div class="panel span4 page" data-page="maintenance" id="maintenance"><div class="sectionTitle"><b>数据维护</b><span class="muted">缓存、Logo、配置。</span></div><div class="row"><button class="danger" onclick="clearCache()">清理媒体缓存</button><span class="muted" id="maintenanceMsg">配置只保存在本机 data。</span></div></div>
</section>
</main>
</div>
<script>
let cfg=null, sys=null, dailyNewsCfg=null, deerPipeCfg=null, deerPipeStats=null, platforms=[], logos={}, groups=[], cacheInfo=null, dirty=false, currentPluginSection='basic', safetyBuiltins=[], selectedSafetyCategory='adult';
const safetyCategories=[
 ['adult','色情'],
 ['ad','广告'],
 ['violence','暴恐'],
 ['politics','政治']
];
const $=id=>document.getElementById(id);
function showPage(name){
 const parts=String(name||'overview').split(':'); const page=parts[0]||'overview'; const section=parts[1]||currentPluginSection||'basic';
 document.querySelectorAll('.page').forEach(el=>el.classList.toggle('active', el.dataset.page===page));
 const sidePage=(page==='mediaparser'||page==='manager'||page==='qqbot'||page==='tgbot'||page==='mediashield'||page==='dailynews'||page==='deerpipe')?'plugins':page;
 document.querySelectorAll('[data-page-link]').forEach(el=>el.classList.toggle('active', el.dataset.pageLink===sidePage));
 if(page==='mediaparser') showPluginSection(section,false);
 updateHeaderSave(page);
 const nextHash=page==='mediaparser'?'#mediaparser:'+currentPluginSection:'#'+page;
 if(location.hash!==nextHash) history.replaceState(null,'',nextHash);
}
function updateHeaderSave(page){
 const btn=$('topSaveButton');
 if(!btn) return;
 const show=page==='mediaparser'||page==='mediashield';
 btn.classList.toggle('hidden', !show);
 btn.textContent='保存配置';
 btn.onclick=save;
}
function showPluginSection(name,updateHash=true){
 currentPluginSection=name||'basic';
 document.querySelectorAll('[data-plugin-section]').forEach(el=>el.classList.toggle('active', el.dataset.pluginSection===currentPluginSection));
 document.querySelectorAll('[data-plugin-tab]').forEach(el=>el.classList.toggle('active', el.dataset.pluginTab===currentPluginSection));
 if(updateHash && location.hash!=='#mediaparser:'+currentPluginSection) history.replaceState(null,'','#mediaparser:'+currentPluginSection);
}
function markDirty(){dirty=true; $('saveMsg').textContent='有未保存修改'}
function checked(v){return v?' checked':''}
function apiURL(path){
 const u=new URL(path, location.origin);
 u.username='';
 u.password='';
 return u.href;
}
function apiFetch(path, options){return fetch(apiURL(path), options)}
function switchHTML(expr,on){return '<label class="switch"><input type="checkbox"'+checked(on)+' onchange="'+expr+'=this.checked;markDirty()"><span class="slider"></span></label>'}
function actionSwitchHTML(fn,on){return '<label class="switch"><input type="checkbox"'+checked(on)+' onchange="'+fn+'(this.checked)"><span class="slider"></span></label>'}
function bindSwitch(map,key,name){return switchHTML("cfg."+key+"['"+name+"']", !!map[name])}
function bindParseCardSwitch(name){const on=!!(cfg.platform_enabled&&cfg.platform_enabled[name]);return '<label class="switch"><input type="checkbox"'+checked(on)+' data-platform="'+escapeHTML(name)+'" onchange="setPlatformParseCard(this.dataset.platform,this.checked)"><span class="slider"></span></label>'}
function bindMediaDownloadSwitch(name){const on=!!(cfg.platform_download_video&&cfg.platform_download_video[name]);return '<label class="switch"><input type="checkbox"'+checked(on)+' data-platform="'+escapeHTML(name)+'" onchange="setPlatformMediaDownload(this.dataset.platform,this.checked)"><span class="slider"></span></label>'}
function setPlatformParseCard(name,on){cfg.platform_enabled[name]=on; cfg.platform_info_card[name]=on; markDirty()}
function setPlatformMediaDownload(name,on){cfg.platform_download_video[name]=on; cfg.platform_send_media[name]=on; markDirty()}
function resolutionCell(name){const v=cfg.platform_video_resolution&&Object.prototype.hasOwnProperty.call(cfg.platform_video_resolution,name)?Number(cfg.platform_video_resolution[name]):'';return '<select data-platform="'+escapeHTML(name)+'" onchange="setPlatformResolution(this.dataset.platform,this.value)"><option value=""'+(v===''?' selected':'')+'>跟随全局</option><option value="0"'+(v===0?' selected':'')+'>不限</option><option value="360"'+(v===360?' selected':'')+'>360p</option><option value="720"'+(v===720?' selected':'')+'>720p</option><option value="1080"'+(v===1080?' selected':'')+'>1080p</option></select>'}
function setPlatformResolution(name,value){cfg.platform_video_resolution=cfg.platform_video_resolution||{}; if(value==='') delete cfg.platform_video_resolution[name]; else cfg.platform_video_resolution[name]=Number(value); markDirty()}
function setKeylolThemeAuto(on){cfg.keylol_theme=on?'auto':((cfg.keylol_theme==='dark'||cfg.keylol_theme==='night'||cfg.keylol_theme==='black')?'dark':'light');renderKeylolThemeSwitches();markDirty()}
function setKeylolThemeDark(on){cfg.keylol_theme=on?'dark':'light';renderKeylolThemeSwitches();markDirty()}
function renderKeylolThemeSwitches(){
 const mode=String((cfg&&cfg.keylol_theme)||'auto').toLowerCase();
 const auto=mode==='auto'||!mode;
 const dark=mode==='dark'||mode==='night'||mode==='black';
 $('keylolThemeAutoSwitch').innerHTML=actionSwitchHTML('setKeylolThemeAuto',auto);
 $('keylolThemeDarkSwitch').innerHTML=actionSwitchHTML('setKeylolThemeDark',auto?false:dark);
 const darkInput=$('keylolThemeDarkSwitch').querySelector('input');
 if(darkInput) darkInput.disabled=auto;
 if($('keylolLightTheme')) $('keylolLightTheme').value=keylolLightThemeValue(cfg&&cfg.keylol_light_theme);
 if($('keylolDarkTheme')) $('keylolDarkTheme').value=keylolDarkThemeValue(cfg&&cfg.keylol_dark_theme);
}
function keylolLightThemeValue(v){v=String(v||'classic').toLowerCase();return ['classic','blue','green','white'].includes(v)?v:'classic'}
function keylolDarkThemeValue(v){v=String(v||'black').toLowerCase();return v==='dark'?'dark':'black'}
function pickRandom(arr,current){const pool=arr.filter(x=>x!==current);return (pool.length?pool:arr)[Math.floor(Math.random()*(pool.length?pool.length:arr.length))]}
function randomKeylolLightTheme(){cfg.keylol_light_theme=pickRandom(['classic','blue','green','white'],keylolLightThemeValue(cfg.keylol_light_theme));renderKeylolThemeSwitches();markDirty()}
function randomKeylolDarkTheme(){cfg.keylol_dark_theme=pickRandom(['black','dark'],keylolDarkThemeValue(cfg.keylol_dark_theme));renderKeylolThemeSwitches();markDirty()}
function cookieCloudOptions(){return cfg.cookiecloud_platform_options||[{name:'bilibili',label:'B站'},{name:'xiaohongshu',label:'小红书'},{name:'youtube',label:'YouTube'},{name:'instagram',label:'Instagram'},{name:'keylol',label:'Keylol'},{name:'linuxdo',label:'Linux.do'}]}
function renderCookieCloudSettings(){
 if(!$('cookieCloudEnabledSwitch')) return;
 cfg.cookiecloud_platforms=cfg.cookiecloud_platforms||{};
 $('cookieCloudEnabledSwitch').innerHTML=switchHTML('cfg.cookiecloud_enabled',!!cfg.cookiecloud_enabled);
 $('cookieCloudServer').value=cfg.cookiecloud_server||'';
 $('cookieCloudUUID').value=cfg.cookiecloud_uuid||'';
 setSecretInput('cookieCloudPassword', cfg.cookiecloud_password_set, 'CookieCloud 密码');
 $('cookieCloudInterval').value=cfg.cookiecloud_interval_minutes||60;
 $('cookieCloudPlatforms').innerHTML=cookieCloudOptions().map(p=>'<label class="groupItem"><input type="checkbox" data-platform="'+escapeHTML(p.name)+'" '+checked(!!cfg.cookiecloud_platforms[p.name])+' onchange="cfg.cookiecloud_platforms[this.dataset.platform]=this.checked;markDirty()"><span><b>'+escapeHTML(p.label||p.name)+'</b><small>'+escapeHTML(p.name)+'</small></span></label>').join('');
}
function dailySourceOptions(selected){
 const sources=(dailyNewsCfg&&dailyNewsCfg.sources)||[];
 return sources.map(s=>'<option value="'+escapeHTML(s.id)+'"'+(s.id===selected?' selected':'')+'>'+escapeHTML(s.name||s.id)+' / '+escapeHTML(s.id)+'</option>').join('');
}
function dailyCategoryLabel(cat){
 const labels={news:'新闻',weather:'天气',hot:'热榜',data:'数据',fun:'娱乐',media:'影音',tool:'工具',custom:'自定义'};
 return labels[cat]||cat||'未分类';
}
function dailySelectedSource(){
 const id=(dailyNewsCfg&&dailyNewsCfg.selected_source)||dailyNewsCfg.default_source||'60s';
 return (dailyNewsCfg.sources||[]).find(x=>x.id===id)||(dailyNewsCfg.sources||[])[0]||null;
}
function renderDailyNewsSettings(){
 if(!dailyNewsCfg||!$('dailyDefaultSource')) return;
 dailyNewsCfg.sources=dailyNewsCfg.sources||[];
 dailyNewsCfg.schedules=dailyNewsCfg.schedules||[];
 dailyNewsCfg.commands=dailyNewsCfg.commands||[];
 dailyNewsCfg.access=dailyNewsCfg.access||{enabled:true,private_enabled:true,group_mode:'none'};
 $('dailyDefaultSource').innerHTML=dailySourceOptions(dailyNewsCfg.default_source||'60s');
 $('dailyDefaultFormat').value=dailyNewsCfg.default_format||'image';
 $('dailyCommands').value=dailyNewsCfg.commands.join('\n');
 $('dailyTaskSource').innerHTML=dailySourceOptions($('dailyTaskSource').value||dailyNewsCfg.default_source||'60s');
 $('dailyEnabledSwitch').innerHTML=actionSwitchHTML('setDailyEnabled',!!dailyNewsCfg.access.enabled);
 $('dailyPrivateSwitch').innerHTML=actionSwitchHTML('setDailyPrivateEnabled',!!dailyNewsCfg.access.private_enabled);
 $('dailyGroupMode').value=dailyNewsCfg.access.group_mode||'none';
 $('dailyPrivateMode').value=dailyNewsCfg.access.private_mode||'none';
 $('dailyPrivateWhitelist').value=dailyIDListText(dailyNewsCfg.access.private_whitelist);
 $('dailyPrivateBlacklist').value=dailyIDListText(dailyNewsCfg.access.private_blacklist);
 renderDailyCategoryFilter();
 renderDailySources();
 renderDailySelectedSource();
 renderDailyTasks();
 renderDailyGroupPickers();
 renderDailyPrivatePickers();
 renderDailyTaskGroupOptions();
 updateDailyTaskTargetMode();
 updateDailyTaskFormat();
}
function collectDailyNews(){
 if(!dailyNewsCfg) dailyNewsCfg={};
 dailyNewsCfg.access=dailyNewsCfg.access||{};
 dailyNewsCfg.default_source=$('dailyDefaultSource').value||'60s';
 dailyNewsCfg.default_format=$('dailyDefaultFormat').value||'image';
 dailyNewsCfg.commands=String($('dailyCommands').value||'').split(/\n+/).map(x=>x.trim()).filter(Boolean);
 dailyNewsCfg.access.group_mode=$('dailyGroupMode').value||'none';
 dailyNewsCfg.access.private_mode=$('dailyPrivateMode').value||'none';
 dailyNewsCfg.access.private_whitelist=dailyParseIDList($('dailyPrivateWhitelist').value);
 dailyNewsCfg.access.private_blacklist=dailyParseIDList($('dailyPrivateBlacklist').value);
 if(dailyNewsCfg.access.group_mode==='whitelist'){
  dailyNewsCfg.access.group_blacklist=[];
 }else if(dailyNewsCfg.access.group_mode==='blacklist'){
  dailyNewsCfg.access.group_whitelist=[];
 }else{
  dailyNewsCfg.access.group_whitelist=[];
  dailyNewsCfg.access.group_blacklist=[];
 }
 if(dailyNewsCfg.access.private_mode==='whitelist'){
  dailyNewsCfg.access.private_blacklist=[];
 }else if(dailyNewsCfg.access.private_mode==='blacklist'){
  dailyNewsCfg.access.private_whitelist=[];
 }else{
  dailyNewsCfg.access.private_whitelist=[];
  dailyNewsCfg.access.private_blacklist=[];
 }
}
function setDailyEnabled(v){dailyNewsCfg.access=dailyNewsCfg.access||{};dailyNewsCfg.access.enabled=!!v;renderDailyNewsSettings();$('dailyNewsMsg').textContent='总开关已修改，记得保存'}
function setDailyPrivateEnabled(v){dailyNewsCfg.access=dailyNewsCfg.access||{};dailyNewsCfg.access.private_enabled=!!v;renderDailyNewsSettings();$('dailyNewsMsg').textContent='私聊开关已修改，记得保存'}
function dailyEncodingLabel(v){return String(v||'json')}
function renderDailyCategoryFilter(){
 const cats=[...new Set((dailyNewsCfg.sources||[]).map(s=>s.category||'custom'))].sort();
 const current=$('dailyCategoryFilter').value||'all';
 $('dailyCategoryFilter').innerHTML='<option value="all">全部分类</option>'+cats.map(c=>'<option value="'+escapeHTML(c)+'"'+(c===current?' selected':'')+'>'+escapeHTML(dailyCategoryLabel(c))+'</option>').join('');
}
function renderDailySources(){
 const cat=$('dailyCategoryFilter')?$('dailyCategoryFilter').value:'all';
 const q=String(($('dailySourceSearch')&&$('dailySourceSearch').value)||'').trim().toLowerCase();
 const list=(dailyNewsCfg.sources||[]).slice().filter(s=>{
  const text=[s.id,s.name,s.desc,s.category,(s.commands||[]).join(' ')].join(' ').toLowerCase();
  return (cat==='all'||(s.category||'custom')===cat) && (!q||text.includes(q));
 }).sort((a,b)=>String(a.category||'').localeCompare(String(b.category||''))||String(a.id).localeCompare(String(b.id)));
 $('dailySources').innerHTML=list.map(s=>{
  const readonly=!!s.builtin;
  const enabled=!s.disabled;
  const badge=(enabled?'<span class="dailyBadge">开启</span>':'<span class="dailyBadge readonly">关闭</span>')+(readonly?'<span class="dailyBadge readonly">内置</span>':'<span class="dailyBadge">自定义</span>');
  const actions=readonly
   ? '<button data-id="'+escapeHTML(s.id)+'" onclick="selectDailySource(this.dataset.id)">选择</button><button data-id="'+escapeHTML(s.id)+'" onclick="toggleDailySourceEnabled(this.dataset.id)">'+(enabled?'关闭':'开启')+'</button>'
   : '<button data-id="'+escapeHTML(s.id)+'" onclick="selectDailySource(this.dataset.id)">选择</button><button data-id="'+escapeHTML(s.id)+'" onclick="toggleDailySourceEnabled(this.dataset.id)">'+(enabled?'关闭':'开启')+'</button><button data-id="'+escapeHTML(s.id)+'" onclick="editDailySource(this.dataset.id)">编辑</button><button data-id="'+escapeHTML(s.id)+'" onclick="deleteDailySource(this.dataset.id)">删除</button>';
  return '<div class="dailyItem"><div class="dailyItemHead"><div><b>'+escapeHTML(s.name||s.id)+'</b><div class="dailyItemMeta">'+escapeHTML(dailyCategoryLabel(s.category))+' · '+escapeHTML(s.id)+' · '+dailyEncodingLabel(s.encoding)+'</div></div>'+badge+'</div><div class="dailyItemMeta">'+escapeHTML(s.desc||s.url||'')+'</div><div class="dailyActions">'+actions+'</div></div>';
 }).join('')||'<div class="muted">暂无接口</div>';
}
function selectDailySource(id){dailyNewsCfg.selected_source=id;renderDailySelectedSource();renderDailySources()}
function toggleDailySourceEnabled(id){
 const s=(dailyNewsCfg.sources||[]).find(x=>x.id===id); if(!s) return;
 s.disabled=!s.disabled; s.enabled=!s.disabled;
 renderDailySelectedSource(); renderDailySources(); $('dailyNewsMsg').textContent=(s.disabled?'已关闭 ':'已开启 ')+(s.name||s.id)+'，记得保存';
}
function renderDailySelectedSource(){
 const s=dailySelectedSource();
 if(!s){$('dailySelectedMeta').textContent='暂无技能';return}
 $('dailySelectedMeta').textContent=(s.disabled?'已关闭 · ':'已开启 · ')+(s.name||s.id)+' · '+dailyCategoryLabel(s.category)+' · '+(s.id||'');
 $('dailySourceCommands').value=(s.commands||[]).join('\n');
 $('dailySourceParams').innerHTML=(s.params||[]).map(p=>'<span>'+escapeHTML((p.label||p.name)+(p.required?'*':'')+' · '+(p.source||'arg'))+'</span>').join('')||'<span>无参数</span>';
}
function saveDailySourceCommands(){
 const s=dailySelectedSource(); if(!s) return;
 s.commands=String($('dailySourceCommands').value||'').split(/\n+/).map(x=>x.trim()).filter(Boolean);
 renderDailySources(); $('dailyNewsMsg').textContent='命令已更新，记得保存';
}
function toggleDailySelectedSource(){const s=dailySelectedSource(); if(s) toggleDailySourceEnabled(s.id)}
function clearDailySourceForm(){['dailySourceID','dailySourceName','dailySourceCategory','dailySourceURL'].forEach(id=>$(id).value=''); $('dailySourceEncoding').value='json'; $('dailySourceTimeout').value=20}
function editDailySource(id){
 const s=(dailyNewsCfg.sources||[]).find(x=>x.id===id); if(!s||s.builtin) return;
 $('dailySourceID').value=s.id||''; $('dailySourceName').value=s.name||''; $('dailySourceCategory').value=s.category||'custom'; $('dailySourceURL').value=s.url||''; $('dailySourceEncoding').value=s.encoding||'json'; $('dailySourceTimeout').value=s.timeout_seconds||20;
}
function addDailySource(){
 collectDailyNews();
 const src={id:String($('dailySourceID').value||'').trim(),name:String($('dailySourceName').value||'').trim(),category:String($('dailySourceCategory').value||'custom').trim(),url:String($('dailySourceURL').value||'').trim(),method:'GET',encoding:$('dailySourceEncoding').value||'json',timeout_seconds:Number($('dailySourceTimeout').value||20),commands:[],enabled:true,disabled:false};
 if(!src.id||!src.url){$('dailyNewsMsg').textContent='接口 ID 和 URL 必填';return}
 const old=(dailyNewsCfg.sources||[]).find(x=>x.id===src.id);
 if(old&&old.builtin){$('dailyNewsMsg').textContent='内置接口不能覆盖';return}
 dailyNewsCfg.sources=(dailyNewsCfg.sources||[]).filter(x=>x.id!==src.id);
 dailyNewsCfg.sources.push(src);
 clearDailySourceForm(); renderDailyNewsSettings(); $('dailyNewsMsg').textContent='接口已加入，记得保存';
}
function deleteDailySource(id){
 const s=(dailyNewsCfg.sources||[]).find(x=>x.id===id); if(!s||s.builtin) return;
 dailyNewsCfg.sources=dailyNewsCfg.sources.filter(x=>x.id!==id);
 dailyNewsCfg.schedules=(dailyNewsCfg.schedules||[]).filter(x=>x.source_id!==id);
 if(dailyNewsCfg.default_source===id) dailyNewsCfg.default_source='60s';
 renderDailyNewsSettings(); $('dailyNewsMsg').textContent='接口已删除，记得保存';
}
function setDailyDefaultSource(id){dailyNewsCfg.default_source=id; renderDailyNewsSettings(); $('dailyNewsMsg').textContent='默认接口已更新，记得保存'}
function renderDailyGroupPickers(){
 if(!dailyNewsCfg||!dailyNewsCfg.access) return;
 const mode=dailyNewsCfg.access.group_mode||'none';
 if($('dailyWhiteBox')) $('dailyWhiteBox').classList.toggle('hidden', mode!=='whitelist');
 if($('dailyBlackBox')) $('dailyBlackBox').classList.toggle('hidden', mode!=='blacklist');
 const white=new Set((dailyNewsCfg.access.group_whitelist||[]).map(String));
 const black=new Set((dailyNewsCfg.access.group_blacklist||[]).map(String));
 renderDailyGroupPicker('dailyGroupWhitePicker','dailyGroupWhiteSearch',white,'white');
 renderDailyGroupPicker('dailyGroupBlackPicker','dailyGroupBlackSearch',black,'black');
}
function renderDailyPrivatePickers(){
 if(!dailyNewsCfg||!dailyNewsCfg.access) return;
 const mode=dailyNewsCfg.access.private_mode||'none';
 if($('dailyPrivateWhiteBox')) $('dailyPrivateWhiteBox').classList.toggle('hidden', mode!=='whitelist');
 if($('dailyPrivateBlackBox')) $('dailyPrivateBlackBox').classList.toggle('hidden', mode!=='blacklist');
}
function dailyParseIDList(text){
 const seen={};
 String(text||'').split(/[\s,，;；]+/).map(x=>x.trim()).filter(Boolean).forEach(x=>{if(/^\d+$/.test(x)) seen[x]=true});
 return Object.keys(seen).map(Number).filter(Boolean).sort((a,b)=>a-b);
}
function dailyIDListText(list){return (list||[]).map(String).join('\n')}
function markDailyPrivateChanged(){
 if(!dailyNewsCfg) return;
 $('dailyNewsMsg').textContent='个人名单已修改，记得保存';
}
function renderDailyGroupPicker(target,searchID,set,kind){
 const q=String(($(searchID)&&$(searchID).value)||'').trim();
 const list=(groups||[]).filter(g=>!q||String(g.id).includes(q)||String(g.name||'').includes(q));
 $(target).innerHTML=list.map(g=>'<label class="groupItem"><input type="checkbox" data-kind="'+kind+'" data-id="'+escapeHTML(g.id)+'" '+checked(set.has(String(g.id)))+' onchange="toggleDailyGroup(this.dataset.kind,this.dataset.id,this.checked)"><span><b>'+escapeHTML(g.name||g.id)+'</b><small>'+escapeHTML(g.id)+'</small></span></label>').join('')||'<div class="muted">暂无群列表</div>';
}
function toggleDailyGroup(kind,id,on){
 dailyNewsCfg.access=dailyNewsCfg.access||{};
 const key=kind==='white'?'group_whitelist':'group_blacklist';
 const set=new Set((dailyNewsCfg.access[key]||[]).map(String));
 if(on) set.add(String(id)); else set.delete(String(id));
 dailyNewsCfg.access[key]=Array.from(set).map(Number).filter(Boolean);
 renderDailyGroupPickers(); $('dailyNewsMsg').textContent='群名单已修改，记得保存';
}
function dailySourceByID(id){return (dailyNewsCfg.sources||[]).find(x=>x.id===id)||null}
function dailyFormatFromEncoding(enc){
 enc=String(enc||'').toLowerCase();
 if(enc==='image'||enc==='image-proxy') return 'image';
 if(enc==='markdown') return 'markdown';
 if(enc==='json') return 'json';
 if(enc==='text') return 'text';
 return 'text';
}
function dailyFormatLabel(format){
 return ({image:'图片',text:'文本',markdown:'Markdown',json:'JSON'})[format]||format||'文本';
}
function updateDailyTaskFormat(){
 const src=dailySourceByID($('dailyTaskSource').value);
 const format=dailyFormatFromEncoding(src&&src.encoding);
 $('dailyTaskFormat').value=format;
 $('dailyTaskFormatView').textContent=dailyFormatLabel(format)+' · '+(src&&src.encoding?src.encoding:'text');
}
function parseDailyTaskTarget(target){
 const m=String(target||'').match(/^([^:：]+)[:：](\d+)$/);
 if(!m) return {type:'group',id:''};
 const kind=m[1].trim();
 return {type:(kind==='私聊'||kind==='private')?'private':'group',id:m[2]};
}
function buildDailyTaskTarget(){
 const type=$('dailyTaskTargetType').value||'group';
 const id=type==='private'?String($('dailyTaskPrivate').value||'').trim():String($('dailyTaskGroup').value||'').trim();
 if(!id) return '';
 return (type==='private'?'私聊':'群')+':'+id;
}
function renderDailyTaskGroupOptions(selected){
 const list=(groups||[]).slice().sort((a,b)=>String(a.name||a.id).localeCompare(String(b.name||b.id)));
 const current=String(selected||$('dailyTaskGroup').value||'');
 $('dailyTaskGroup').innerHTML=list.map(g=>'<option value="'+escapeHTML(g.id)+'"'+(String(g.id)===current?' selected':'')+'>'+escapeHTML((g.name||'群')+' / '+g.id)+'</option>').join('');
}
function updateDailyTaskTargetMode(){
 const type=$('dailyTaskTargetType').value||'group';
 $('dailyTaskGroup').classList.toggle('hidden',type!=='group');
 $('dailyTaskPrivate').classList.toggle('hidden',type!=='private');
}
function setDailyTaskTarget(target){
 const parsed=parseDailyTaskTarget(target);
 $('dailyTaskTargetType').value=parsed.type;
 if(parsed.type==='private'){
  $('dailyTaskPrivate').value=parsed.id;
 }else{
  renderDailyTaskGroupOptions(parsed.id);
 }
 updateDailyTaskTargetMode();
}
function dailyCronFromTime(v){
 const s=String(v||'').trim();
 const m=s.match(/^(\d{1,2}):(\d{2})$/);
 if(!m) return s;
 return String(Number(m[2]))+' '+String(Number(m[1]))+' * * *';
}
function dailyTaskCron(t){return String((t&&t.cron)||dailyCronFromTime(t&&t.time)||'30 8 * * *').trim()}
function dailyCronLooksOK(expr){
 const parts=String(expr||'').trim().split(/\s+/);
 return parts.length===5 && parts.every(x=>/^[\d*,/?-]+$/.test(x));
}
function deleteDailyTask(id){dailyNewsCfg.schedules=(dailyNewsCfg.schedules||[]).filter(x=>x.id!==id); renderDailyNewsSettings(); saveDailyNews('定时任务已删除并生效')}
function toggleDailyTask(id){const t=(dailyNewsCfg.schedules||[]).find(x=>x.id===id); if(t){t.enabled=!t.enabled; renderDailyNewsSettings(); saveDailyNews('定时任务开关已保存')}}
function renderDailyTasks(){
 const list=(dailyNewsCfg.schedules||[]).slice().sort((a,b)=>dailyTaskCron(a).localeCompare(dailyTaskCron(b))||String(a.id).localeCompare(String(b.id)));
 $('dailyTasks').innerHTML=list.map(t=>{
  const state=t.enabled?'<span class="dailyBadge">启用</span>':'<span class="dailyBadge readonly">关闭</span>';
  const src=dailySourceByID(t.source_id);
  const format=dailyFormatFromEncoding(src&&src.encoding);
  return '<div class="dailyItem"><div class="dailyItemHead"><div><b>'+escapeHTML(t.id||'-')+'</b><div class="dailyItemMeta">'+escapeHTML(dailyTaskCron(t))+' · '+escapeHTML(t.source_id||'-')+' · '+escapeHTML(dailyFormatLabel(format))+'</div></div>'+state+'</div><div class="dailyItemMeta">'+escapeHTML(t.target||'')+(t.last_run?' · 上次 '+escapeHTML(t.last_run):'')+'</div><div class="dailyActions"><button data-id="'+escapeHTML(t.id)+'" onclick="editDailyTask(this.dataset.id)">编辑</button><button data-id="'+escapeHTML(t.id)+'" onclick="toggleDailyTask(this.dataset.id)">开关</button><button data-id="'+escapeHTML(t.id)+'" onclick="deleteDailyTask(this.dataset.id)">删除</button></div></div>';
 }).join('')||'<div class="muted">暂无定时任务</div>';
}
function clearDailyTaskForm(){$('dailyTaskID').value=''; $('dailyTaskPrivate').value=''; $('dailyTaskCron').value='30 8 * * *'; $('dailyTaskEnabled').checked=true; if(dailyNewsCfg) $('dailyTaskSource').value=dailyNewsCfg.default_source||'60s'; $('dailyTaskTargetType').value='group'; renderDailyTaskGroupOptions(); updateDailyTaskTargetMode(); updateDailyTaskFormat()}
function editDailyTask(id){
 const t=(dailyNewsCfg.schedules||[]).find(x=>x.id===id); if(!t) return;
 $('dailyTaskID').value=t.id||''; $('dailyTaskSource').value=t.source_id||dailyNewsCfg.default_source||'60s'; setDailyTaskTarget(t.target||''); $('dailyTaskCron').value=dailyTaskCron(t); $('dailyTaskEnabled').checked=!!t.enabled; updateDailyTaskFormat();
}
function addDailyTask(){
 collectDailyNews();
 updateDailyTaskFormat();
 const cron=String($('dailyTaskCron').value||'').trim();
 const task={id:String($('dailyTaskID').value||'').trim(),source_id:$('dailyTaskSource').value||dailyNewsCfg.default_source||'60s',target:buildDailyTaskTarget(),time:'',cron:cron,format:$('dailyTaskFormat').value||'image',enabled:!!$('dailyTaskEnabled').checked};
 if(!task.id||!task.target||!task.cron){$('dailyNewsMsg').textContent='任务 ID、目标和 Cron 必填';return false}
 if(!dailyCronLooksOK(task.cron)){$('dailyNewsMsg').textContent='Cron 需要 5 段，例如 30 8 * * *';return false}
 dailyNewsCfg.schedules=(dailyNewsCfg.schedules||[]).filter(x=>x.id!==task.id);
 dailyNewsCfg.schedules.push(task);
 clearDailyTaskForm(); renderDailyNewsSettings(); $('dailyNewsMsg').textContent='定时任务已加入，记得保存';
 return true;
}
async function saveDailyTask(){
 if(addDailyTask()) await saveDailyNews('定时任务已保存并生效', true);
}
async function saveDailyNews(doneText, reloadAfter){
 collectDailyNews();
 $('dailyNewsMsg').textContent='保存中...';
 const r=await apiFetch('/api/dailynews/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(dailyNewsCfg)});
 if(!r.ok){$('dailyNewsMsg').textContent='保存失败：'+await r.text(); return}
 const data=await r.json(); dailyNewsCfg=data.config||dailyNewsCfg; renderDailyNewsSettings(); $('dailyNewsMsg').textContent=doneText||'已保存，已立即生效';
 if(reloadAfter) await loadDailyNewsConfig(false, doneText||'已保存，已立即生效');
}
async function loadDailyNewsConfig(showMsg, doneText){
 if(showMsg) $('dailyNewsMsg').textContent='刷新中...';
 const dailyData=await (await apiFetch('/api/dailynews/config')).json();
 dailyNewsCfg=(dailyData&&dailyData.config)||dailyNewsCfg||{};
 renderDailyNewsSettings();
 showPage('dailynews');
 $('dailyNewsMsg').textContent=doneText||'已刷新';
}
async function testDailyNews(){
 $('dailyNewsMsg').textContent='测试：聊天里发监听命令；定时任务按 Cron 自动发送。';
}
function deerAccess(){
 deerPipeCfg=deerPipeCfg||{};
 deerPipeCfg.access=deerPipeCfg.access||{private_mode:'none',group_mode:'none',group_user_mode:'none'};
 return deerPipeCfg.access;
}
function markDeerChanged(){if($('deerPipeMsg'))$('deerPipeMsg').textContent='有未保存修改，记得保存'}
function setDeerEnabled(v){deerPipeCfg.enabled=v;renderDeerPipeSettings();markDeerChanged()}
function setDeerPrivateEnabled(v){deerPipeCfg.private_enabled=v;markDeerChanged()}
function setDeerHelp(v){deerPipeCfg.help_enabled=v;markDeerChanged()}
function setDeerMakeup(v){deerPipeCfg.makeup_enabled=v;markDeerChanged()}
function setDeerRank(v){deerPipeCfg.rank_enabled=v;markDeerChanged()}
function onDeerAccessModeChange(){
 const a=deerAccess();
 a.private_mode=$('deerPrivateMode').value||'none';
 a.group_mode=$('deerGroupMode').value||'none';
 a.group_user_mode=$('deerGroupUserMode').value||'none';
 updateDeerAccessVisibility();renderDeerGroupPickers();markDeerChanged();
}
function updateDeerAccessVisibility(){
 document.querySelectorAll('.deerAccessField').forEach(el=>{
  const modeEl=$(el.dataset.mode); const mode=modeEl?modeEl.value:'none';
  el.classList.toggle('hidden', mode!==el.dataset.kind);
 });
}
function renderDeerGroupPickers(){
 if(!deerPipeCfg) return;
 const a=deerAccess();
 const white=new Set((a.group_whitelist||[]).map(String));
 const black=new Set((a.group_blacklist||[]).map(String));
 renderDeerGroupPicker('deerGroupWhitePicker','deerGroupWhiteSearch',white,'white');
 renderDeerGroupPicker('deerGroupBlackPicker','deerGroupBlackSearch',black,'black');
}
function renderDeerGroupPicker(target,searchID,set,kind){
 if(!$(target)) return;
 const q=String(($(searchID)&&$(searchID).value)||'').trim();
 const list=(groups||[]).filter(g=>!q||String(g.id).includes(q)||String(g.name||'').includes(q));
 $(target).innerHTML=list.map(g=>'<label class="groupItem"><input type="checkbox" data-kind="'+kind+'" data-id="'+escapeHTML(g.id)+'" '+checked(set.has(String(g.id)))+' onchange="toggleDeerGroup(this.dataset.kind,this.dataset.id,this.checked)"><span><b>'+escapeHTML(g.name||g.id)+'</b><small>'+escapeHTML(g.id)+'</small></span></label>').join('')||'<div class="muted">暂无群列表，点击“刷新群列表”</div>';
}
function toggleDeerGroup(kind,id,on){
 const a=deerAccess();
 const key=kind==='white'?'group_whitelist':'group_blacklist';
 const set=new Set((a[key]||[]).map(String));
 if(on) set.add(String(id)); else set.delete(String(id));
 a[key]=Array.from(set).map(Number).filter(Boolean);
 renderDeerGroupPickers(); markDeerChanged();
}
function renderDeerStats(){
 if(!$('deerStats')) return;
 const s=deerPipeStats||{};
 const badge=(v,label)=>'<div class="proxyBadge"><b>'+escapeHTML(String(v==null?'-':v))+'</b><span>'+escapeHTML(label)+'</span></div>';
 $('deerStats').innerHTML=badge(s.active_users||0,'本月🦌过的人')+badge(s.month_checks||0,'本月🦌次数')+badge(s.today_checks||0,'今日🦌次数')+badge(s.banned_users||0,'被禁🦌')+badge(s.help_disabled||0,'谢绝帮🦌')+badge((deerPipeCfg&&deerPipeCfg.ban_max_days)||30,'禁🦌上限(天)');
}
function renderDeerPipeSettings(){
 if(!deerPipeCfg||!$('deerEnabledSwitch')) return;
 const a=deerAccess();
 $('deerEnabledSwitch').innerHTML=actionSwitchHTML('setDeerEnabled',!!deerPipeCfg.enabled);
 $('deerPrivateSwitch').innerHTML=actionSwitchHTML('setDeerPrivateEnabled',!!deerPipeCfg.private_enabled);
 $('deerHelpSwitch').innerHTML=actionSwitchHTML('setDeerHelp',!!deerPipeCfg.help_enabled);
 $('deerMakeupSwitch').innerHTML=actionSwitchHTML('setDeerMakeup',!!deerPipeCfg.makeup_enabled);
 $('deerRankSwitch').innerHTML=actionSwitchHTML('setDeerRank',!!deerPipeCfg.rank_enabled);
 $('deerBanMaxDays').value=deerPipeCfg.ban_max_days||30;
 $('deerPrivateMode').value=a.private_mode||'none';
 $('deerGroupMode').value=a.group_mode||'none';
 $('deerGroupUserMode').value=a.group_user_mode||'none';
 $('deerPrivateWhitelist').value=idListText(a.private_whitelist);
 $('deerPrivateBlacklist').value=idListText(a.private_blacklist);
 $('deerGroupUserWhitelist').value=idListText(a.group_user_whitelist);
 $('deerGroupUserBlacklist').value=idListText(a.group_user_blacklist);
 if($('deerPipePluginStatus')){
  $('deerPipePluginStatus').textContent=deerPipeCfg.enabled?'已启用':'已关闭';
  $('deerPipePluginStatus').className=deerPipeCfg.enabled?'ok':'muted';
 }
 updateDeerAccessVisibility();
 renderDeerGroupPickers();
 renderDeerStats();
}
function collectDeerPipe(){
 if(!deerPipeCfg) return;
 const a=deerAccess();
 a.private_mode=$('deerPrivateMode').value||'none';
 a.group_mode=$('deerGroupMode').value||'none';
 a.group_user_mode=$('deerGroupUserMode').value||'none';
 a.private_whitelist=parseIDArray($('deerPrivateWhitelist').value);
 a.private_blacklist=parseIDArray($('deerPrivateBlacklist').value);
 a.group_user_whitelist=parseIDArray($('deerGroupUserWhitelist').value);
 a.group_user_blacklist=parseIDArray($('deerGroupUserBlacklist').value);
 deerPipeCfg.ban_max_days=Math.max(1,Math.min(365,Number($('deerBanMaxDays').value)||30));
}
async function saveDeerPipe(doneText){
 if(!deerPipeCfg) return;
 collectDeerPipe();
 $('deerPipeMsg').textContent='保存中...';
 const r=await apiFetch('/api/deerpipe/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(deerPipeCfg)});
 if(!r.ok){$('deerPipeMsg').textContent='保存失败：'+await r.text(); return}
 const data=await r.json();
 deerPipeCfg=data.config||deerPipeCfg;
 deerPipeStats=data.stats||deerPipeStats;
 renderDeerPipeSettings();
 $('deerPipeMsg').textContent=doneText||'已保存，已立即生效';
}
async function loadDeerPipeConfig(showMsg){
 if(showMsg&&$('deerPipeMsg')) $('deerPipeMsg').textContent='刷新中...';
 try{
  const data=await (await apiFetch('/api/deerpipe/config')).json();
  deerPipeCfg=(data&&data.config)||deerPipeCfg||{};
  deerPipeStats=(data&&data.stats)||deerPipeStats;
 }catch(e){if($('deerPipeMsg'))$('deerPipeMsg').textContent='读取失败';return}
 renderDeerPipeSettings();
 if(showMsg){showPage('deerpipe');$('deerPipeMsg').textContent='已刷新'}
}
function collectCookieCloudSettings(){
 if(!$('cookieCloudServer')) return;
 cfg.cookiecloud_server=String($('cookieCloudServer').value||'').trim();
 cfg.cookiecloud_uuid=String($('cookieCloudUUID').value||'').trim();
 cfg.cookiecloud_password=String($('cookieCloudPassword').value||'').trim();
 cfg.cookiecloud_interval_minutes=Number($('cookieCloudInterval').value||60);
 cfg.cookiecloud_platforms=cfg.cookiecloud_platforms||{};
}
async function syncCookieCloudNow(){
 collectCookieCloudSettings();
 $('cookieCloudSyncMsg').textContent='同步中...';
 const saved=await apiFetch('/api/mediaparser/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(cfg)});
 if(!saved.ok){$('cookieCloudSyncMsg').textContent='保存配置失败'; return}
 const r=await apiFetch('/api/mediaparser/cookiecloud/sync',{method:'POST'});
 if(!r.ok){$('cookieCloudSyncMsg').textContent='同步失败：'+await r.text(); return}
 const data=await r.json();
 cfg=data.config||cfg;
 const res=(data.result||{});
 const updated=(res.updated||[]), skipped=(res.skipped||[]), warnings=(res.warnings||[]);
 let msg=updated.length?'已更新：'+updated.join('、'):'未匹配 Cookie';
 if(skipped.length) msg+='；跳过 '+skipped.length+' 项';
 if(warnings.length) msg+='；警告 '+warnings.length+' 项';
 $('cookieCloudSyncMsg').textContent=msg;
 render();
}
function logoCell(p){const info=logos[p.name]||{};const custom=!!info.exists;const src=info.url||('/api/mediaparser/logos/image?platform='+encodeURIComponent(p.name));const preview='<img class="logoPreview" src="'+escapeHTML(src)+'" alt="'+escapeHTML(p.label)+' Logo">';return '<div class="logoWrap">'+preview+'<div><div class="logoTools"><input id="logo-'+p.name+'" data-platform="'+p.name+'" type="file" accept="image/*" style="display:none" onchange="uploadLogo(this.dataset.platform)"><button data-target="logo-'+p.name+'" onclick="$(this.dataset.target).click()">'+(custom?'替换':'上传')+'</button><input id="logoUrl-'+p.name+'" type="text" placeholder="粘贴图片链接自动缓存"><button data-platform="'+p.name+'" onclick="cacheLogoURL(this.dataset.platform)">缓存链接</button></div><div class="muted">'+(custom?'已缓存本地 Logo':'使用内置 Logo，可上传覆盖')+'</div></div></div>'}
function listText(map){return Object.keys(map||{}).filter(k=>map[k]).sort((a,b)=>Number(a)-Number(b)).join('\n')}
function parseList(text){const out={}; String(text||'').split(/[\s,，;；]+/).map(x=>x.trim()).filter(Boolean).forEach(x=>{if(/^-?\d+$/.test(x)) out[x]=true}); return out}
function idListText(list){return (list||[]).map(String).join('\n')}
function parseIDArray(text){const seen={}; String(text||'').split(/[\s,，;；]+/).map(x=>x.trim()).filter(Boolean).forEach(x=>{if(/^-?\d+$/.test(x)&&x!=='0') seen[x]=true}); return Object.keys(seen).map(Number).filter(n=>n!==0).sort((a,b)=>a-b)}
function setTextareaMap(id,map){$(id).value=listText(map)}
function addListID(textarea,id,on){const map=parseList($(textarea).value); if(on) map[String(id)]=true; else delete map[String(id)]; setTextareaMap(textarea,map); markDirty()}
function toggleLastMsg(){const el=$('lastMsg'); el.classList.toggle('expanded')}
function onAccessModeChange(){updateAccessVisibility();renderGroupPickers();markDirty()}
function updateAccessVisibility(){
 document.querySelectorAll('.accessField').forEach(el=>{
  const modeEl=$(el.dataset.mode); const mode=modeEl?modeEl.value:'none';
  el.classList.toggle('hidden', mode!==el.dataset.kind);
 });
 const hasGroupMode=$('gmode').value!=='none';
 $('groupMsg').textContent=hasGroupMode?'群列表用于快速勾选当前群名单':'群聊名单已关闭，需要时选择白名单或黑名单';
}
function onTGBotAccessModeChange(){updateTGBotAccessVisibility()}
function updateTGBotAccessVisibility(){
 document.querySelectorAll('.tgbotAccessField').forEach(el=>{
  const modeEl=$(el.dataset.mode); const mode=modeEl?modeEl.value:'none';
  el.classList.toggle('hidden', mode!==el.dataset.kind);
 });
}
async function refreshStatus(){
 const st=await (await apiFetch('/api/status')).json();
 $('svc').innerHTML='<span class="ok">运行中</span>'; $('topState').textContent=dirty?'有未保存修改':'WebUI 已连接';
 const rt=st.runtime_status||{}; const bot=st.bot||{};
 $('self').innerHTML=botAccountsHTML(bot, rt); $('okn').textContent=rt.parse_success||0; $('failn').textContent=rt.parse_failed||0;
 $('okn2').textContent=rt.parse_success||0; $('failn2').textContent=rt.parse_failed||0;
 $('lastMsg').textContent=rt.last_message||'暂无消息';
 const enabled=platforms.filter(p=>cfg&&cfg.platform_enabled&&cfg.platform_enabled[p.name]).length;
 $('runtimeSummary').innerHTML=[
  infoLine('Go 版本', st.go||'-'),
  infoLine('WebUI 地址', (sys&&sys.webui_addr)||'-'),
  infoLine('OneBot WS', (sys&&sys.ws_url)||'-'),
  infoLine('官方 QQBot', qqbotStatusText()),
  infoLine('Telegram Bot', tgbotStatusText()),
  infoLine('命令前缀', (sys&&sys.command_prefix)||'/')
 ].join('');
 $('overviewDetails').innerHTML=[
  infoLine('聚合解析', (cfg&&cfg.auto_parse?'已开启':'已关闭')+'，启用平台 '+enabled+'/'+platforms.length),
  infoLine('OneBot 策略', '解析卡片 '+onText(cfg&&cfg.auto_parse)+' / 媒体下载 '+onText(cfg&&cfg.download_video)),
  infoLine('QQBot 策略', qqbotPolicyText()),
  infoLine('Telegram 策略', tgbotPolicyText()),
  infoLine('视频限制', '画质 '+qualityText(cfg&&cfg.video_max_resolution)+'，最大 '+((cfg&&cfg.max_video_mb)||'-')+' MB，避开 AV1 '+onText(cfg&&cfg.avoid_av1)),
		infoLine('缓存', cacheInfo?((cacheInfo.files||0)+' 个文件 / '+formatBytes(cacheInfo.bytes||0)):'-')
	].join('');
 loadLogs();
}
function infoLine(k,v){return '<div class="infoLine"><b>'+escapeHTML(k)+'</b><span class="muted">'+escapeHTML(v)+'</span></div>'}
function botAccountsHTML(bot,rt){
 const accounts=(bot&&bot.accounts)||[];
 if(accounts.length){
  return accounts.map(a=>'<span style="display:block;font-size:15px;line-height:1.35"><small class="muted">'+escapeHTML(a.label||a.kind||'Bot')+'</small><br>'+escapeHTML(a.id||'-')+'</span>').join('');
 }
 const fallback=(bot&&bot.self_id)||(rt&&rt.last_self_id)||'-';
 return '<span style="display:block;font-size:15px">'+escapeHTML(fallback)+'</span>';
}
function onText(v){return v?'开启':'关闭'}
function qualityText(v){return Number(v)>0 ? String(v)+'p' : '不限'}
function qqbotAvailable(){return !!(sys&&sys.qqbot_available)}
function qqbotUnavailableText(){return '官方包无此功能'}
function qqbotStatusText(){
 if(!qqbotAvailable()) return qqbotUnavailableText();
 return (sys&&sys.qqbot_enabled?'已启用':'未启用')+' / 图片 '+((sys&&sys.qqbot_public_base)?'已配置':'未配置');
}
function qqbotPolicyText(){
 if(!qqbotAvailable()) return qqbotUnavailableText();
 return (sys&&sys.qqbot_enabled?(sys.qqbot_card_enabled?'卡片开启':'卡片关闭'):'未启用')+' / 媒体图片/视频 '+onText(sys&&sys.qqbot_media_enabled);
}
function tgbotAvailable(){return !!(sys&&sys.tgbot_available)}
function tgbotUnavailableText(){return 'Telegram 驱动未加载'}
function tgbotStatusText(){
 if(!tgbotAvailable()) return tgbotUnavailableText();
 return (sys&&sys.tgbot_enabled?'已启用':'未启用')+' / Token '+((sys&&sys.tgbot_token_set)?'已设置':'未设置');
}
function tgbotPolicyText(){
 if(!tgbotAvailable()) return tgbotUnavailableText();
 return (sys&&sys.tgbot_enabled?'长轮询开启':'未启用')+' / 媒体上传 '+onText(sys&&sys.tgbot_media_enabled)+' / 超管 '+tgbotSuperUserCount()+' / '+tgbotAccessSummary();
}
function accessModeText(mode){return mode==='whitelist'?'白名单':(mode==='blacklist'?'黑名单':'未限制')}
function tgbotSuperUserCount(){return ((sys&&sys.tgbot_super_users)||[]).length}
function tgbotAccessSummary(){
 if(!sys) return '未限制';
 return '私聊 '+accessModeText(sys.tgbot_private_mode)+'，群 '+accessModeText(sys.tgbot_group_mode)+'，发言人 '+accessModeText(sys.tgbot_group_user_mode);
}
function renderQQBotAvailability(){
 const available=qqbotAvailable();
 const status=$('qqbotPluginStatus');
 if(status){
  status.textContent=available?(sys&&sys.qqbot_enabled?'已启用':'可配置'):qqbotUnavailableText();
  status.className=available?'ok':'muted';
 }
 if($('qqbotPluginDesc')) $('qqbotPluginDesc').textContent=available?'QQ 官方机器人通道，第一阶段接入媒体解析':'当前官方包未加载 QQBot 驱动，此功能不可用';
 if($('qqbotUnavailable')) $('qqbotUnavailable').classList.toggle('hidden', available);
 if($('qqbotSaveHint')) $('qqbotSaveHint').textContent=available?'公网根地址用于把本地卡片 PNG 映射成 QQ 可访问的 Markdown 图片 URL。':qqbotUnavailableText();
 if($('qqbotSaveButton')) $('qqbotSaveButton').disabled=!available;
 ['qqbotEnabled','qqbotName','qqbotAppID','qqbotSecret','qqbotOpenID','qqbotGroupOpenID','qqbotPublicBase','qqbotCardEnabled','qqbotMediaEnabled','qqbotMarkdown'].forEach(id=>{
  const el=$(id);
  if(el) el.disabled=!available;
 });
}
function renderTGBotAvailability(){
 const available=tgbotAvailable();
 const status=$('tgbotPluginStatus');
 if(status){
  status.textContent=available?(sys&&sys.tgbot_enabled?'已启用':'可配置'):tgbotUnavailableText();
  status.className=available?'ok':'muted';
 }
 if($('tgbotPluginDesc')) $('tgbotPluginDesc').textContent=available?'Telegram 长轮询通道，可接入全部插件消息':'当前包未加载 Telegram 驱动，此功能不可用';
 if($('tgbotUnavailable')) $('tgbotUnavailable').classList.toggle('hidden', available);
 if($('tgbotSaveHint')) $('tgbotSaveHint').textContent=available?'保存后需重启；群聊自动解析可能需要关闭 BotFather privacy mode。':tgbotUnavailableText();
 if($('tgbotSaveButton')) $('tgbotSaveButton').disabled=!available;
 ['tgbotEnabled','tgbotName','tgbotToken','tgbotAPIBase','tgbotProxy','tgbotMediaEnabled','tgbotSuperUsers','tgbotPrivateMode','tgbotGroupMode','tgbotGroupUserMode','tgbotUserWhitelist','tgbotUserBlacklist','tgbotGroupWhitelist','tgbotGroupBlacklist','tgbotGroupUserWhitelist','tgbotGroupUserBlacklist'].forEach(id=>{
  const el=$(id);
  if(el) el.disabled=!available;
 });
}
async function loadLogs(){
 try{
  const data=await (await apiFetch('/api/logs?limit=80')).json();
  const logs=data.logs||[];
  $('logSummary').innerHTML=logs.map(l=>'<div class="logLine"><b>'+escapeHTML(l.time+' '+String(l.level||'').toUpperCase())+'</b>'+escapeHTML(l.message||'')+'</div>').join('')||'<div class="muted">暂无程序日志</div>';
 }catch(e){$('logSummary').innerHTML='<div class="bad">日志读取失败：'+escapeHTML(e)+'</div>'}
}
async function load(){
 const data=await (await apiFetch('/api/mediaparser/config')).json();
 const sysData=await (await apiFetch('/api/system/settings')).json();
 const dailyData=await (await apiFetch('/api/dailynews/config')).json();
 const deerData=await (await apiFetch('/api/deerpipe/config')).json().catch(()=>null);
 const logoData=await (await apiFetch('/api/mediaparser/logos')).json();
 const authData=await (await apiFetch('/api/system/auth')).json();
 await loadCacheStats(false);
 cfg=data.config; platforms=data.platforms; safetyBuiltins=data.safety_builtins||[];
 sys=sysData.settings||{};
 dailyNewsCfg=(dailyData&&dailyData.config)||{};
 deerPipeCfg=(deerData&&deerData.config)||{};
 deerPipeStats=(deerData&&deerData.stats)||null;
 logos=logoData.logos||{};
 if($('webAuthUser')) $('webAuthUser').value=(authData&&authData.user)||'admin';
 await refreshStatus();
 render();
}
function render(){
 const items=[['auto_parse','解析卡片'],['download_video','媒体下载'],['long_article_cards','长文卡片'],['parse_reaction','解析回应'],['debug','调试日志'],['avoid_av1','禁用 AV1'],['use_yt_dlp_fallback','yt-dlp 备用']];
 renderMediaShieldSettings();
 $('globalControls').innerHTML=items.map(x=>'<label class="row">'+x[1]+switchHTML('cfg.'+x[0],!!cfg[x[0]])+'</label>').join('');
 cfg.platform_video_resolution=cfg.platform_video_resolution||{};
 $('platformRows').innerHTML=platforms.map(p=>'<tr><td><b>'+p.label+'</b><div class="muted">'+(p.local||p.name)+'</div></td><td>'+bindParseCardSwitch(p.name)+'</td><td>'+bindMediaDownloadSwitch(p.name)+'</td><td>'+resolutionCell(p.name)+'</td><td>'+logoCell(p)+'</td></tr>').join('');
 if(!cfg.platform_group_block) cfg.platform_group_block={};
 $('pmode').value=cfg.private_access_mode||'none'; $('gmode').value=cfg.group_access_mode||'none'; $('gumode').value=cfg.group_user_access_mode||'none';
 $('userWhitelist').value=listText(cfg.user_whitelist); $('userBlacklist').value=listText(cfg.user_blacklist);
 $('groupWhitelist').value=listText(cfg.group_whitelist); $('groupBlacklist').value=listText(cfg.group_blacklist);
 $('groupUserWhitelist').value=listText(cfg.group_user_whitelist); $('groupUserBlacklist').value=listText(cfg.group_user_blacklist);
 $('res').value=String(cfg.video_max_resolution||0); $('maxmb').value=cfg.max_video_mb||1000; $('ttl').value=cfg.cache_ttl_minutes||60; $('reactionEmoji').value=cfg.parse_reaction_emoji||'🍉'; $('failReactionEmoji').value=cfg.fail_reaction_emoji||'❌';
 $('youtubeExtractorArgs').value=cfg.youtube_extractor_args||'youtube:player_client=default,android;formats=missing_pot';
 $('proxy').value=cfg.proxy||'';
 $('browserCDPURL').value=cfg.browser_cdp_url||'';
 if($('proxySummaryText')) $('proxySummaryText').textContent=cfg.proxy||'未配置';
 setSecretInput('bilibiliCookie', cfg.bilibili_cookie_set, 'SESSDATA=...; bili_jct=...');
 setSecretInput('xiaohongshuCookie', cfg.xiaohongshu_cookie_set, 'a1=...; web_session=...');
	setSecretInput('youtubeCookie', cfg.youtube_cookie_set, 'VISITOR_INFO1_LIVE=...; SID=...');
	setSecretInput('instagramCookie', cfg.instagram_cookie_set, 'sessionid=...; ds_user_id=...');
	setSecretInput('keylolCookie', cfg.keylol_cookie_set, 'key=value; key2=value2');
	setSecretInput('linuxdoCookie', cfg.linuxdo_cookie_set, '_t=...; cf_clearance=...');
	$('linuxdoUA').value=cfg.linuxdo_ua||'';
	$('linuxdoFlaresolverrURL').value=cfg.linuxdo_flaresolverr_url||'';
	$('linuxdoFlaresolverrWait').value=cfg.linuxdo_flaresolverr_wait_seconds||2;
	$('linuxdoFlaresolverrTimeout').value=cfg.linuxdo_flaresolverr_timeout_ms||60000;
	$('linuxdoFlaresolverrProxySwitch').innerHTML=switchHTML('cfg.linuxdo_flaresolverr_use_proxy',!!cfg.linuxdo_flaresolverr_use_proxy);
	renderCookieCloudSettings();
	$('keylolFooter').value=cfg.keylol_footer||'Keylol 帖子截图 · 浏览器渲染 · {time}';
 renderKeylolThemeSwitches();
 renderCacheStats();
 $('keylolASFForwardSwitch').innerHTML=switchHTML('cfg.keylol_asf_forward', cfg.keylol_asf_forward!==false);
 renderSystemSettings();
 renderDailyNewsSettings();
 renderDeerPipeSettings();
 updateAccessVisibility();
 renderPlatformGroupBlock();
 renderSafetySettings();
 if(!groups.length) loadGroups(false); else renderGroupPickers();
 showPage((location.hash||'#overview').slice(1)||'overview');
}
function setSecretInput(id,set,placeholder){
 const el=$(id); if(!el) return;
 if(document.activeElement!==el && !el.value) el.value='';
 el.placeholder=set?'Already set; leave blank to keep, enter a new value to replace':placeholder;
}
function renderSystemSettings(){
 if(!sys) return;
 $('sysWebui').value=sys.webui_addr||'';
 $('sysWS').value=sys.ws_url||'';
 $('sysToken').value='';
 $('sysToken').placeholder=sys.ws_token_set?'已设置，留空不修改':'留空表示不设置';
 $('onebotDataDir').value=sys.onebot_data_dir||'';
 $('sysNick').value=sys.nickname||'';
 $('sysPrefix').value=sys.command_prefix||'/';
 $('sysSuperUsers').value=(sys.super_users||[]).join('\n');
 if($('qqbotEnabled')){
  $('qqbotEnabled').checked=!!sys.qqbot_enabled;
  $('qqbotName').value=sys.qqbot_name||'qqbot';
  $('qqbotAppID').value=sys.qqbot_app_id||'';
  $('qqbotSecret').value='';
  $('qqbotSecret').placeholder=sys.qqbot_secret_set?'已设置，留空不修改':'留空表示不设置';
  $('qqbotOpenID').value=sys.qqbot_openid||'';
  $('qqbotGroupOpenID').value=sys.qqbot_group_openid||'';
  $('qqbotPublicBase').value=sys.qqbot_public_base||'';
  $('qqbotCardEnabled').checked=sys.qqbot_card_enabled!==false;
  $('qqbotMediaEnabled').checked=!!sys.qqbot_media_enabled;
  $('qqbotMarkdown').checked=!!sys.qqbot_markdown;
 }
 renderQQBotAvailability();
 if($('tgbotEnabled')){
  $('tgbotEnabled').checked=!!sys.tgbot_enabled;
  $('tgbotName').value=sys.tgbot_name||'telegram';
  $('tgbotToken').value='';
  $('tgbotToken').placeholder=sys.tgbot_token_set?'已设置，留空不修改':'留空表示不设置';
  $('tgbotAPIBase').value=sys.tgbot_api_base||'https://api.telegram.org';
  $('tgbotProxy').value=sys.tgbot_proxy||'';
  $('tgbotMediaEnabled').checked=!!sys.tgbot_media_enabled;
  $('tgbotSuperUsers').value=idListText(sys.tgbot_super_users);
  $('tgbotPrivateMode').value=sys.tgbot_private_mode||'none';
  $('tgbotGroupMode').value=sys.tgbot_group_mode||'none';
  $('tgbotGroupUserMode').value=sys.tgbot_group_user_mode||'none';
  $('tgbotUserWhitelist').value=idListText(sys.tgbot_user_whitelist);
  $('tgbotUserBlacklist').value=idListText(sys.tgbot_user_blacklist);
  $('tgbotGroupWhitelist').value=idListText(sys.tgbot_group_whitelist);
  $('tgbotGroupBlacklist').value=idListText(sys.tgbot_group_blacklist);
  $('tgbotGroupUserWhitelist').value=idListText(sys.tgbot_group_user_whitelist);
  $('tgbotGroupUserBlacklist').value=idListText(sys.tgbot_group_user_blacklist);
 }
 renderTGBotAvailability();
 updateTGBotAccessVisibility();
 const pending=sys.pending_restart||[];
 $('sysPending').textContent=pending.length?'重启后生效：'+pending.join('、'):'当前没有待重启生效的配置';
 if($('restartTop')){
  $('restartTop').classList.toggle('hidden', !pending.length);
  $('restartTop').title=pending.length?'重启后生效：'+pending.join('、'):'';
 }
}
async function saveSystemSettings(){
 const payload={
  webui_addr:String($('sysWebui').value||'').trim(),
  ws_url:String($('sysWS').value||'').trim(),
  ws_token:String($('sysToken').value||'').trim(),
  onebot_data_dir:String($('onebotDataDir').value||'').trim(),
  nickname:String($('sysNick').value||'').trim(),
  command_prefix:String($('sysPrefix').value||'/').trim()||'/',
  super_users:Object.keys(parseList($('sysSuperUsers').value)).map(x=>Number(x))
 };
 if(qqbotAvailable()){
  Object.assign(payload,{
   qqbot_enabled:$('qqbotEnabled')?!!$('qqbotEnabled').checked:false,
   qqbot_name:$('qqbotName')?String($('qqbotName').value||'qqbot').trim():'qqbot',
   qqbot_app_id:$('qqbotAppID')?String($('qqbotAppID').value||'').trim():'',
   qqbot_secret:$('qqbotSecret')?String($('qqbotSecret').value||'').trim():'',
   qqbot_openid:$('qqbotOpenID')?String($('qqbotOpenID').value||'').trim():'',
   qqbot_group_openid:$('qqbotGroupOpenID')?String($('qqbotGroupOpenID').value||'').trim():'',
   qqbot_public_base:$('qqbotPublicBase')?String($('qqbotPublicBase').value||'').trim():'',
   qqbot_card_disabled:$('qqbotCardEnabled')?!$('qqbotCardEnabled').checked:false,
   qqbot_media_enabled:$('qqbotMediaEnabled')?!!$('qqbotMediaEnabled').checked:false,
   qqbot_markdown:$('qqbotMarkdown')?!!$('qqbotMarkdown').checked:false
 });
 }
 if(tgbotAvailable()){
  Object.assign(payload,{
   tgbot_enabled:$('tgbotEnabled')?!!$('tgbotEnabled').checked:false,
   tgbot_name:$('tgbotName')?String($('tgbotName').value||'telegram').trim():'telegram',
   tgbot_token:$('tgbotToken')?String($('tgbotToken').value||'').trim():'',
   tgbot_api_base:$('tgbotAPIBase')?String($('tgbotAPIBase').value||'https://api.telegram.org').trim():'https://api.telegram.org',
   tgbot_proxy:$('tgbotProxy')?String($('tgbotProxy').value||'').trim():'',
   tgbot_media_enabled:$('tgbotMediaEnabled')?!!$('tgbotMediaEnabled').checked:false,
   tgbot_super_users:$('tgbotSuperUsers')?parseIDArray($('tgbotSuperUsers').value):[],
   tgbot_private_mode:$('tgbotPrivateMode')?String($('tgbotPrivateMode').value||'none'):'none',
   tgbot_group_mode:$('tgbotGroupMode')?String($('tgbotGroupMode').value||'none'):'none',
   tgbot_group_user_mode:$('tgbotGroupUserMode')?String($('tgbotGroupUserMode').value||'none'):'none',
   tgbot_user_whitelist:$('tgbotUserWhitelist')?parseIDArray($('tgbotUserWhitelist').value):[],
   tgbot_user_blacklist:$('tgbotUserBlacklist')?parseIDArray($('tgbotUserBlacklist').value):[],
   tgbot_group_whitelist:$('tgbotGroupWhitelist')?parseIDArray($('tgbotGroupWhitelist').value):[],
   tgbot_group_blacklist:$('tgbotGroupBlacklist')?parseIDArray($('tgbotGroupBlacklist').value):[],
   tgbot_group_user_whitelist:$('tgbotGroupUserWhitelist')?parseIDArray($('tgbotGroupUserWhitelist').value):[],
   tgbot_group_user_blacklist:$('tgbotGroupUserBlacklist')?parseIDArray($('tgbotGroupUserBlacklist').value):[]
  });
 }
 const r=await apiFetch('/api/system/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
 $('systemMsg').textContent=r.ok?'全局设置已保存':'全局设置保存失败';
 if(r.ok){const data=await r.json(); sys=data.settings||sys; renderSystemSettings(); await refreshStatus();}
}
async function saveWebAuth(){
 const user=String($('webAuthUser').value||'').trim();
 const pass=String($('webAuthPass').value||'').trim();
 const pass2=String($('webAuthPass2').value||'').trim();
 if(!user){$('webAuthMsg').textContent='用户名不能为空'; return}
 if(!pass){$('webAuthMsg').textContent='新密码不能为空'; return}
 if(pass!==pass2){$('webAuthMsg').textContent='两次密码不一致'; return}
 const r=await apiFetch('/api/system/auth',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({user:user,password:pass})});
 if(r.ok){
  $('webAuthPass').value=''; $('webAuthPass2').value='';
  $('webAuthMsg').textContent='登录账户已保存；刷新页面后使用新账户重新登录';
 }else{
  $('webAuthMsg').textContent='保存失败：'+await r.text();
 }
}
async function restartSystem(){
 if(!confirm('确定重启机器人进程吗？systemd 会自动拉起新进程。')) return;
 $('systemMsg').textContent='正在重启进程...';
 try{await apiFetch('/api/system/restart',{method:'POST'}); setTimeout(()=>{location.reload()},5000)}catch(e){$('systemMsg').textContent='重启请求发送失败：'+e}
}
async function loadGroups(force){
 try{
  $('groupMsg').textContent=force?'正在拉取群列表...':'正在读取群列表...';
  const data=await (await apiFetch('/api/onebot/groups')).json();
  groups=data.groups||[]; $('groupMsg').textContent='已拉取 '+groups.length+' 个群';
  renderGroupPickers();
 }catch(e){$('groupMsg').textContent='群列表拉取失败：'+e}
}
function renderGroupPickers(){
 updateAccessVisibility();
 renderGroupPicker('groupWhitePicker','groupWhiteSearch','groupWhitelist');
 renderGroupPicker('groupBlackPicker','groupBlackSearch','groupBlacklist');
 renderPlatformGroupBlock();
 renderMediaShieldGroups();
 renderDailyGroupPickers();
 renderDailyTaskGroupOptions();
 renderDeerGroupPickers();
}
function renderGroupPicker(container,searchID,textarea){
 const q=String($(searchID).value||'').toLowerCase().trim();
 const selected=parseList($(textarea).value);
 const list=groups.filter(g=>!q||String(g.id).includes(q)||String(g.name).toLowerCase().includes(q)).sort((a,b)=>(selected[b.id]?1:0)-(selected[a.id]?1:0)||String(a.name).localeCompare(String(b.name),'zh-CN'));
 $(container).innerHTML=list.map(g=>'<label class="groupItem"><input type="checkbox" '+checked(!!selected[g.id])+' data-list="'+textarea+'" data-id="'+g.id+'" onchange="toggleGroupPick(this)"><span>'+escapeHTML(g.name)+'<small>'+g.id+'</small></span></label>').join('')||'<div class="muted">没有匹配的群</div>';
}
function toggleGroupPick(el){addListID(el.dataset.list,el.dataset.id,el.checked);renderGroupPickers()}
function platformByName(name){return platforms.find(p=>p.name===name)||{name:name,label:name,local:name}}
function groupLabel(id){const g=groups.find(x=>String(x.id)===String(id)); return g ? g.name+' / '+g.id : '未知群 / '+id}
function platformBlockGroupIDs(){
 const ids={}; groups.forEach(g=>ids[String(g.id)]=g.name);
 Object.values(cfg.platform_group_block||{}).forEach(m=>Object.keys(m||{}).forEach(id=>{if(m[id]) ids[String(id)]=ids[String(id)]||''}));
 return Object.keys(ids).sort((a,b)=>{
  const ac=groupBlockedCount(a), bc=groupBlockedCount(b);
  if(ac!==bc) return bc-ac;
  return groupLabel(a).localeCompare(groupLabel(b),'zh-CN');
 });
}
function groupBlockedCount(groupID){let n=0; Object.values(cfg.platform_group_block||{}).forEach(m=>{if(m&&m[groupID]) n++}); return n}
function selectedBlockGroup(){return $('platformBlockGroupSelect').value || platformBlockGroupIDs()[0] || ''}
function setPlatformBlockedForGroup(platform, groupID, on){
 cfg.platform_group_block=cfg.platform_group_block||{}; cfg.platform_group_block[platform]=cfg.platform_group_block[platform]||{};
 if(on) cfg.platform_group_block[platform][String(groupID)]=true; else delete cfg.platform_group_block[platform][String(groupID)];
 markDirty();
}
function togglePlatformGroupBlock(el){setPlatformBlockedForGroup(el.dataset.platform, selectedBlockGroup(), el.checked); renderPlatformGroupBlock()}
function clearGroupPlatformBlock(){const gid=selectedBlockGroup(); if(!gid) return; platforms.forEach(p=>setPlatformBlockedForGroup(p.name,gid,false)); renderPlatformGroupBlock()}
function renderPlatformGroupBlock(){
 if(!cfg) return; cfg.platform_group_block=cfg.platform_group_block||{};
 const prev=$('platformBlockGroupSelect').value;
 const ids=platformBlockGroupIDs();
 $('platformBlockGroupSelect').innerHTML=ids.map(id=>'<option value="'+id+'">'+escapeHTML(groupLabel(id))+(groupBlockedCount(id)?'（已屏蔽 '+groupBlockedCount(id)+' 个平台）':'')+'</option>').join('');
 if(prev) $('platformBlockGroupSelect').value=prev;
 if(!$('platformBlockGroupSelect').value && ids[0]) $('platformBlockGroupSelect').value=ids[0];
 const gid=selectedBlockGroup();
 if(!gid){$('platformBlockPicker').innerHTML='<div class="muted">还没有拉取到群列表</div>'; return}
 $('platformBlockMsg').textContent='当前群：'+groupLabel(gid);
 const q=String($('platformBlockSearch').value||'').toLowerCase().trim();
 const list=platforms.filter(p=>!q||String(p.name).toLowerCase().includes(q)||String(p.label).toLowerCase().includes(q)||String(p.local||'').toLowerCase().includes(q));
 $('platformBlockPicker').innerHTML=list.map(p=>{const blocked=!!((cfg.platform_group_block[p.name]||{})[gid]); return '<label class="groupItem"><input type="checkbox" '+checked(blocked)+' data-platform="'+p.name+'" onchange="togglePlatformGroupBlock(this)"><span>'+escapeHTML(p.label)+'<small>'+(blocked?'已屏蔽':'未屏蔽')+' · '+escapeHTML(p.local||p.name)+'</small></span></label>'}).join('')||'<div class="muted">没有匹配的平台</div>';
}
function builtinSafetyCategories(){return (safetyBuiltins&&safetyBuiltins.length?safetyBuiltins:safetyCategories.map(c=>({id:c[0],label:c[1],keywords:[]})))}
function ensureSafetyMaps(){
 cfg.safety_global_categories=cfg.safety_global_categories||{};
 cfg.safety_platform_categories=cfg.safety_platform_categories||{};
 cfg.safety_custom_categories=cfg.safety_custom_categories||{};
 cfg.safety_custom_global=cfg.safety_custom_global||{};
 cfg.safety_custom_platform=cfg.safety_custom_platform||{};
 cfg.safety_exclude_global=cfg.safety_exclude_global||{};
 cfg.safety_exclude_platform=cfg.safety_exclude_platform||{};
}
function isBuiltinSafetyCategory(id){return builtinSafetyCategories().some(c=>c.id===id)}
function safetyCategoryLabelByID(id){const b=builtinSafetyCategories().find(c=>c.id===id); if(b) return b.label; const c=(cfg.safety_custom_categories||{})[id]; return c&&c.label?c.label:id}
function allSafetyCategories(){
 const builtins=builtinSafetyCategories().map(c=>({id:c.id,label:c.label,builtin:true,keywords:c.keywords||[],hidden:!!c.hidden}));
 const customs=Object.keys(cfg.safety_custom_categories||{}).sort().map(id=>({id:id,label:(cfg.safety_custom_categories[id]&&cfg.safety_custom_categories[id].label)||id,builtin:false,keywords:(cfg.safety_custom_categories[id]&&cfg.safety_custom_categories[id].words)||[]}));
 return builtins.concat(customs);
}
function renderSafetySettings(){
 if(!cfg||!$('safetyEnabledSwitch')) return;
 ensureSafetyMaps();
 if(!allSafetyCategories().some(c=>c.id===selectedSafetyCategory)) selectedSafetyCategory=(allSafetyCategories()[0]||{}).id||'adult';
 $('safetyEnabledSwitch').innerHTML=switchHTML('cfg.safety_filter_enabled', cfg.safety_filter_enabled!==false);
 if($('safetyTwitterSensitiveSwitch')) $('safetyTwitterSensitiveSwitch').innerHTML=switchHTML('cfg.safety_twitter_sensitive_block', cfg.safety_twitter_sensitive_block!==false);
 $('safetyNoticeSwitch').innerHTML=switchHTML('cfg.safety_filter_notice', !!cfg.safety_filter_notice);
 if($('safetyNoticeText')) $('safetyNoticeText').value=cfg.safety_filter_notice_text||'内容触发安全屏蔽，已停止解析。';
 const platformOptions=platforms.map(p=>'<option value="'+p.name+'">'+escapeHTML(p.label)+' / '+escapeHTML(p.name)+'</option>').join('');
 $('safetyPlatformSelect').innerHTML=platformOptions;
 renderSafetyCategorySelect();
 renderSafetyCategoryEditor();
 renderSafetyGlobalCategories();
 renderSafetyPlatformCategories();
}
function renderMediaShieldSettings(){
 if(!cfg) return;
 cfg.media_shield_user_enabled=cfg.media_shield_user_enabled||{};
 cfg.media_shield_group_enabled=cfg.media_shield_group_enabled||{};
 if($('mediaShieldPluginStatus')){
  $('mediaShieldPluginStatus').textContent=cfg.media_shield_enabled?'已启用':'未启用';
  $('mediaShieldPluginStatus').className=cfg.media_shield_enabled?'ok':'muted';
 }
 if($('mediaShieldEnabledSwitch')) $('mediaShieldEnabledSwitch').innerHTML=switchHTML('cfg.media_shield_enabled', !!cfg.media_shield_enabled);
 if($('mediaShieldPassiveSwitch')) $('mediaShieldPassiveSwitch').innerHTML=switchHTML('cfg.media_shield_passive', cfg.media_shield_passive!==false);
 if($('mediaShieldActiveSwitch')) $('mediaShieldActiveSwitch').innerHTML=switchHTML('cfg.media_shield_active', cfg.media_shield_active!==false);
 if($('mediaShieldPrivateSwitch')) $('mediaShieldPrivateSwitch').innerHTML=switchHTML('cfg.media_shield_private_enabled', !!cfg.media_shield_private_enabled);
 if($('mediaShieldUserEnabled')) $('mediaShieldUserEnabled').value=listText(cfg.media_shield_user_enabled);
 if($('mediaShieldReplyText')) $('mediaShieldReplyText').value=cfg.media_shield_reply_text||'已打包，解压密码：{password}';
 if($('mediaShieldEmoji')) $('mediaShieldEmoji').value=cfg.media_shield_emoji||'😏';
 if($('mediaShieldKeywords')) $('mediaShieldKeywords').value=(cfg.media_shield_keywords||[]).join('\n');
 if($('mediaShieldPassiveWords')) $('mediaShieldPassiveWords').value=(cfg.media_shield_passive_words||[]).join('\n');
 if($('mediaShieldPassiveExcludes')) $('mediaShieldPassiveExcludes').value=(cfg.media_shield_passive_excludes||[]).join('\n');
 renderMediaShieldGroups();
}
function mediaShieldGroupIDs(){
 const ids={}; groups.forEach(g=>ids[String(g.id)]=true);
 Object.keys((cfg&&cfg.media_shield_group_enabled)||{}).forEach(id=>{if(cfg.media_shield_group_enabled[id]) ids[String(id)]=true});
 return Object.keys(ids).sort((a,b)=>{
  const av=!!cfg.media_shield_group_enabled[a], bv=!!cfg.media_shield_group_enabled[b];
  if(av!==bv) return bv-av;
  return groupLabel(a).localeCompare(groupLabel(b),'zh-CN');
 });
}
function toggleMediaShieldGroup(el){
 cfg.media_shield_group_enabled=cfg.media_shield_group_enabled||{};
 if(el.checked) cfg.media_shield_group_enabled[String(el.dataset.id)]=true;
 else delete cfg.media_shield_group_enabled[String(el.dataset.id)];
 markDirty();
 renderMediaShieldGroups();
}
function renderMediaShieldGroups(){
 if(!cfg||!$('mediaShieldGroupPicker')) return;
 cfg.media_shield_group_enabled=cfg.media_shield_group_enabled||{};
 const q=String(($('mediaShieldGroupSearch')&&$('mediaShieldGroupSearch').value)||'').toLowerCase().trim();
 const ids=mediaShieldGroupIDs().filter(id=>!q||String(id).includes(q)||groupLabel(id).toLowerCase().includes(q));
 $('mediaShieldGroupPicker').innerHTML=ids.map(id=>{
  const on=!!cfg.media_shield_group_enabled[id];
  return '<label class="groupItem"><input type="checkbox" '+checked(on)+' data-id="'+escapeHTML(id)+'" onchange="toggleMediaShieldGroup(this)"><span>'+escapeHTML(groupLabel(id))+'<small>'+(on?'MediaShield 已启用':'未启用')+'</small></span></label>';
 }).join('')||'<div class="muted">没有可显示的群；先刷新群列表。</div>';
}
function renderSafetyCategorySelect(){
 const list=allSafetyCategories();
 if(!$('safetyCategorySelect')) return;
 $('safetyCategorySelect').innerHTML=list.map(c=>{
  const n=c.builtin?(c.keywords||[]).length:((cfg.safety_custom_categories[c.id]&&cfg.safety_custom_categories[c.id].words||[]).length);
  const count=c.hidden?'预览隐藏':(n+' 词');
  return '<option value="'+escapeHTML(c.id)+'">'+escapeHTML(c.label)+' · '+(c.builtin?'内置':'自定义')+' · '+escapeHTML(c.id)+' · '+count+'</option>';
 }).join('');
 $('safetyCategorySelect').value=selectedSafetyCategory;
}
function selectSafetyCategory(id){collectSafetyCategoryEditor(); selectedSafetyCategory=id; renderSafetySettings()}
function renderSafetyGlobalCategories(){
 const list=allSafetyCategories();
 $('safetyGlobalCategories').innerHTML=list.map(c=>'<label class="groupItem"><input type="checkbox" '+checked(!!cfg.safety_global_categories[c.id])+' data-category="'+escapeHTML(c.id)+'" onchange="setSafetyGlobalCategory(this.dataset.category,this.checked)"><span>'+escapeHTML(c.label)+'<small>'+(c.builtin?'内置':'自定义')+' · '+escapeHTML(c.id)+'</small></span></label>').join('');
}
function setSafetyGlobalCategory(cat,on){ensureSafetyMaps(); cfg.safety_global_categories[cat]=on; markDirty(); renderSafetyCategorySelect()}
function setSafetyPlatformCategory(platform,cat,on){ensureSafetyMaps(); cfg.safety_platform_categories[platform]=cfg.safety_platform_categories[platform]||{}; cfg.safety_platform_categories[platform][cat]=on; markDirty(); renderSafetyCategorySelect()}
function renderSafetyPlatformCategories(){
 if(!cfg||!$('safetyPlatformCategories')) return;
 ensureSafetyMaps();
 const platform=$('safetyPlatformSelect').value || (platforms[0]&&platforms[0].name) || '';
 const map=cfg.safety_platform_categories[platform]||{};
 $('safetyPlatformCategories').innerHTML=allSafetyCategories().map(c=>'<label class="groupItem"><input type="checkbox" '+checked(!!map[c.id])+' data-platform="'+escapeHTML(platform)+'" data-category="'+escapeHTML(c.id)+'" onchange="setSafetyPlatformCategory(this.dataset.platform,this.dataset.category,this.checked)"><span>'+escapeHTML(c.label)+'<small>'+(c.builtin?'内置':'自定义')+' · '+escapeHTML(c.id)+'</small></span></label>').join('');
}
function splitWords(text){return String(text||'').split(/\n+/).map(x=>x.trim().replace(/^#+/,'')).filter(Boolean)}
function renderSafetyCategoryEditor(){
 ensureSafetyMaps();
 const id=selectedSafetyCategory;
 const builtin=builtinSafetyCategories().find(c=>c.id===id);
 const custom=cfg.safety_custom_categories[id]||{label:'',words:[],excludes:[]};
 $('safetyCategoryTitle').textContent=builtin?builtin.label:(custom.label||id);
 $('safetyCategoryMeta').textContent=builtin?(builtin.hidden?'内置分类，词库预览已隐藏；排除词仍可用于处理误杀。':'内置分类，只能预览词库；排除词用于处理误杀。'):'自定义分类，可编辑名称、屏蔽词和排除词。';
 $('safetyCategoryID').value=id||'';
 $('safetyCategoryID').disabled=!!builtin;
 $('safetyCategoryLabel').value=builtin?builtin.label:(custom.label||'');
 $('safetyCategoryLabel').disabled=!!builtin;
 $('safetyDeleteCategory').style.display=builtin?'none':'inline-block';
 $('safetyCategoryIdentity').style.display=builtin?'none':'grid';
 $('safetyBuiltinPreviewWrap').style.display=builtin?'flex':'none';
 $('safetyCustomWordsLabel').textContent=builtin?'补充屏蔽词':'自定义屏蔽词';
 $('safetyCustomExcludesLabel').textContent=builtin?'本分类排除词 / 白名单':'排除词 / 白名单';
 $('safetyBuiltinPreview').value=builtin?(builtin.hidden?'此分类内置词已隐藏，仅后端匹配使用。':(builtin.keywords||[]).join('\n')):'';
 $('safetyCustomWords').value=builtin?((cfg.safety_custom_global&&cfg.safety_custom_global[id])||[]).join('\n'):(custom.words||[]).join('\n');
 $('safetyCustomWords').disabled=false;
 $('safetyCustomExcludes').value=builtin?((cfg.safety_exclude_global&&cfg.safety_exclude_global[id])||[]).join('\n'):(custom.excludes||[]).join('\n');
}
function normalizeSafetyID(raw){return String(raw||'').toLowerCase().trim().replace(/[^a-z0-9_-]+/g,'_').replace(/^_+|_+$/g,'')}
function collectSafetyCategoryEditor(){
 if(!cfg||!$('safetyCategoryID')||!selectedSafetyCategory) return;
 ensureSafetyMaps();
 const oldID=selectedSafetyCategory;
 if(isBuiltinSafetyCategory(oldID)){
  cfg.safety_custom_global[oldID]=splitWords($('safetyCustomWords').value);
  cfg.safety_exclude_global[oldID]=splitWords($('safetyCustomExcludes').value);
  return;
 }
 const item=cfg.safety_custom_categories[oldID]||{};
 item.label=String($('safetyCategoryLabel').value||oldID).trim()||oldID;
 item.words=splitWords($('safetyCustomWords').value);
 item.excludes=splitWords($('safetyCustomExcludes').value);
 cfg.safety_custom_categories[oldID]=item;
 cfg.safety_custom_platform={}; cfg.safety_exclude_platform={};
}
function editSafetyCustomCategoryID(){
 if(isBuiltinSafetyCategory(selectedSafetyCategory)) return;
 const next=normalizeSafetyID($('safetyCategoryID').value);
 if(!next||next===selectedSafetyCategory||isBuiltinSafetyCategory(next)) return;
 ensureSafetyMaps();
 if(cfg.safety_custom_categories[next]) return;
 const old=selectedSafetyCategory;
 cfg.safety_custom_categories[next]=cfg.safety_custom_categories[old]||{label:next,words:[],excludes:[]};
 delete cfg.safety_custom_categories[old];
 if(Object.prototype.hasOwnProperty.call(cfg.safety_global_categories,old)){cfg.safety_global_categories[next]=cfg.safety_global_categories[old]; delete cfg.safety_global_categories[old]}
 Object.values(cfg.safety_platform_categories||{}).forEach(m=>{if(Object.prototype.hasOwnProperty.call(m,old)){m[next]=m[old]; delete m[old]}});
 selectedSafetyCategory=next; markDirty(); renderSafetySettings();
}
function addSafetyCustomCategory(){
 ensureSafetyMaps();
 let i=1,id='custom_category';
 while(cfg.safety_custom_categories[id]||isBuiltinSafetyCategory(id)){id='custom_category_'+i++}
 cfg.safety_custom_categories[id]={label:'新分类',words:[],excludes:[]};
 selectedSafetyCategory=id; markDirty(); renderSafetySettings();
}
function deleteSafetyCustomCategory(){
 if(isBuiltinSafetyCategory(selectedSafetyCategory)) return;
 const id=selectedSafetyCategory;
 delete cfg.safety_custom_categories[id];
 delete cfg.safety_global_categories[id];
 Object.values(cfg.safety_platform_categories||{}).forEach(m=>delete m[id]);
 selectedSafetyCategory=(allSafetyCategories()[0]||{}).id||'adult';
 markDirty(); renderSafetySettings();
}
function escapeHTML(s){return String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
async function save(){
 collectSafetyCategoryEditor();
 collectCookieCloudSettings();
 if($('mediaShieldReplyText')) cfg.media_shield_reply_text=String($('mediaShieldReplyText').value||'').trim();
 if($('mediaShieldEmoji')) cfg.media_shield_emoji=String($('mediaShieldEmoji').value||'').trim();
 if($('mediaShieldKeywords')) cfg.media_shield_keywords=splitWords($('mediaShieldKeywords').value);
 if($('mediaShieldPassiveWords')) cfg.media_shield_passive_words=splitWords($('mediaShieldPassiveWords').value);
 if($('mediaShieldPassiveExcludes')) cfg.media_shield_passive_excludes=splitWords($('mediaShieldPassiveExcludes').value);
 if($('mediaShieldUserEnabled')) cfg.media_shield_user_enabled=parseList($('mediaShieldUserEnabled').value);
 if($('safetyNoticeText')) cfg.safety_filter_notice_text=String($('safetyNoticeText').value||'').trim();
 cfg.video_max_resolution=Number($('res').value); cfg.max_video_mb=Number($('maxmb').value); cfg.cache_ttl_minutes=Number($('ttl').value); cfg.parse_reaction_emoji=String($('reactionEmoji').value||'🍉').trim()||'🍉'; cfg.fail_reaction_emoji=String($('failReactionEmoji').value||'❌').trim()||'❌';
 cfg.yt_dlp_cookie_file=''; cfg.youtube_cookie_file=''; cfg.instagram_cookie_file=''; cfg.youtube_extractor_args=String($('youtubeExtractorArgs').value||'').trim(); cfg.proxy=String($('proxy').value||'').trim(); cfg.browser_cdp_url=String($('browserCDPURL').value||'').trim();
	cfg.bilibili_cookie=String($('bilibiliCookie').value||'').trim(); cfg.bilibili_use_cookie=!!cfg.bilibili_cookie; cfg.xiaohongshu_cookie=String($('xiaohongshuCookie').value||'').trim(); cfg.youtube_cookie=String($('youtubeCookie').value||'').trim(); cfg.instagram_cookie=String($('instagramCookie').value||'').trim(); cfg.keylol_cookie=String($('keylolCookie').value||'').trim(); cfg.linuxdo_cookie=String($('linuxdoCookie').value||'').trim(); cfg.linuxdo_ua=String($('linuxdoUA').value||'').trim(); cfg.linuxdo_flaresolverr_url=String($('linuxdoFlaresolverrURL').value||'').trim(); cfg.linuxdo_flaresolverr_wait_seconds=Number($('linuxdoFlaresolverrWait').value||2); cfg.linuxdo_flaresolverr_timeout_ms=Number($('linuxdoFlaresolverrTimeout').value||60000); cfg.linuxdo_flaresolverr_use_proxy=!!cfg.linuxdo_flaresolverr_use_proxy;
 cfg.keylol_footer=String($('keylolFooter').value||'').trim();
 cfg.keylol_theme=String(cfg.keylol_theme||'auto').trim()||'auto';
 cfg.keylol_light_theme=keylolLightThemeValue($('keylolLightTheme')?$('keylolLightTheme').value:cfg.keylol_light_theme);
 cfg.keylol_dark_theme=keylolDarkThemeValue($('keylolDarkTheme')?$('keylolDarkTheme').value:cfg.keylol_dark_theme);
 cfg.keylol_asf_forward=cfg.keylol_asf_forward!==false;
 cfg.send_info_card=!!cfg.auto_parse; cfg.send_media=!!cfg.download_video; platforms.forEach(p=>{cfg.platform_info_card[p.name]=!!cfg.platform_enabled[p.name]; cfg.platform_send_media[p.name]=!!cfg.platform_download_video[p.name]});
 cfg.private_access_mode=$('pmode').value; cfg.group_access_mode=$('gmode').value; cfg.group_user_access_mode=$('gumode').value;
 cfg.user_whitelist=parseList($('userWhitelist').value); cfg.user_blacklist=parseList($('userBlacklist').value);
 cfg.group_whitelist=parseList($('groupWhitelist').value); cfg.group_blacklist=parseList($('groupBlacklist').value);
 cfg.group_user_whitelist=parseList($('groupUserWhitelist').value); cfg.group_user_blacklist=parseList($('groupUserBlacklist').value);
 const r=await apiFetch('/api/mediaparser/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(cfg)});
 dirty=!r.ok; $('saveMsg').textContent=r.ok?'已保存':'保存失败';
 if(r.ok) await load();
}
function formatBytes(n){n=Number(n||0);if(n<1024)return n+' B';if(n<1048576)return (n/1024).toFixed(1)+' KB';if(n<1073741824)return (n/1048576).toFixed(1)+' MB';return (n/1073741824).toFixed(2)+' GB'}
function renderCacheStats(){if(!$('cacheFiles'))return;$('cacheFiles').textContent=cacheInfo?String(cacheInfo.files||0):'-';$('cacheSize').textContent=cacheInfo?formatBytes(cacheInfo.bytes||0):'-'}
async function loadCacheStats(showMsg=true){try{cacheInfo=await (await apiFetch('/api/mediaparser/cache/stats')).json();renderCacheStats();if(showMsg&&$('cacheMsg'))$('cacheMsg').textContent='缓存统计已刷新'}catch(e){if(showMsg&&$('cacheMsg'))$('cacheMsg').textContent='缓存统计读取失败'}}
async function clearCache(){const r=await (await apiFetch('/api/mediaparser/cache/clear',{method:'POST'})).json(); $('saveMsg').textContent='已清理 '+(r.removed||0)+' 个缓存目录'; if($('cacheMsg'))$('cacheMsg').textContent='已清理 '+(r.removed||0)+' 个缓存目录'; await loadCacheStats(false)}
async function uploadLogo(platform){
 const input=$('logo-'+platform); if(!input.files||!input.files[0]) return;
 const fd=new FormData(); fd.append('file', input.files[0]);
 const r=await apiFetch('/api/mediaparser/logos?platform='+encodeURIComponent(platform),{method:'POST',body:fd});
 $('saveMsg').textContent=r.ok?'Logo 已保存':'Logo 保存失败';
 await load();
}
async function cacheLogoURL(platform){
 const el=$('logoUrl-'+platform); const raw=String(el.value||'').trim(); if(!raw){$('saveMsg').textContent='请先粘贴 Logo 图片链接'; return}
 $('saveMsg').textContent='正在缓存 Logo...';
 const r=await apiFetch('/api/mediaparser/logos?platform='+encodeURIComponent(platform),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:raw})});
 $('saveMsg').textContent=r.ok?'Logo 链接已缓存':'Logo 链接缓存失败';
 if(r.ok) el.value='';
 await load();
}
document.addEventListener('input',e=>{if(e.target&&e.target.closest('main')&&!String(e.target.id||'').startsWith('logoUrl-')) markDirty()});
document.addEventListener('change',e=>{if(e.target&&e.target.closest('main')&&!String(e.target.id||'').startsWith('logo-')) markDirty()});
load(); setInterval(refreshStatus,5000);
</script>
</body>
</html>`
