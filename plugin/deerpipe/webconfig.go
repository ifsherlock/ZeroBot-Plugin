package deerpipe

import "time"

// WebDeerConfig 是 WebUI 读写的配置结构。
type WebDeerConfig = deerConfig

// WebDeerStats 是 WebUI 展示的运行统计。
type WebDeerStats = deerStats

// WebConfig 返回当前配置快照，供 WebUI 读取。
func WebConfig() WebDeerConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

// SaveWebConfig 归一化并保存 WebUI 提交的配置，立即生效。
func SaveWebConfig(next WebDeerConfig) (WebDeerConfig, error) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = normalizeConfig(next)
	if err := saveConfigLocked(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// WebStats 返回当前运行统计。
func WebStats() WebDeerStats {
	return statsSnapshot(time.Now())
}
