package mediaparser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
)

type WebStatusProvider func() map[string]any

type webPlatform struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Local string `json:"local"`
}

type webGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func StartWebUI(addr string, extra WebStatusProvider) {
	addr = strings.TrimSpace(addr)
	if addr == "" || addr == "off" || addr == "0" {
		return
	}
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
			"config_path":    configPath,
			"system_path":    SystemSettingsPath(),
			"cache_dir":      cacheDir,
			"mediaparser":    snapshotConfig(),
			"runtime_status": snapshotRuntime(),
		}
		if extra != nil {
			payload["bot"] = extra()
		}
		writeJSON(w, payload)
	})
	mux.HandleFunc("/api/mediaparser/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, map[string]any{"config": snapshotConfig(), "platforms": webPlatforms()})
		case http.MethodPost:
			var next config
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			normalizeConfig(&next)
			stateMu.Lock()
			currentConf = next
			err := saveConfigLocked()
			stateMu.Unlock()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "config": snapshotConfig()})
		default:
			writeMethodNotAllowed(w)
		}
	})
	mux.HandleFunc("/api/system/settings", serveSystemSettingsAPI)
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
	mux.HandleFunc("/api/mediaparser/logos", serveLogoAPI)
	mux.HandleFunc("/api/mediaparser/logos/image", serveLogoImageAPI)
	mux.HandleFunc("/api/onebot/groups", serveGroupListAPI)
	go func() {
		logrus.Infof("[webui] listening on %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logrus.Errorf("[webui] stopped: %v", err)
		}
	}()
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
		writeJSON(w, map[string]any{"settings": systemSettingsForWeb(), "path": SystemSettingsPath()})
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
	return systemSettingsResponse{
		WebUIAddr:      firstNonEmpty(settings.WebUIAddr, current.WebUIAddr),
		WSURL:          firstNonEmpty(settings.WSURL, current.WSURL),
		WSTokenSet:     settings.WSToken != "",
		Nickname:       firstNonEmpty(settings.Nickname, firstString(zero.BotConfig.NickName)),
		CommandPrefix:  firstNonEmpty(settings.CommandPrefix, zero.BotConfig.CommandPrefix),
		SuperUsers:     uniqueInt64(settings.SuperUsers),
		PendingRestart: pending,
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
	runtimeSystem.Nickname = firstNonEmpty(settings.Nickname, runtimeSystem.Nickname)
	runtimeSystem.CommandPrefix = firstNonEmpty(settings.CommandPrefix, runtimeSystem.CommandPrefix)
	runtimeSystem.SuperUsers = uniqueInt64(settings.SuperUsers)
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
		path, err := savePlatformLogo(platform, img)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		platformLogoCache.Delete(platform)
		writeJSON(w, map[string]any{"ok": true, "path": path})
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
			"path":    path,
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
	imaging.Paste(canvas, fit, image.Pt((238-b.Dx())/2, (88-b.Dy())/2))
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
.grid{display:grid;grid-template-columns:repeat(4,1fr);gap:14px}.panel{background:rgba(255,255,255,.94);border:1px solid rgba(203,213,225,.9);border-radius:10px;padding:16px;box-shadow:var(--shadow)}.sectionTitle{display:flex;gap:10px;align-items:center;margin-bottom:12px}.sectionTitle b{font-size:16px}.span4{grid-column:span 4}.span2{grid-column:span 2}
.metric{display:flex;flex-direction:column;gap:6px;min-height:94px}.metric span:first-child{font-size:12px;text-transform:uppercase;letter-spacing:.04em}.metric b{font-size:26px}.muted{color:var(--muted)}.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.right{margin-left:auto}
table{width:100%;border-collapse:separate;border-spacing:0;background:white;border:1px solid var(--line);border-radius:10px;overflow:hidden}th,td{padding:12px;border-bottom:1px solid var(--line);text-align:left;vertical-align:middle}th{background:#f8fafc;font-size:12px;color:#64748b;font-weight:700;text-transform:uppercase;letter-spacing:.04em}tr:last-child td{border-bottom:0}tbody tr:hover{background:#fbfdff}
button,select,input,textarea{border:1px solid var(--line);border-radius:8px;background:white;color:var(--text);padding:0 10px}button,select,input{height:34px}textarea{width:100%;min-height:110px;padding:9px 10px;resize:vertical;font:13px/1.45 ui-monospace,SFMono-Regular,Consolas,monospace}button{cursor:pointer;background:#fff;font-weight:650}button:hover{border-color:#b8c5d6}button.primary{background:var(--blue);border-color:var(--blue);color:#fff}button.danger{border-color:#fecdd3;color:var(--red);background:#fff7f7}
.hidden,.page{display:none!important}.page.active{display:block!important}.page.active.metric{display:flex!important}.controlPills{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.controlPills>label{background:var(--soft);border:1px solid var(--line);border-radius:999px;padding:7px 10px}
.field{display:flex;flex-direction:column;gap:6px;min-width:180px}.accessGrid{display:grid;grid-template-columns:repeat(3,1fr);gap:12px;margin-top:12px}.accessGrid .field{min-width:0}.accessGrid label{font-weight:650}.accessGrid textarea{font-weight:400}
.groupTools{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px}.groupBox{border:1px solid var(--line);border-radius:10px;padding:12px;background:#fbfdff}.groupList{max-height:260px;overflow:auto;margin-top:8px}.groupItem{display:flex;gap:8px;align-items:flex-start;padding:7px 3px;border-bottom:1px solid #eef2f7}.groupItem:last-child{border-bottom:0}.groupItem span{font-size:13px}.groupItem small{display:block;color:var(--muted)}
.logoWrap{display:grid;grid-template-columns:92px minmax(240px,1fr);gap:10px;align-items:center}.logoPreview{width:92px;height:42px;object-fit:contain;border:1px solid var(--line);border-radius:8px;background:#fff}.logoEmpty{width:92px;height:42px;display:flex;align-items:center;justify-content:center;border:1px dashed var(--line);border-radius:8px;color:var(--muted);background:#fafbfc;font-size:12px}.logoTools{display:grid;grid-template-columns:auto minmax(160px,1fr) auto;gap:8px;align-items:center}.logoTools input[type=text]{width:100%}
.lastMsg{max-height:76px;overflow:hidden;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;word-break:break-all;overflow-wrap:anywhere;margin-bottom:0;background:#f8fafc;border:1px solid var(--line);border-radius:8px;padding:10px}.lastMsg.expanded{max-height:220px;overflow:auto;display:block;-webkit-line-clamp:unset}
.switch{position:relative;display:inline-block;width:42px;height:24px;flex:0 0 auto}.switch input{display:none}.slider{position:absolute;inset:0;background:#cbd5e1;border-radius:999px;transition:.15s}.slider:before{content:"";position:absolute;width:20px;height:20px;left:2px;top:2px;background:white;border-radius:50%;transition:.15s;box-shadow:0 1px 3px #0002}.switch input:checked+.slider{background:var(--blue)}.switch input:checked+.slider:before{transform:translateX(18px)}
.ok{color:var(--green);font-weight:700}.bad{color:var(--red);font-weight:700}.msg{min-height:20px;color:var(--muted)}.statusDot{display:inline-flex;align-items:center;gap:6px}.statusDot:before{content:"";width:8px;height:8px;background:var(--green);border-radius:50%;box-shadow:0 0 0 4px #dcfce7}
@media(max-width:980px){header{height:auto;min-height:58px;padding:10px 14px;gap:10px;align-items:flex-start}header .toolbar{justify-content:flex-end}h1{font-size:18px}.app{display:block;min-height:calc(100vh - 58px)}.sidebar{position:sticky;top:58px;z-index:15;padding:8px 10px;border-right:0;border-bottom:1px solid rgba(148,163,184,.28);background:rgba(255,255,255,.86);backdrop-filter:blur(14px)}.brand{display:none}.nav{display:flex;flex-direction:row;gap:8px;overflow-x:auto;overscroll-behavior-x:contain;padding:2px 2px 6px;scrollbar-width:thin}.nav a{flex:0 0 auto;white-space:nowrap;padding:8px 12px;background:rgba(255,255,255,.68);border:1px solid rgba(203,213,225,.75)}.nav a.active{background:var(--blue);color:#fff;border-color:var(--blue)}.subnav{flex-wrap:nowrap;overflow-x:auto;overscroll-behavior-x:contain;padding-bottom:4px}.subnav button{flex:0 0 auto}.pluginHead{display:block}.grid,.accessGrid,.groupTools,.logoWrap,.logoTools{grid-template-columns:1fr}.span2,.span4{grid-column:span 1}.wrap{padding:14px}.hero{align-items:flex-start;flex-direction:column}.hero h2{font-size:24px}.toolbar .primary{height:34px;padding:0 12px}table{font-size:12px;display:block;overflow-x:auto}th,td{padding:8px}.panel{border-radius:9px;padding:14px}.metric b{font-size:24px}}
</style>
</head>
<body>
<header><h1>ZeroBot 控制台</h1><div class="toolbar"><span class="statusDot" id="topState">加载中</span><button class="primary" onclick="save()">保存配置</button></div></header>
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
<div class="hero"><div><h2>机器人控制台</h2><p>查看运行状态，调整媒体解析与插件配置。</p></div><div class="toolbar"><button onclick="refreshStatus()">刷新状态</button><button class="danger" onclick="clearCache()">清理缓存</button></div></div>
<section class="grid">
<div class="span4" id="overview"></div>
<div class="panel metric page active" data-page="overview"><span class="muted">服务状态</span><b id="svc">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">机器人 QQ</span><b id="self">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">解析成功</span><b id="okn">-</b></div>
<div class="panel metric page active" data-page="overview"><span class="muted">解析失败</span><b id="failn">-</b></div>
<div class="panel span2 page active" data-page="overview"><div class="sectionTitle"><b>最近消息</b><button class="right" onclick="toggleLastMsg()">展开</button></div><p class="muted lastMsg" id="lastMsg">-</p></div>
<div class="panel span2 page active" data-page="overview"><div class="sectionTitle"><b>运行信息</b></div><p class="muted" id="runtimeSummary">-</p></div>
<div class="panel span4 page" data-page="system" id="system">
<div class="sectionTitle"><b>全局设置</b><span class="muted">端口和 WS 地址保存后需要重启服务生效；超级管理员、昵称、命令前缀会立即写入运行时。</span><span class="right msg" id="systemMsg"></span></div>
<div class="accessGrid">
<label class="field">WebUI 监听地址 <input id="sysWebui" placeholder="0.0.0.0:3000"></label>
<label class="field">OneBot WS 地址 <input id="sysWS" placeholder="ws://127.0.0.1:3001"></label>
<label class="field">OneBot Token <input id="sysToken" type="password" placeholder="留空表示不修改"></label>
<label class="field">机器人昵称 <input id="sysNick" placeholder="ZeroBot"></label>
<label class="field">命令前缀 <input id="sysPrefix" placeholder="/"></label>
<label class="field">超级管理员 QQ <textarea id="sysSuperUsers" placeholder="一行一个 QQ"></textarea></label>
</div>
<div class="row" style="margin-top:12px"><button class="primary" onclick="saveSystemSettings()">保存全局设置</button><span class="muted" id="sysPending"></span></div>
</div>
<div class="panel span4 page" data-page="plugins" id="plugins">
<div class="sectionTitle"><b>插件中心</b><span class="muted">每个插件以后独立放入口，聚合解析只是其中一个配置页。</span></div>
<table><thead><tr><th>插件</th><th>状态</th><th>说明</th><th>操作</th></tr></thead><tbody><tr><td><b>聚合解析</b><div class="muted">Media Parser</div></td><td><span class="ok">已启用</span></td><td>短视频、图文、动态、商品链接解析</td><td><button onclick="showPage('mediaparser:basic')">进入配置</button></td></tr><tr><td><b>控制功能</b><div class="muted">Manager</div></td><td><span class="ok">已启用</span></td><td>基础群管理和机器人控制能力</td><td><button disabled>待接入</button></td></tr></tbody></table>
</div>
<div class="panel span4 page" data-page="mediaparser" id="mediaparserHead">
<div class="pluginHead"><div><div class="crumb">插件中心 / 聚合解析</div><div class="sectionTitle"><b>聚合解析</b><span class="muted">短视频、图文、动态、商品链接解析配置。</span></div></div><button onclick="showPage('plugins')">返回插件中心</button></div>
<div class="subnav" id="mediaparserTabs">
<button class="active" data-plugin-tab="basic" onclick="showPluginSection('basic')">基础开关</button>
<button data-plugin-tab="platforms" onclick="showPluginSection('platforms')">平台设置</button>
<button data-plugin-tab="access" onclick="showPluginSection('access')">黑白名单</button>
<button data-plugin-tab="group-platform" onclick="showPluginSection('group-platform')">群平台开关</button>
<button data-plugin-tab="runtime" onclick="showPluginSection('runtime')">下载与 Cookie</button>
</div>
</div>
<div class="panel span2 page plugin-section active" data-page="mediaparser" data-plugin-section="basic" id="global"><div class="sectionTitle"><b>聚合解析总开关</b><span class="right msg" id="saveMsg"></span></div><div class="controlPills" id="globalControls"></div></div>
<div class="panel span2 page plugin-section active" data-page="mediaparser" data-plugin-section="basic"><div class="sectionTitle"><b>解析状态</b></div><p class="muted">解析成功 <b id="okn2">-</b> 次，失败 <b id="failn2">-</b> 次。</p></div>
<div class="span4 page plugin-section" data-page="mediaparser" data-plugin-section="platforms" id="platforms">
<div class="sectionTitle"><b>平台开关与 Logo</b><span class="muted">每个平台可独立控制解析、卡片、媒体和下载。</span></div>
<table><thead><tr><th>平台</th><th>解析</th><th>卡片</th><th>媒体</th><th>下载</th><th>Logo</th></tr></thead><tbody id="platformRows"></tbody></table>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="access" id="access">
<div class="sectionTitle"><b>访问控制</b><span class="muted">先判断私聊/群号是否允许，再判断群聊发言人；三套名单互不影响。</span></div>
<div class="row" style="margin-top:12px">
<label>私聊模式 <select id="pmode" onchange="onAccessModeChange()"><option value="none">关闭名单</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
<label>群聊模式 <select id="gmode" onchange="onAccessModeChange()"><option value="none">关闭名单</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
<label>群成员模式 <select id="gumode" onchange="onAccessModeChange()"><option value="none">关闭名单</option><option value="blacklist">黑名单</option><option value="whitelist">白名单</option></select></label>
</div>
<div class="accessGrid">
<label class="field accessField" data-mode="pmode" data-kind="whitelist">用户白名单<textarea id="userWhitelist" placeholder="例如：10000"></textarea></label>
<label class="field accessField" data-mode="pmode" data-kind="blacklist">用户黑名单<textarea id="userBlacklist" placeholder="一行一个 QQ 号"></textarea></label>
<label class="field accessField" data-mode="gmode" data-kind="whitelist">群白名单<textarea id="groupWhitelist" placeholder="一行一个群号"></textarea></label>
<label class="field accessField" data-mode="gmode" data-kind="blacklist">群黑名单<textarea id="groupBlacklist" placeholder="一行一个群号"></textarea></label>
<label class="field accessField" data-mode="gumode" data-kind="whitelist">群成员白名单<textarea id="groupUserWhitelist" placeholder="只在群聊里检查发言人 QQ"></textarea></label>
<label class="field accessField" data-mode="gumode" data-kind="blacklist">群成员黑名单<textarea id="groupUserBlacklist" placeholder="想屏蔽某人就填这里"></textarea></label>
</div>
<div class="row" style="margin-top:12px">
<button onclick="loadGroups(true)">刷新群列表</button><span class="muted" id="groupMsg">群列表用于快速勾选群白名单/群黑名单</span>
</div>
<div class="groupTools">
<div class="groupBox accessField" data-mode="gmode" data-kind="whitelist"><div class="row"><b>勾选到群白名单</b><input id="groupWhiteSearch" placeholder="搜索群名或群号" oninput="renderGroupPickers()"></div><div class="groupList" id="groupWhitePicker"></div></div>
<div class="groupBox accessField" data-mode="gmode" data-kind="blacklist"><div class="row"><b>勾选到群黑名单</b><input id="groupBlackSearch" placeholder="搜索群名或群号" oninput="renderGroupPickers()"></div><div class="groupList" id="groupBlackPicker"></div></div>
</div>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="group-platform" id="group-platform">
<div class="sectionTitle"><b>群平台开关</b><span class="muted">先选群，再选择这个群里要屏蔽哪些平台；不影响其他群。</span></div>
<div class="row" style="margin-top:12px">
<label>群 <select id="platformBlockGroupSelect" onchange="renderPlatformGroupBlock()"></select></label>
<button class="danger" onclick="clearGroupPlatformBlock()">清空当前群屏蔽</button>
<span class="muted" id="platformBlockMsg">勾选表示在当前群屏蔽该平台解析</span>
</div>
<div class="groupBox" style="margin-top:12px"><div class="row"><b>当前群的平台屏蔽</b><input id="platformBlockSearch" placeholder="搜索平台" oninput="renderPlatformGroupBlock()"></div><div class="groupList" id="platformBlockPicker"></div></div>
</div>
<div class="panel span4 page plugin-section" data-page="mediaparser" data-plugin-section="runtime" id="runtime">
<div class="row">
<b>下载与调试</b>
<label>分辨率 <select id="res"><option value="0">不限</option><option value="360">360p</option><option value="720">720p</option><option value="1080">1080p</option></select></label>
<label>最大 MB <input id="maxmb" type="number" min="1" style="width:96px"></label>
<label>缓存分钟 <input id="ttl" type="number" min="1" style="width:96px"></label>
<label>解析回应 <input id="reactionEmoji" maxlength="8" style="width:72px" placeholder="🍉"></label>
<label>失败回应 <input id="failReactionEmoji" maxlength="8" style="width:72px" placeholder="❌"></label>
<button class="danger" onclick="clearCache()">清理缓存</button>
<button class="primary right" onclick="save()">保存</button>
</div>
<div class="accessGrid" style="margin-top:12px">
<label class="field">yt-dlp Cookie 文件 <input id="ytdlpCookieFile" placeholder="/path/to/cookies.txt"></label>
<label class="field">YouTube Cookie 文件 <input id="youtubeCookieFile" placeholder="/path/to/youtube-cookies.txt"></label>
<label class="field">Instagram Cookie 文件 <input id="instagramCookieFile" placeholder="/path/to/instagram-cookies.txt"></label>
<label class="field">YouTube extractor 参数 <input id="youtubeExtractorArgs" placeholder="youtube:player_client=default,android;formats=missing_pot"></label>
<label class="field">B站 Cookie <textarea id="bilibiliCookie" placeholder="SESSDATA=...; bili_jct=..."></textarea></label>
<label class="field">小红书 Cookie <textarea id="xiaohongshuCookie" placeholder="a1=...; web_session=..."></textarea></label>
<label class="field">B站最高画质 <select id="bilibiliMaxQuality"><option value="不限制">不限制</option><option value="4K">4K</option><option value="1080P60">1080P60</option><option value="1080P+">1080P+</option><option value="1080P">1080P</option><option value="720P">720P</option><option value="480P">480P</option><option value="360P">360P</option></select></label>
</div>
</div>
<div class="panel span4 page" data-page="logs" id="logs"><div class="sectionTitle"><b>日志诊断</b><span class="muted">第一阶段先展示运行摘要和最近消息，后续可以接 journal 过滤。</span></div><p class="muted lastMsg expanded" id="logSummary">-</p></div>
<div class="panel span4 page" data-page="maintenance" id="maintenance"><div class="sectionTitle"><b>数据维护</b><span class="muted">缓存、Logo、本地配置文件维护。</span></div><div class="row"><button class="danger" onclick="clearCache()">清理媒体缓存</button><span class="muted" id="maintenanceMsg">配置文件只保存在本机 data 目录，不会上传到 GitHub。</span></div></div>
</section>
</main>
</div>
<script>
let cfg=null, sys=null, platforms=[], logos={}, groups=[], dirty=false, currentPluginSection='basic';
const $=id=>document.getElementById(id);
function showPage(name){
 const parts=String(name||'overview').split(':'); const page=parts[0]||'overview'; const section=parts[1]||currentPluginSection||'basic';
 document.querySelectorAll('.page').forEach(el=>el.classList.toggle('active', el.dataset.page===page));
 const sidePage=page==='mediaparser'?'plugins':page;
 document.querySelectorAll('[data-page-link]').forEach(el=>el.classList.toggle('active', el.dataset.pageLink===sidePage));
 if(page==='mediaparser') showPluginSection(section,false);
 const nextHash=page==='mediaparser'?'#mediaparser:'+currentPluginSection:'#'+page;
 if(location.hash!==nextHash) history.replaceState(null,'',nextHash);
}
function showPluginSection(name,updateHash=true){
 currentPluginSection=name||'basic';
 document.querySelectorAll('[data-plugin-section]').forEach(el=>el.classList.toggle('active', el.dataset.pluginSection===currentPluginSection));
 document.querySelectorAll('[data-plugin-tab]').forEach(el=>el.classList.toggle('active', el.dataset.pluginTab===currentPluginSection));
 if(updateHash && location.hash!=='#mediaparser:'+currentPluginSection) history.replaceState(null,'','#mediaparser:'+currentPluginSection);
}
function markDirty(){dirty=true; $('saveMsg').textContent='有未保存修改'}
function checked(v){return v?' checked':''}
function switchHTML(expr,on){return '<label class="switch"><input type="checkbox"'+checked(on)+' onchange="'+expr+'=this.checked;markDirty()"><span class="slider"></span></label>'}
function bindSwitch(map,key,name){return switchHTML("cfg."+key+"['"+name+"']", !!map[name])}
function logoCell(p){const info=logos[p.name]||{};const custom=!!info.exists;const src=info.url||('/api/mediaparser/logos/image?platform='+encodeURIComponent(p.name));const preview='<img class="logoPreview" src="'+escapeHTML(src)+'" alt="'+escapeHTML(p.label)+' Logo">';return '<div class="logoWrap">'+preview+'<div><div class="logoTools"><input id="logo-'+p.name+'" data-platform="'+p.name+'" type="file" accept="image/*" style="display:none" onchange="uploadLogo(this.dataset.platform)"><button data-target="logo-'+p.name+'" onclick="$(this.dataset.target).click()">'+(custom?'替换':'上传')+'</button><input id="logoUrl-'+p.name+'" type="text" placeholder="粘贴图片链接自动缓存"><button data-platform="'+p.name+'" onclick="cacheLogoURL(this.dataset.platform)">缓存链接</button></div><div class="muted">'+(custom?'已缓存本地 Logo':'使用内置 Logo，可上传覆盖')+'</div></div></div>'}
function listText(map){return Object.keys(map||{}).filter(k=>map[k]).sort((a,b)=>Number(a)-Number(b)).join('\n')}
function parseList(text){const out={}; String(text||'').split(/[\s,，;；]+/).map(x=>x.trim()).filter(Boolean).forEach(x=>{if(/^-?\d+$/.test(x)) out[x]=true}); return out}
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
async function refreshStatus(){
 const st=await (await fetch('/api/status')).json();
 $('svc').innerHTML='<span class="ok">运行中</span>'; $('topState').textContent=dirty?'有未保存修改':'WebUI 已连接';
 const rt=st.runtime_status||{}; const bot=st.bot||{};
 $('self').textContent=rt.last_self_id||bot.self_id||'-'; $('okn').textContent=rt.parse_success||0; $('failn').textContent=rt.parse_failed||0;
 $('okn2').textContent=rt.parse_success||0; $('failn2').textContent=rt.parse_failed||0;
 $('lastMsg').textContent=rt.last_message||'暂无消息';
 $('runtimeSummary').textContent='Go '+(st.go||'-')+' / WebUI '+((sys&&sys.webui_addr)||'-')+' / WS '+((sys&&sys.ws_url)||'-');
 $('logSummary').textContent='最近消息：'+(rt.last_message||'暂无')+'\n成功：'+(rt.parse_success||0)+'，失败：'+(rt.parse_failed||0);
}
async function load(){
 const data=await (await fetch('/api/mediaparser/config')).json();
 const sysData=await (await fetch('/api/system/settings')).json();
 const logoData=await (await fetch('/api/mediaparser/logos')).json();
 cfg=data.config; platforms=data.platforms;
 sys=sysData.settings||{};
 logos=logoData.logos||{};
 await refreshStatus();
 render();
}
function render(){
 const items=[['auto_parse','自动解析'],['send_info_card','发送卡片'],['send_media','发送媒体'],['download_video','下载视频'],['parse_reaction','解析回应'],['debug','调试日志'],['avoid_av1','禁用 AV1'],['use_yt_dlp_fallback','yt-dlp 备用']];
 $('globalControls').innerHTML=items.map(x=>'<label class="row">'+x[1]+switchHTML('cfg.'+x[0],!!cfg[x[0]])+'</label>').join('');
 $('platformRows').innerHTML=platforms.map(p=>'<tr><td><b>'+p.label+'</b><div class="muted">'+(p.local||p.name)+'</div></td><td>'+bindSwitch(cfg.platform_enabled,'platform_enabled',p.name)+'</td><td>'+bindSwitch(cfg.platform_info_card,'platform_info_card',p.name)+'</td><td>'+bindSwitch(cfg.platform_send_media,'platform_send_media',p.name)+'</td><td>'+bindSwitch(cfg.platform_download_video,'platform_download_video',p.name)+'</td><td>'+logoCell(p)+'</td></tr>').join('');
 if(!cfg.platform_group_block) cfg.platform_group_block={};
 $('pmode').value=cfg.private_access_mode||'none'; $('gmode').value=cfg.group_access_mode||'none'; $('gumode').value=cfg.group_user_access_mode||'none';
 $('userWhitelist').value=listText(cfg.user_whitelist); $('userBlacklist').value=listText(cfg.user_blacklist);
 $('groupWhitelist').value=listText(cfg.group_whitelist); $('groupBlacklist').value=listText(cfg.group_blacklist);
 $('groupUserWhitelist').value=listText(cfg.group_user_whitelist); $('groupUserBlacklist').value=listText(cfg.group_user_blacklist);
 $('res').value=String(cfg.video_max_resolution||0); $('maxmb').value=cfg.max_video_mb||1000; $('ttl').value=cfg.cache_ttl_minutes||60; $('reactionEmoji').value=cfg.parse_reaction_emoji||'🍉'; $('failReactionEmoji').value=cfg.fail_reaction_emoji||'❌';
 $('ytdlpCookieFile').value=cfg.yt_dlp_cookie_file||''; $('youtubeCookieFile').value=cfg.youtube_cookie_file||''; $('instagramCookieFile').value=cfg.instagram_cookie_file||''; $('youtubeExtractorArgs').value=cfg.youtube_extractor_args||'youtube:player_client=default,android;formats=missing_pot';
 $('bilibiliCookie').value=cfg.bilibili_cookie||''; $('xiaohongshuCookie').value=cfg.xiaohongshu_cookie||''; $('bilibiliMaxQuality').value=cfg.bilibili_max_quality||'不限制';
 renderSystemSettings();
 updateAccessVisibility();
 renderPlatformGroupBlock();
 if(!groups.length) loadGroups(false); else renderGroupPickers();
 showPage((location.hash||'#overview').slice(1)||'overview');
}
function renderSystemSettings(){
 if(!sys) return;
 $('sysWebui').value=sys.webui_addr||'';
 $('sysWS').value=sys.ws_url||'';
 $('sysToken').value='';
 $('sysToken').placeholder=sys.ws_token_set?'已设置，留空不修改':'留空表示不设置';
 $('sysNick').value=sys.nickname||'';
 $('sysPrefix').value=sys.command_prefix||'/';
 $('sysSuperUsers').value=(sys.super_users||[]).join('\n');
 const pending=sys.pending_restart||[];
 $('sysPending').textContent=pending.length?'重启后生效：'+pending.join('、'):'当前没有待重启生效的配置';
}
async function saveSystemSettings(){
 const payload={
  webui_addr:String($('sysWebui').value||'').trim(),
  ws_url:String($('sysWS').value||'').trim(),
  ws_token:String($('sysToken').value||'').trim(),
  nickname:String($('sysNick').value||'').trim(),
  command_prefix:String($('sysPrefix').value||'/').trim()||'/',
  super_users:Object.keys(parseList($('sysSuperUsers').value)).map(x=>Number(x))
 };
 const r=await fetch('/api/system/settings',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
 $('systemMsg').textContent=r.ok?'全局设置已保存':'全局设置保存失败';
 if(r.ok){const data=await r.json(); sys=data.settings||sys; renderSystemSettings(); await refreshStatus();}
}
async function loadGroups(force){
 try{
  $('groupMsg').textContent=force?'正在拉取群列表...':'正在读取群列表...';
  const data=await (await fetch('/api/onebot/groups')).json();
  groups=data.groups||[]; $('groupMsg').textContent='已拉取 '+groups.length+' 个群';
  renderGroupPickers();
 }catch(e){$('groupMsg').textContent='群列表拉取失败：'+e}
}
function renderGroupPickers(){
 updateAccessVisibility();
 renderGroupPicker('groupWhitePicker','groupWhiteSearch','groupWhitelist');
 renderGroupPicker('groupBlackPicker','groupBlackSearch','groupBlacklist');
 renderPlatformGroupBlock();
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
function escapeHTML(s){return String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}
async function save(){
 cfg.video_max_resolution=Number($('res').value); cfg.max_video_mb=Number($('maxmb').value); cfg.cache_ttl_minutes=Number($('ttl').value); cfg.parse_reaction_emoji=String($('reactionEmoji').value||'🍉').trim()||'🍉'; cfg.fail_reaction_emoji=String($('failReactionEmoji').value||'❌').trim()||'❌';
 cfg.yt_dlp_cookie_file=String($('ytdlpCookieFile').value||'').trim(); cfg.youtube_cookie_file=String($('youtubeCookieFile').value||'').trim(); cfg.instagram_cookie_file=String($('instagramCookieFile').value||'').trim(); cfg.youtube_extractor_args=String($('youtubeExtractorArgs').value||'').trim();
 cfg.bilibili_cookie=String($('bilibiliCookie').value||'').trim(); cfg.bilibili_use_cookie=!!cfg.bilibili_cookie; cfg.xiaohongshu_cookie=String($('xiaohongshuCookie').value||'').trim(); cfg.bilibili_max_quality=$('bilibiliMaxQuality').value||'不限制';
 cfg.private_access_mode=$('pmode').value; cfg.group_access_mode=$('gmode').value; cfg.group_user_access_mode=$('gumode').value;
 cfg.user_whitelist=parseList($('userWhitelist').value); cfg.user_blacklist=parseList($('userBlacklist').value);
 cfg.group_whitelist=parseList($('groupWhitelist').value); cfg.group_blacklist=parseList($('groupBlacklist').value);
 cfg.group_user_whitelist=parseList($('groupUserWhitelist').value); cfg.group_user_blacklist=parseList($('groupUserBlacklist').value);
 const r=await fetch('/api/mediaparser/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(cfg)});
 dirty=!r.ok; $('saveMsg').textContent=r.ok?'已保存':'保存失败';
 if(r.ok) await load();
}
async function clearCache(){const r=await (await fetch('/api/mediaparser/cache/clear',{method:'POST'})).json(); $('saveMsg').textContent='已清理 '+(r.removed||0)+' 个缓存文件'}
async function uploadLogo(platform){
 const input=$('logo-'+platform); if(!input.files||!input.files[0]) return;
 const fd=new FormData(); fd.append('file', input.files[0]);
 const r=await fetch('/api/mediaparser/logos?platform='+encodeURIComponent(platform),{method:'POST',body:fd});
 $('saveMsg').textContent=r.ok?'Logo 已保存':'Logo 保存失败';
 await load();
}
async function cacheLogoURL(platform){
 const el=$('logoUrl-'+platform); const raw=String(el.value||'').trim(); if(!raw){$('saveMsg').textContent='请先粘贴 Logo 图片链接'; return}
 $('saveMsg').textContent='正在缓存 Logo...';
 const r=await fetch('/api/mediaparser/logos?platform='+encodeURIComponent(platform),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:raw})});
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
