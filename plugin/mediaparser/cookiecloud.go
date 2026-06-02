package mediaparser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const cookieCloudDefaultIntervalMinutes = 60

type cookieCloudCookie struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Path   string `json:"path"`
}

type cookieCloudResponse struct {
	Action     string                         `json:"action"`
	CookieData map[string][]cookieCloudCookie `json:"cookie_data"`
	Data       map[string][]cookieCloudCookie `json:"data"`
	Cookies    map[string][]cookieCloudCookie `json:"cookies"`
	Error      string                         `json:"error"`
	Message    string                         `json:"message"`
}

type cookieCloudSyncResult struct {
	Updated  []string `json:"updated"`
	Skipped  []string `json:"skipped"`
	Warnings []string `json:"warnings"`
}

type cookieCloudPlatformSpec struct {
	Name    string
	Label   string
	Domains []string
}

var cookieCloudPlatforms = []cookieCloudPlatformSpec{
	{Name: "bilibili", Label: "B站", Domains: []string{"bilibili.com"}},
	{Name: "xiaohongshu", Label: "小红书", Domains: []string{"xiaohongshu.com"}},
	{Name: "youtube", Label: "YouTube", Domains: []string{"youtube.com", "google.com"}},
	{Name: "instagram", Label: "Instagram", Domains: []string{"instagram.com"}},
	{Name: "keylol", Label: "Keylol", Domains: []string{"keylol.com"}},
	{Name: "linuxdo", Label: "Linux.do", Domains: []string{"linux.do"}},
}

func cookieCloudSupportedPlatform(platform string) bool {
	return cookieCloudPlatformSpecByName(platform) != nil
}

func cookieCloudPlatformOptions() []map[string]string {
	out := make([]map[string]string, 0, len(cookieCloudPlatforms))
	for _, spec := range cookieCloudPlatforms {
		out = append(out, map[string]string{
			"name":  spec.Name,
			"label": spec.Label,
		})
	}
	return out
}

func cookieCloudPlatformSpecByName(platform string) *cookieCloudPlatformSpec {
	platform = strings.ToLower(strings.TrimSpace(platform))
	for i := range cookieCloudPlatforms {
		if cookieCloudPlatforms[i].Name == platform {
			return &cookieCloudPlatforms[i]
		}
	}
	return nil
}

func startCookieCloudSyncer() {
	go func() {
		time.Sleep(3 * time.Second)
		if _, err := syncCookieCloudNow(false); err != nil {
			logrus.Warnf("[mediaparser] cookiecloud_initial_sync_failed error=%v", err)
		}
		for {
			cfg := snapshotConfig()
			interval := cfg.CookieCloudIntervalMinutes
			if interval <= 0 {
				interval = cookieCloudDefaultIntervalMinutes
			}
			time.Sleep(time.Duration(interval) * time.Minute)
			if _, err := syncCookieCloudNow(false); err != nil {
				logrus.Warnf("[mediaparser] cookiecloud_sync_failed error=%v", err)
			}
		}
	}()
}

func syncCookieCloudNow(force bool) (cookieCloudSyncResult, error) {
	cfg := snapshotConfig()
	if !force && !cfg.CookieCloudEnabled {
		return cookieCloudSyncResult{}, nil
	}
	if strings.TrimSpace(cfg.CookieCloudServer) == "" || strings.TrimSpace(cfg.CookieCloudUUID) == "" || strings.TrimSpace(cfg.CookieCloudPassword) == "" {
		if force || cfg.CookieCloudEnabled {
			return cookieCloudSyncResult{}, fmt.Errorf("CookieCloud 配置不完整")
		}
		return cookieCloudSyncResult{}, nil
	}
	selected := cookieCloudSelectedPlatforms(cfg)
	if len(selected) == 0 {
		if force || cfg.CookieCloudEnabled {
			return cookieCloudSyncResult{}, fmt.Errorf("未选择需要同步的平台")
		}
		return cookieCloudSyncResult{}, nil
	}

	cookies, err := fetchCookieCloudCookies(cfg)
	if err != nil {
		return cookieCloudSyncResult{}, err
	}
	result := applyCookieCloudCookies(selected, cookies)
	if len(result.Updated) > 0 {
		sort.Strings(result.Updated)
		logrus.Infof("[mediaparser] cookiecloud_sync_ok updated=%s warnings=%d", strings.Join(result.Updated, ","), len(result.Warnings))
	} else if force || cfg.CookieCloudEnabled {
		logrus.Warnf("[mediaparser] cookiecloud_sync_no_update skipped=%s warnings=%d", strings.Join(result.Skipped, ","), len(result.Warnings))
	}
	return result, nil
}

