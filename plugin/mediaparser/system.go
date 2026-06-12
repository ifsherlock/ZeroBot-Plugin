package mediaparser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type SystemSettings struct {
	WebUIAddr         string  `json:"webui_addr"`
	WSURL             string  `json:"ws_url"`
	WSToken           string  `json:"ws_token,omitempty"`
	OneBotDataDir     string  `json:"onebot_data_dir,omitempty"`
	Nickname          string  `json:"nickname"`
	CommandPrefix     string  `json:"command_prefix"`
	SuperUsers        []int64 `json:"super_users"`
	QQBotEnabled      bool    `json:"qqbot_enabled"`
	QQBotName         string  `json:"qqbot_name"`
	QQBotAppID        string  `json:"qqbot_app_id,omitempty"`
	QQBotSecret       string  `json:"qqbot_secret,omitempty"`
	QQBotOpenID       string  `json:"qqbot_openid,omitempty"`
	QQBotGroupOpenID  string  `json:"qqbot_group_openid,omitempty"`
	QQBotPublicBase   string  `json:"qqbot_public_base,omitempty"`
	QQBotCardDisabled bool    `json:"qqbot_card_disabled,omitempty"`
	QQBotMediaEnabled bool    `json:"qqbot_media_enabled"`
	QQBotMarkdown     bool    `json:"qqbot_markdown"`
	TGBotEnabled      bool    `json:"tgbot_enabled"`
	TGBotName         string  `json:"tgbot_name"`
	TGBotToken        string  `json:"tgbot_token,omitempty"`
	TGBotAPIBase      string  `json:"tgbot_api_base,omitempty"`
	TGBotProxy        string  `json:"tgbot_proxy,omitempty"`
	TGBotMediaEnabled bool    `json:"tgbot_media_enabled"`
}

type systemSettingsResponse struct {
	WebUIAddr         string   `json:"webui_addr"`
	WSURL             string   `json:"ws_url"`
	WSToken           string   `json:"ws_token,omitempty"`
	WSTokenSet        bool     `json:"ws_token_set"`
	OneBotDataDir     string   `json:"onebot_data_dir,omitempty"`
	Nickname          string   `json:"nickname"`
	CommandPrefix     string   `json:"command_prefix"`
	SuperUsers        []int64  `json:"super_users"`
	QQBotEnabled      bool     `json:"qqbot_enabled"`
	QQBotName         string   `json:"qqbot_name"`
	QQBotAppID        string   `json:"qqbot_app_id,omitempty"`
	QQBotSecretSet    bool     `json:"qqbot_secret_set"`
	QQBotOpenID       string   `json:"qqbot_openid,omitempty"`
	QQBotGroupOpenID  string   `json:"qqbot_group_openid,omitempty"`
	QQBotPublicBase   string   `json:"qqbot_public_base,omitempty"`
	QQBotCardEnabled  bool     `json:"qqbot_card_enabled"`
	QQBotMediaEnabled bool     `json:"qqbot_media_enabled"`
	QQBotMarkdown     bool     `json:"qqbot_markdown"`
	QQBotAvailable    bool     `json:"qqbot_available"`
	TGBotEnabled      bool     `json:"tgbot_enabled"`
	TGBotName         string   `json:"tgbot_name"`
	TGBotTokenSet     bool     `json:"tgbot_token_set"`
	TGBotAPIBase      string   `json:"tgbot_api_base,omitempty"`
	TGBotProxy        string   `json:"tgbot_proxy,omitempty"`
	TGBotMediaEnabled bool     `json:"tgbot_media_enabled"`
	TGBotAvailable    bool     `json:"tgbot_available"`
	PendingRestart    []string `json:"pending_restart"`
}

var (
	systemMu       sync.RWMutex
	runtimeSystem  SystemSettings
	lastSystemPath string
)

func SystemSettingsPath() string {
	return filepath.Join(engine.DataFolder(), "system.json")
}

func LoadSystemSettings() SystemSettings {
	settings, _ := readSystemSettings()
	return settings
}

func SetRuntimeSystemSettings(settings SystemSettings) {
	systemMu.Lock()
	runtimeSystem = normalizeSystemSettings(settings)
	systemMu.Unlock()
}

func readSystemSettings() (SystemSettings, error) {
	path := SystemSettingsPath()
	lastSystemPath = path
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SystemSettings{}, nil
		}
		return SystemSettings{}, err
	}
	var settings SystemSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return SystemSettings{}, err
	}
	return normalizeSystemSettings(settings), nil
}

func saveSystemSettings(settings SystemSettings) error {
	settings = normalizeSystemSettings(settings)
	path := SystemSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeSystemSettings(settings SystemSettings) SystemSettings {
	settings.WebUIAddr = strings.TrimSpace(settings.WebUIAddr)
	settings.WSURL = strings.TrimSpace(settings.WSURL)
	settings.WSToken = strings.TrimSpace(settings.WSToken)
	settings.OneBotDataDir = strings.TrimSpace(settings.OneBotDataDir)
	settings.Nickname = strings.TrimSpace(settings.Nickname)
	settings.CommandPrefix = strings.TrimSpace(settings.CommandPrefix)
	settings.SuperUsers = uniqueInt64(settings.SuperUsers)
	settings.QQBotName = strings.TrimSpace(settings.QQBotName)
	settings.QQBotAppID = strings.TrimSpace(settings.QQBotAppID)
	settings.QQBotSecret = strings.TrimSpace(settings.QQBotSecret)
	settings.QQBotOpenID = strings.TrimSpace(settings.QQBotOpenID)
	settings.QQBotGroupOpenID = strings.TrimSpace(settings.QQBotGroupOpenID)
	settings.QQBotPublicBase = strings.TrimSpace(settings.QQBotPublicBase)
	settings.TGBotName = strings.TrimSpace(settings.TGBotName)
	if settings.TGBotName == "" {
		settings.TGBotName = "telegram"
	}
	settings.TGBotToken = strings.TrimSpace(settings.TGBotToken)
	settings.TGBotAPIBase = strings.TrimRight(strings.TrimSpace(settings.TGBotAPIBase), "/")
	if settings.TGBotAPIBase == "" {
		settings.TGBotAPIBase = "https://api.telegram.org"
	}
	settings.TGBotProxy = strings.TrimSpace(settings.TGBotProxy)
	return settings
}

func uniqueInt64(in []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if v <= 0 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func mergeInt64(a, b []int64) []int64 {
	out := append(append([]int64{}, a...), b...)
	return uniqueInt64(out)
}