func cookieCloudSelectedPlatforms(cfg config) []cookieCloudPlatformSpec {
	out := []cookieCloudPlatformSpec{}
	for _, spec := range cookieCloudPlatforms {
		if cfg.CookieCloudPlatforms != nil && cfg.CookieCloudPlatforms[spec.Name] {
			out = append(out, spec)
		}
	}
	return out
}

func fetchCookieCloudCookies(cfg config) ([]cookieCloudCookie, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.CookieCloudServer), "/")
	u, err := url.Parse(base + "/get/" + url.PathEscape(strings.TrimSpace(cfg.CookieCloudUUID)))
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"password": strings.TrimSpace(cfg.CookieCloudPassword)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CookieCloud HTTP %d", resp.StatusCode)
	}
	var payload cookieCloudResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Error != "" {
		return nil, fmt.Errorf("CookieCloud: %s", payload.Error)
	}
	if payload.Message != "" && strings.EqualFold(payload.Action, "error") {
		return nil, fmt.Errorf("CookieCloud: %s", payload.Message)
	}
	data := payload.CookieData
	if len(data) == 0 {
		data = payload.Data
	}
	if len(data) == 0 {
		data = payload.Cookies
	}
	if len(data) == 0 {
		if strings.TrimSpace(payload.Message) != "" {
			return nil, fmt.Errorf("CookieCloud: %s", payload.Message)
		}
		return nil, fmt.Errorf("CookieCloud 未返回 cookie_data")
	}
	out := []cookieCloudCookie{}
	for domain, list := range data {
		for _, ck := range list {
			if strings.TrimSpace(ck.Domain) == "" {
				ck.Domain = domain
			}
			if strings.TrimSpace(ck.Name) == "" {
				continue
			}
			out = append(out, ck)
		}
	}
	return out, nil
}

func applyCookieCloudCookies(platforms []cookieCloudPlatformSpec, cookies []cookieCloudCookie) cookieCloudSyncResult {
	result := cookieCloudSyncResult{}
	stateMu.Lock()
	defer stateMu.Unlock()
	for _, spec := range platforms {
		header := cookieCloudHeaderForDomains(cookies, spec.Domains)
		if header == "" {
			result.Skipped = append(result.Skipped, spec.Name)
			continue
		}
		switch spec.Name {
		case "bilibili":
			currentConf.BilibiliCookie = header
			currentConf.BilibiliUseCookie = true
		case "xiaohongshu":
			currentConf.XiaohongshuCookie = header
		case "youtube":
			currentConf.YouTubeCookie = header
		case "instagram":
			currentConf.InstagramCookie = header
		case "keylol":
			currentConf.KeylolCookie = header
		case "linuxdo":
			currentConf.LinuxdoCookie = header
			if !strings.Contains(strings.ToLower(header), "cf_clearance=") {
				result.Warnings = append(result.Warnings, "linuxdo 缺少 cf_clearance")
			}
		}
		result.Updated = append(result.Updated, spec.Name)
	}
	if len(result.Updated) > 0 {
		if err := saveConfigLocked(); err != nil {
			result.Warnings = append(result.Warnings, "保存配置失败: "+err.Error())
		}
	}
	return result
}

func cookieCloudHeaderForDomains(cookies []cookieCloudCookie, domains []string) string {
	pairs := []string{}
	seen := map[string]bool{}
	for _, ck := range cookies {
		name := strings.TrimSpace(ck.Name)
		if name == "" || ck.Value == "" || !cookieCloudDomainMatch(ck.Domain, domains) {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, name+"="+ck.Value)
	}
	return strings.Join(pairs, "; ")
}

func cookieCloudDomainMatch(domain string, targets []string) bool {
	d := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	for _, target := range targets {
		t := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(target)), ".")
		if d == t || strings.HasSuffix(d, "."+t) {
			return true
		}
	}
	return false
}
