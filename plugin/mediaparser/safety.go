package mediaparser

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	safetyCategoryAdult    = "adult"
	safetyCategoryAd       = "ad"
	safetyCategoryViolence = "violence"
	safetyCategoryPolitics = "politics"

	safetyMarkerTwitterSensitive = "__mediaparser_twitter_possibly_sensitive__"

	currentSafetyCustomSeedVersion = 4
	currentSafetyDefaultVersion    = 3
	currentSafetyTwitterVersion    = 1
)

type safetyHit struct {
	Category string
	Keyword  string
	Source   string
}

type safetyCategoryDef struct {
	ID          string
	Label       string
	KeywordsB64 []string
}

type safetyCustomCategory struct {
	Label    string   `json:"label"`
	Words    []string `json:"words"`
	Excludes []string `json:"excludes"`
}

var safetyCategoryDefs = []safetyCategoryDef{
	{
		ID:    safetyCategoryAdult,
		Label: "色情",
		KeywordsB64: []string{
			"bnNmdw==", "cjE4", "ci0xOA==", "MTgr", "YWR1bHQgY29udGVudA==", "ZXhwbGljaXQ=",
			"cG9ybg==", "eHh4", "bnVkZQ==", "bnVkaXR5", "bGV3ZA==", "aGVudGFp",
			"ZWNjaGk=", "b25seWZhbnM=", "ZmFuc2x5",
			"5oiQ5Lq65YaF5a65", "6KO46IGK", "5oiQ5Lq65ZCR",
			"I05TRlc=", "5oiQ5Lq65ZCR44GR", "44Ko44Ot", "44GI44KN", "44OY44Oz44K/44Kk",
			"44OM44O844OJ", "7ISx7J2466y8", "MTnquIg=", "7JW87Kek",
			"7ZuE67Cp7KO87J2Y", "7J2M656A", "64iE65Oc",
			"d2F0dGE=", "Y2h1ZGFp", "Z29vbg==", "Z29vbmVy", "amF2", "d2F0YWE=",
			"Z29vbmV0dGU=",
			"am9p", "Z29vbmluZw==", "YmFiZWNvY2s=", "bnNmd3R3dA==",
			"Y3Vt", "bWlsZg==", "Y3Vtc2x1dA==",
			"c2x1dA==", "Y3Vjaw==", "d2Fua2NoYXQ=", "bGV3ZHJw", "YmlnZGljaw==", "cHVzc3k=",
			"c2V4",
		},
	},
	{
		ID:    safetyCategoryAd,
		Label: "广告",
		KeywordsB64: []string{
			"dC5tZQ==", "dGfnpo/liKk=", "ZG0gZm9yIG1lbnU=", "b25seWZhbnMuY29t", "ZmFuc2x5LmNvbQ==",
			"ZnJlZSBvbmx5ZmFucyBsZWFr", "bGVha2VkIG51ZGVz", "5Y2a5b2p5bmz5Y+w", "572R6LWM",
			"5oiQ5Lq65a+86Iiq", "55yL54mH572R5Z2A", "Y3J5cHRvIGdpdmVhd2F5", "YWlyZHJvcCBzY2Ft",
			"54Sh5L+u5q2j44Oq44Oz44Kv", "7ISx7J247IKs7J207Yq4", "67aI67KV7Yag7Yag", "64+E67CV7IKs7J207Yq4",
		},
	},
	{
		ID:    safetyCategoryViolence,
		Label: "暴恐",
		KeywordsB64: []string{
			"6JmQ5p2A", "5pap6aaW", "5YiG5bC4", "6IKi6Kej",
			"5aSE5Yaz6KeG6aKR", "6Ieq5q6L55u05pKt", "5p6q5pSv5Lqk5piT", "54K46I2v6YWN5pa5",
			"54iG54K454mp5Yi25L2c", "6Ieq5Yi254K45by5", "566h5Yi25YiA5YW35Lqk5piT", "Z3JhcGhpYyB2aW9sZW5jZQ==", "YmVoZWFkaW5n",
			"ZGlzbWVtYmVybWVudA==", "ZXhlY3V0aW9uIHZpZGVv", "Z2hvc3QgZ3Vu", "aG9tZW1hZGUgZXhwbG9zaXZl", "Ym9tYiBtYWtpbmc=",
			"aWVkIHR1dG9yaWFs", "5pas6aaW", "6YqD6LKp5aOy",
			"54iG6Jas5L2c5oiQ", "54iG5by+5L2c5oiQ", "7LC47IiY",
			"7LSd6riw6rGw656Y", "7Y+t7YOE7KCc7KGw", "7Y+t67Cc66y87KCc7KGw",
		},
	},
	{
		ID:    safetyCategoryPolitics,
		Label: "政治",
		KeywordsB64: []string{
			"5Lmg6L+R5bmz", "5Lit5YWx", "5YWx5Lqn5YWa", "5YWt5Zub", "5aSp5a6J6Zeo5LqL5Lu2", "5Y+w54us",
			"5riv54us", "6JeP54us", "55aG54us", "5rOV6L2u5Yqf", "5raJ5pS/", "5pS/5rK75pWP5oSf",
			"eGkgamlucGluZw==", "Y2Nw", "Y29tbXVuaXN0IHBhcnR5IG9mIGNoaW5h", "dGlhbmFubWVu", "anVuZSA0dGg=", "ZnJlZSB0aWJldA==",
			"ZnJlZSBob25nIGtvbmc=",
		},
	},
}

var safetyBuiltinWordCache = buildSafetyBuiltinWordCache()

func defaultSafetyPlatformCategories() map[string]map[string]bool {
	return map[string]map[string]bool{}
}

func defaultSafetyGlobalCategories() map[string]bool {
	return map[string]bool{
		safetyCategoryPolitics: true,
	}
}

func seedSafetyCustomWords(cfg *config) bool {
	const id = "custom_adult_extra"
	words := decodeSafetyBuiltinWords([]string{
		"56aP5Yip5aes", "5aSW5Zu0", "5L+u6L2m", "5Zu95Lqn5Lmx5Lym", "5o6i6Iqx", "6KO46IGK",
		"6Imy5Zu+", "6buE54mH", "55Wq5Y+3", "6KOP5Z6i", "6KOP5Z6i5aWz5a2Q", "6KOP5Z6i55S35a2Q",
		"44Kq44OK44Oa", "44OR44Kk5Ye4", "44Oe44Oz5Ye4", "7J287YOI", "7J287YOI6rOE", "7IS57Yq4",
		"7Jik7ZSE", "7KGw6rG0", "7IK07IOJ6rOE", "7JW87Kek", "5oCn5Lqk", "5YGa54ix",
		"5ZWq5ZWq5ZWq", "5omT54Ku", "5YaF5bCE", "5Y+j5Lqk", "6IKb5Lqk", "6Ieq5oWw",
		"5omT6aOe5py6", "6auY5r2u", "5Za35rC0", "5r2u5ZC5", "6aKc5bCE", "5Lit5Ye6",
		"6L+35aW4", "5by65aW4", "6L2u5aW4", "aW50ZXJjb3Vyc2U=", "ZnVjaw==", "Ymxvd2pvYg==",
		"Ymo=", "Y3VubmlsaW5ndXM=", "YW5hbA==", "Y3JlYW1waWU=", "b3JnYXNt", "c3F1aXJ0",
		"bWFzdHVyYmF0aW9u", "aGFuZGpvYg==", "ZmFjaWFs", "Z2FuZ2Jhbmc=", "cmFwZQ==",
		"44K744OD44Kv44K5", "5Lit5Ye644GX", "44OP44Oh5pKu44KK", "44OV44Kn44Op", "44Kv44Oz44OL",
		"44Ki44OK44Or", "44Kq44OK44OL44O8", "5omL44Kz44Kt", "5pys55Wq", "5r2u5ZC544GN",
		"44OR44Kk44K644Oq", "6aiO5LmX5L2N", "44Os44Kk44OX", "7IS57Iqk", "7ISx6rSA6rOE",
		"7KeI64K07IKs7KCV", "7Jik656E", "7Y6g6528", "7L+g64uI", "7JWg64SQ", "7J6Q7JyE",
		"65S465S47J20", "7Jik64KY64uI", "7KCI7KCV", "67aE7IiY", "7Ja87Iu4", "7YyM7J207KaI66as",
		"6rCV6rCE", "6Zi06IyO", "6Zi06YGT", "6Zi06JKC", "552+5Li4", "6bih5be0",
		"5bGM", "5bGE", "5Lmz5oi/", "5aW25aS0", "5Lmz5aS0", "5beo5Lmz",
		"54iG5Lmz", "6Zyy54K5", "6Zyy6Zi0", "cGVuaXM=", "dmFnaW5h", "Y2xpdG9yaXM=",
		"ZGljaw==", "Y29jaw==", "Y3VudA==", "Ym9vYnM=", "dGl0cw==", "bmlwcGxl",
		"YnJlYXN0cw==", "Ym9vdHk=", "6Zmw6IyO", "44Gh44KT44GT", "44GK44Gh44KT44Gh44KT",
		"6Zmw5qC4", "44Kv44Oq44OI44Oq44K5", "6Iaj", "44G+44KT44GT", "44GK44G+44KT44GT",
		"44GK44Gj44Gx44GE", "5Lmz6aaW", "44Ot44O844Ki44Oz44Kw44Or", "7J2M6rK9", "7J6Q7KeA",
		"6rys7LaU", "7J2M7Iic", "67O07KeA", "7JS5", "7YG066as", "7KCW6ryt7KeA",
		"7Jyg65GQ", "6rGw7Jyg", "7Y+t7Jyg", "6LCD5pWZ", "5oCn5aW0", "57u/5bi9",
		"5Li75LuG", "5o2G57uR", "6IKJ5L6/5Zmo", "5Y6f56WeaA==", "5ZCM5Lq6aA==", "6YeM55Wq",
		"YmRzbQ==", "Ym9uZGFnZQ==", "ZmV0aXNo", "c3VibWlzc2l2ZQ==", "ZG9taW5hbnQ=", "Y3Vja29sZA==",
		"ZXJvdGlj", "bnNmd2FydA==", "aGVudGFpYXJ0", "6Kq/5pWZ", "5aW06Zq3", "5a+d5Y+W44KJ44KM",
		"bnRy", "57eK57ib", "44Ko44Ot44Ki44OL44Oh", "44Ko44Ot5ryr55S7", "5ZCM5Lq66KqM",
		"44Ko44Ot44OR44Ot", "6rWs7IaN", "7IOI7JeE66eI", "6re87Lmc", "64ql7JqV", "7Y6o64+U",
		"66mc64+U", "7JW87JWg64uI", "7JW866eM7ZmU", "64+Z7J247KeA",
	})
	if cfg.SafetyCustomCategories == nil {
		cfg.SafetyCustomCategories = map[string]safetyCustomCategory{}
	}
	item := cfg.SafetyCustomCategories[id]
	changed := false
	if strings.TrimSpace(item.Label) == "" {
		item.Label = "成人扩展"
		changed = true
	}
	before := len(item.Words)
	item.Words = uniqueSafetyWords(append(item.Words, words...))
	if len(item.Words) != before {
		changed = true
	}
	cfg.SafetyCustomCategories[id] = item
	if cfg.SafetyPlatformCategories == nil {
		cfg.SafetyPlatformCategories = map[string]map[string]bool{}
	}
	if cfg.SafetyPlatformCategories["twitter"] == nil {
		cfg.SafetyPlatformCategories["twitter"] = map[string]bool{}
	}
	if !cfg.SafetyPlatformCategories["twitter"][id] {
		cfg.SafetyPlatformCategories["twitter"][id] = true
		changed = true
	}
	return changed
}

func safetyBuiltinWords(def safetyCategoryDef) []string {
	if words, ok := safetyBuiltinWordCache[def.ID]; ok {
		return words
	}
	return decodeSafetyBuiltinWords(def.KeywordsB64)
}

func buildSafetyBuiltinWordCache() map[string][]string {
	out := make(map[string][]string, len(safetyCategoryDefs))
	for _, def := range safetyCategoryDefs {
		out[def.ID] = decodeSafetyBuiltinWords(def.KeywordsB64)
	}
	return out
}

func decodeSafetyBuiltinWords(encoded []string) []string {
	words := make([]string, 0, len(encoded))
	for _, raw := range encoded {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			continue
		}
		words = append(words, strings.TrimPrefix(strings.TrimSpace(string(decoded)), "#"))
	}
	return uniqueSafetyWords(words)
}

func safetyNoticeText(cfg config, meta mediaMeta, hit safetyHit) string {
	text := strings.TrimSpace(cfg.SafetyFilterNoticeText)
	if text == "" {
		text = "内容触发安全屏蔽，已停止解析。"
	}
	replacer := strings.NewReplacer(
		"{platform}", strings.TrimSpace(meta.Platform),
		"{category}", strings.TrimSpace(hit.Category),
		"{title}", strings.TrimSpace(meta.Title),
	)
	return replacer.Replace(text)
}

func migrateSafetyCategoryID(id string) string {
	switch normalizeSafetyCategory(id) {
	case "adult", "adult_scam", "minor_risk":
		return safetyCategoryAdult
	case "ad", "illegal_url_ad":
		return safetyCategoryAd
	case "violence", "weapon_explosive":
		return safetyCategoryViolence
	case "politics", "political_sensitive":
		return safetyCategoryPolitics
	default:
		return normalizeSafetyCategory(id)
	}
}

func mergeSafetyCustomCategorySeeds(cfg *config, seeds map[string][]string) bool {
	changed := false
	if cfg.SafetyCustomCategories == nil {
		cfg.SafetyCustomCategories = map[string]safetyCustomCategory{}
	}
	for cat, words := range seeds {
		cat = migrateSafetyCategoryID(cat)
		if len(words) == 0 {
			continue
		}
		id := "custom_" + cat
		item := cfg.SafetyCustomCategories[id]
		if strings.TrimSpace(item.Label) == "" {
			item.Label = "自定义-" + safetyCategoryLabel(cat)
			changed = true
		}
		before := len(item.Words)
		item.Words = uniqueSafetyWords(append(item.Words, filterMigratedSafetyWords(words)...))
		if len(item.Words) != before {
			changed = true
		}
		cfg.SafetyCustomCategories[id] = item
		if cfg.SafetyGlobalCategories == nil {
			cfg.SafetyGlobalCategories = map[string]bool{}
		}
		if !cfg.SafetyGlobalCategories[id] {
			cfg.SafetyGlobalCategories[id] = true
			changed = true
		}
	}
	return changed
}

func mergeSafetyCustomWords(dst, src map[string][]string) map[string][]string {
	if dst == nil {
		dst = map[string][]string{}
	}
	for cat, words := range src {
		dst[cat] = uniqueSafetyWords(append(dst[cat], words...))
	}
	return dst
}

func mergeSafetyCustomPlatformWords(dst, src map[string]map[string][]string) map[string]map[string][]string {
	if dst == nil {
		dst = map[string]map[string][]string{}
	}
	for platform, cats := range src {
		if dst[platform] == nil {
			dst[platform] = map[string][]string{}
		}
		dst[platform] = mergeSafetyCustomWords(dst[platform], cats)
	}
	return dst
}

func cloneSafetyCustomMap(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for cat, words := range in {
		out[cat] = append([]string(nil), words...)
	}
	return out
}

func migrateLegacySafetyCustomWords(cfg *config) bool {
	legacyCustomGlobal, keptCustomGlobal := splitLegacyBuiltinWordMap(cfg.SafetyCustomGlobal)
	legacyExcludeGlobal, keptExcludeGlobal := splitLegacyBuiltinWordMap(cfg.SafetyExcludeGlobal)
	changed := mergeSafetyCustomCategorySeeds(cfg, legacyCustomGlobal)
	for cat, words := range legacyExcludeGlobal {
		cat = migrateSafetyCategoryID(cat)
		if len(words) == 0 {
			continue
		}
		id := "custom_" + normalizeSafetyCategory(cat)
		item := cfg.SafetyCustomCategories[id]
		if strings.TrimSpace(item.Label) == "" {
			item.Label = "自定义-" + safetyCategoryLabel(cat)
			changed = true
		}
		before := len(item.Excludes)
		item.Excludes = uniqueSafetyWords(append(item.Excludes, filterMigratedSafetyWords(words)...))
		if len(item.Excludes) != before {
			changed = true
		}
		cfg.SafetyCustomCategories[id] = item
		if len(item.Excludes) > 0 || len(item.Words) > 0 {
			if !cfg.SafetyGlobalCategories[id] {
				cfg.SafetyGlobalCategories[id] = true
				changed = true
			}
		}
	}
	if migrateSafetyPlatformWords(cfg, cfg.SafetyCustomPlatform) {
		changed = true
	}
	for platform, cats := range cfg.SafetyExcludePlatform {
		name := normalizePlatformName(platform)
		if name == "" {
			continue
		}
		if cfg.SafetyPlatformCategories[name] == nil {
			cfg.SafetyPlatformCategories[name] = map[string]bool{}
		}
		for cat, words := range cats {
			cat = migrateSafetyCategoryID(cat)
			if len(words) == 0 {
				continue
			}
			id := "custom_" + normalizeSafetyCategory(cat)
			item := cfg.SafetyCustomCategories[id]
			if strings.TrimSpace(item.Label) == "" {
				item.Label = "自定义-" + safetyCategoryLabel(cat)
				changed = true
			}
			before := len(item.Excludes)
			item.Excludes = uniqueSafetyWords(append(item.Excludes, filterMigratedSafetyWords(words)...))
			if len(item.Excludes) != before {
				changed = true
			}
			cfg.SafetyCustomCategories[id] = item
			if len(item.Excludes) > 0 || len(item.Words) > 0 {
				if !cfg.SafetyGlobalCategories[id] {
					cfg.SafetyGlobalCategories[id] = true
					changed = true
				}
			}
		}
	}
	if len(legacyCustomGlobal) > 0 || len(cfg.SafetyCustomPlatform) > 0 || len(legacyExcludeGlobal) > 0 || len(cfg.SafetyExcludePlatform) > 0 {
		cfg.SafetyCustomGlobal = keptCustomGlobal
		cfg.SafetyCustomPlatform = map[string]map[string][]string{}
		cfg.SafetyExcludeGlobal = keptExcludeGlobal
		cfg.SafetyExcludePlatform = map[string]map[string][]string{}
		changed = true
	}
	return changed
}

func splitLegacyBuiltinWordMap(in map[string][]string) (legacy map[string][]string, kept map[string][]string) {
	legacy = map[string][]string{}
	kept = map[string][]string{}
	for cat, words := range in {
		raw := normalizeSafetyCategory(cat)
		migrated := migrateSafetyCategoryID(raw)
		if raw != "" && raw == migrated && validSafetyCategory(raw) {
			kept[raw] = uniqueSafetyWords(words)
			continue
		}
		legacy[cat] = words
	}
	return legacy, kept
}

func migrateSafetyPlatformWords(cfg *config, src map[string]map[string][]string) bool {
	changed := false
	if cfg.SafetyCustomCategories == nil {
		cfg.SafetyCustomCategories = map[string]safetyCustomCategory{}
	}
	if cfg.SafetyPlatformCategories == nil {
		cfg.SafetyPlatformCategories = map[string]map[string]bool{}
	}
	for platform, cats := range src {
		name := normalizePlatformName(platform)
		if name == "" {
			continue
		}
		for cat, words := range cats {
			cat = migrateSafetyCategoryID(cat)
			if len(words) == 0 {
				continue
			}
			id := "custom_" + normalizeSafetyCategory(cat)
			item := cfg.SafetyCustomCategories[id]
			if strings.TrimSpace(item.Label) == "" {
				item.Label = "自定义-" + safetyCategoryLabel(cat)
				changed = true
			}
			before := len(item.Words)
			item.Words = uniqueSafetyWords(append(item.Words, filterMigratedSafetyWords(words)...))
			if len(item.Words) != before {
				changed = true
			}
			cfg.SafetyCustomCategories[id] = item
			if len(item.Words) > 0 {
				if cfg.SafetyGlobalCategories == nil {
					cfg.SafetyGlobalCategories = map[string]bool{}
				}
				if !cfg.SafetyGlobalCategories[id] {
					cfg.SafetyGlobalCategories[id] = true
					changed = true
				}
			}
		}
	}
	return changed
}

func filterMigratedSafetyWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, word := range words {
		switch normalizeSafetyText(word) {
		case "t co":
			continue
		default:
			out = append(out, word)
		}
	}
	return out
}

func platformDisplayName(name string) string {
	for _, p := range platforms {
		if p.Name == name {
			if len(p.Aliases) > 0 && p.Aliases[0] != "" {
				return p.Aliases[0]
			}
			return p.Name
		}
	}
	return name
}

func builtinSafetyCategoryIDs() []string {
	ids := make([]string, 0, len(safetyCategoryDefs))
	for _, def := range safetyCategoryDefs {
		ids = append(ids, def.ID)
	}
	return ids
}

func validSafetyCategory(id string) bool {
	id = migrateSafetyCategoryID(id)
	for _, def := range safetyCategoryDefs {
		if def.ID == id {
			return true
		}
	}
	return false
}

func safetyCategoryLabel(id string) string {
	id = migrateSafetyCategoryID(id)
	for _, def := range safetyCategoryDefs {
		if def.ID == id {
			return def.Label
		}
	}
	return id
}

func normalizeSafetyCategory(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizeCustomSafetyCategoryID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastSep := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSep = false
			continue
		}
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			if !lastSep && b.Len() > 0 {
				b.WriteRune('_')
				lastSep = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func normalizeSafetyCustomCategories(in map[string]safetyCustomCategory) map[string]safetyCustomCategory {
	out := map[string]safetyCustomCategory{}
	for id, item := range in {
		id = normalizeCustomSafetyCategoryID(id)
		if id == "" || validSafetyCategory(id) {
			continue
		}
		legacy := false
		if target, ok := legacyCustomSafetyCategoryID(id); ok {
			id = target
			legacy = true
		}
		item.Label = cleanSafetyCustomCategoryLabel(id, item.Label)
		if item.Label == "" {
			item.Label = id
		}
		if legacy {
			item.Words = uniqueSafetyWords(filterMigratedSafetyWords(item.Words))
			item.Excludes = uniqueSafetyWords(filterMigratedSafetyWords(item.Excludes))
		} else {
			item.Words = uniqueSafetyWords(item.Words)
			item.Excludes = uniqueSafetyWords(item.Excludes)
		}
		if existing, ok := out[id]; ok {
			if strings.TrimSpace(existing.Label) == "" || strings.HasPrefix(existing.Label, "自定义-") {
				existing.Label = item.Label
			}
			existing.Words = uniqueSafetyWords(append(existing.Words, item.Words...))
			existing.Excludes = uniqueSafetyWords(append(existing.Excludes, item.Excludes...))
			out[id] = existing
			continue
		}
		out[id] = item
	}
	return out
}

func cleanSafetyCustomCategoryLabel(id, label string) string {
	label = strings.TrimSpace(label)
	switch normalizeCustomSafetyCategoryID(id) {
	case "custom_adult":
		return "自定义-色情"
	case "custom_ad":
		return "自定义-广告"
	case "custom_violence":
		return "自定义-暴恐"
	case "custom_politics":
		return "自定义-政治"
	}
	for _, prefix := range []string{"Instagram ", "TikTok ", "Twitter ", "YouTube ", "X ", "tk "} {
		label = strings.TrimPrefix(label, prefix)
	}
	return strings.TrimSpace(label)
}

func migrateSafetyDefaults(cfg *config) bool {
	if cfg.SafetyDefaultVersion >= currentSafetyDefaultVersion {
		return false
	}
	cfg.SafetyGlobalCategories = defaultSafetyGlobalCategories()
	cfg.SafetyPlatformCategories = defaultSafetyPlatformCategories()
	cfg.SafetyDefaultVersion = currentSafetyDefaultVersion
	return true
}

func migrateSafetyTwitterSensitiveDefault(cfg *config) bool {
	if cfg.SafetyTwitterVersion >= currentSafetyTwitterVersion {
		return false
	}
	cfg.SafetyTwitterSensitive = true
	cfg.SafetyTwitterVersion = currentSafetyTwitterVersion
	return true
}

func legacyCustomSafetyCategoryID(id string) (string, bool) {
	id = normalizeCustomSafetyCategoryID(id)
	if !strings.HasPrefix(id, "custom_") {
		return "", false
	}
	rest := strings.TrimPrefix(id, "custom_")
	for _, legacy := range []string{
		"political_sensitive",
		"illegal_url_ad",
		"weapon_explosive",
		"adult_scam",
		"minor_risk",
		"politics",
		"violence",
		"adult",
		"ad",
	} {
		if rest == legacy || strings.HasSuffix(rest, "_"+legacy) {
			return "custom_" + migrateSafetyCategoryID(legacy), true
		}
	}
	return "", false
}

func normalizeSafetyMap(in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range in {
		if target, ok := legacyCustomSafetyCategoryID(k); ok {
			k = target
		} else {
			k = migrateSafetyCategoryID(k)
		}
		if validSafetyCategory(k) || k != "" {
			out[k] = out[k] || v
		}
	}
	return out
}

func normalizeSafetyPlatformCategories(in map[string]map[string]bool) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for platform, cats := range in {
		name := normalizePlatformName(platform)
		if name == "" {
			continue
		}
		out[name] = normalizeSafetyMap(cats)
	}
	return out
}

func normalizeSafetyCustom(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for cat, words := range in {
		cat = migrateSafetyCategoryID(cat)
		if !validSafetyCategory(cat) {
			continue
		}
		out[cat] = uniqueSafetyWords(words)
	}
	return out
}

func normalizeSafetyCustomPlatform(in map[string]map[string][]string) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for platform, cats := range in {
		name := normalizePlatformName(platform)
		if name == "" {
			continue
		}
		out[name] = normalizeSafetyCustom(cats)
	}
	return out
}

func uniqueSafetyWords(words []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, word := range words {
		word = cleanSafetyWord(word)
		if word == "" {
			continue
		}
		key := safetyWordKey(word)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, word)
	}
	sort.Slice(out, func(i, j int) bool {
		return safetyWordKey(out[i]) < safetyWordKey(out[j])
	})
	return out
}

func cleanSafetyWord(word string) string {
	return strings.TrimLeft(strings.TrimSpace(word), "#")
}

func safetyBlocked(cfg config, meta mediaMeta, raw string) (safetyHit, bool) {
	if !cfg.SafetyFilterEnabled {
		return safetyHit{}, false
	}
	if safetyTwitterSensitiveBlocked(cfg, meta) {
		return safetyHit{Category: safetyCategoryAdult, Keyword: safetyMarkerTwitterSensitive, Source: "platform_sensitive"}, true
	}
	text := safetyScanText(meta, raw)
	if strings.TrimSpace(text) == "" {
		return safetyHit{}, false
	}
	normalized := normalizeSafetyText(text)
	if normalized == "" {
		return safetyHit{}, false
	}
	platform := normalizePlatformName(meta.Platform)
	if platform == "" {
		platform = normalizePlatformName(meta.SourceURL)
	}
	categories := activeSafetyCategories(cfg, platform)
	for _, def := range safetyCategoryDefs {
		if !categories[def.ID] {
			continue
		}
		for _, word := range safetyBuiltinWords(def) {
			if safetyTextContains(normalized, word) {
				if safetyExcluded(cfg, platform, def.ID, normalized) {
					continue
				}
				return safetyHit{Category: def.ID, Keyword: word, Source: "builtin"}, true
			}
		}
		for _, word := range cfg.SafetyCustomGlobal[def.ID] {
			if safetyTextContains(normalized, word) {
				if safetyExcluded(cfg, platform, def.ID, normalized) {
					continue
				}
				return safetyHit{Category: def.ID, Keyword: word, Source: "builtin_custom"}, true
			}
		}
	}
	for id, item := range cfg.SafetyCustomCategories {
		id = normalizeSafetyCategory(id)
		if !categories[id] {
			continue
		}
		for _, word := range item.Words {
			if safetyTextContains(normalized, word) {
				if safetyWordsContain(normalized, item.Excludes) {
					continue
				}
				return safetyHit{Category: id, Keyword: word, Source: "custom_category"}, true
			}
		}
	}
	return safetyHit{}, false
}

func safetyTwitterSensitiveBlocked(cfg config, meta mediaMeta) bool {
	if !cfg.SafetyTwitterSensitive {
		return false
	}
	platform := normalizePlatformName(meta.Platform)
	if platform == "" {
		platform = normalizePlatformName(meta.SourceURL)
	}
	if platform != "twitter" {
		return false
	}
	return strings.Contains(meta.AccessText, safetyMarkerTwitterSensitive)
}

func safetyExcluded(cfg config, platform, category, normalizedText string) bool {
	if safetyWordsContain(normalizedText, cfg.SafetyExcludeGlobal[category]) {
		return true
	}
	if platformExcludes := cfg.SafetyExcludePlatform[platform]; platformExcludes != nil {
		return safetyWordsContain(normalizedText, platformExcludes[category])
	}
	return false
}

func safetyWordsContain(normalizedText string, words []string) bool {
	for _, word := range words {
		if safetyTextContains(normalizedText, word) {
			return true
		}
	}
	return false
}

func activeSafetyCategories(cfg config, platform string) map[string]bool {
	out := map[string]bool{}
	for cat, on := range cfg.SafetyGlobalCategories {
		if on {
			out[normalizeSafetyCategory(cat)] = true
		}
	}
	if platformCats := cfg.SafetyPlatformCategories[platform]; platformCats != nil {
		for cat, on := range platformCats {
			if on {
				out[normalizeSafetyCategory(cat)] = true
			} else {
				delete(out, normalizeSafetyCategory(cat))
			}
		}
	}
	return out
}

func safetyScanText(meta mediaMeta, raw string) string {
	parts := []string{raw, meta.Title, meta.Desc, meta.Author, meta.AccessText}
	return strings.Join(parts, "\n")
}

func safetyTextContains(normalizedText, word string) bool {
	word = cleanSafetyWord(word)
	if word == "" {
		return false
	}
	if pattern, ok := safetyRegexPattern(word); ok {
		re, err := regexp.Compile("(?i)" + pattern)
		return err == nil && re.MatchString(normalizedText)
	}
	if safetyWildcardWord(word) {
		re, err := regexp.Compile("(?i)" + safetyWildcardPattern(word))
		return err == nil && re.MatchString(normalizedText)
	}
	word = normalizeSafetyText(word)
	return word != "" && strings.Contains(normalizedText, word)
}

func safetyWordKey(word string) string {
	word = cleanSafetyWord(word)
	if word == "" {
		return ""
	}
	if _, ok := safetyRegexPattern(word); ok || safetyWildcardWord(word) {
		return strings.ToLower(word)
	}
	return normalizeSafetyText(word)
}

func safetyRegexPattern(word string) (string, bool) {
	word = strings.TrimSpace(word)
	switch {
	case strings.HasPrefix(word, "re:"):
		return strings.TrimSpace(strings.TrimPrefix(word, "re:")), true
	case strings.HasPrefix(word, "regexp:"):
		return strings.TrimSpace(strings.TrimPrefix(word, "regexp:")), true
	default:
		return "", false
	}
}

func safetyWildcardWord(word string) bool {
	if _, ok := safetyRegexPattern(word); ok {
		return false
	}
	return strings.ContainsAny(word, "*?")
}

func safetyWildcardPattern(word string) string {
	word = strings.ToLower(strings.TrimSpace(word))
	var b strings.Builder
	lastSpace := false
	for _, r := range word {
		switch {
		case r == '*':
			b.WriteString(".*")
			lastSpace = false
		case r == '?':
			b.WriteString(".")
			lastSpace = false
		case r == '+' || r == '-' || r == '_' || r == '#' || r == '@':
			b.WriteString(regexp.QuoteMeta(string(r)))
			lastSpace = false
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
			if !lastSpace {
				b.WriteString(`\s+`)
				lastSpace = true
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
			lastSpace = false
		}
	}
	return b.String()
}

func normalizeSafetyText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if r == '+' || r == '-' || r == '_' || r == '#' || r == '@' {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return strings.TrimSpace(b.String())
}

func safetyKeywordDigest(keyword string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(keyword))))
	return hex.EncodeToString(sum[:])[:10]
}

func safetyStatusText(cfg config) string {
	lines := []string{fmt.Sprintf("内容安全: %s", onOffText(cfg.SafetyFilterEnabled))}
	lines = append(lines, fmt.Sprintf("命中提示: %s", onOffText(cfg.SafetyFilterNotice)))
	lines = append(lines, "全局分类: "+strings.Join(enabledSafetyCategories(cfg.SafetyGlobalCategories), ", "))
	for _, p := range platforms {
		if cats := enabledSafetyCategories(cfg.SafetyPlatformCategories[p.Name]); len(cats) > 0 {
			lines = append(lines, fmt.Sprintf("%s: %s", p.Name, strings.Join(cats, ", ")))
		}
	}
	return strings.Join(lines, "\n")
}

func enabledSafetyCategories(cats map[string]bool) []string {
	out := []string{}
	for _, id := range builtinSafetyCategoryIDs() {
		if cats[id] {
			out = append(out, id)
		}
	}
	custom := []string{}
	for id, on := range cats {
		if on && !validSafetyCategory(id) {
			custom = append(custom, id)
		}
	}
	sort.Strings(custom)
	out = append(out, custom...)
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

func onOffText(on bool) string {
	if on {
		return "开启"
	}
	return "关闭"
}

func handleSafetyCommand(ctx *zero.Ctx, args []string) bool {
	if len(args) == 0 || args[0] == "状态" || args[0] == "status" {
		ctx.SendChain(message.Text(safetyStatusText(currentConf)))
		return false
	}
	switch args[0] {
	case "开启", "on", "enable":
		currentConf.SafetyFilterEnabled = true
		return true
	case "关闭", "off", "disable":
		currentConf.SafetyFilterEnabled = false
		return true
	case "提示", "notice":
		if len(args) < 2 {
			ctx.SendChain(message.Text("usage: /媒体解析 safety notice on|off"))
			return false
		}
		currentConf.SafetyFilterNotice = isOn(args[1])
		return true
	case "分类", "category":
		if len(args) < 3 {
			ctx.SendChain(message.Text("usage: /媒体解析 safety category adult on|off"))
			return false
		}
		cat := normalizeSafetyCategory(args[1])
		if !validSafetyCategory(cat) {
			ctx.SendChain(message.Text("未知安全分类: ", args[1]))
			return false
		}
		if currentConf.SafetyGlobalCategories == nil {
			currentConf.SafetyGlobalCategories = map[string]bool{}
		}
		currentConf.SafetyGlobalCategories[cat] = isOn(args[2])
		return true
	case "平台", "platform":
		return handleSafetyPlatformCommand(ctx, args[1:])
	case "全局", "global":
		return handleSafetyWordsCommand(ctx, "", args[1:])
	case "排除", "exclude", "allow":
		return handleSafetyExcludeCommand(ctx, "", args[1:])
	default:
		ctx.SendChain(message.Text("usage: /媒体解析 safety status|on|off|notice|category|platform|global"))
		return false
	}
}

func handleSafetyPlatformCommand(ctx *zero.Ctx, args []string) bool {
	if len(args) < 1 {
		ctx.SendChain(message.Text("usage: /媒体解析 safety platform twitter category ad on|off"))
		return false
	}
	platform := normalizePlatformName(args[0])
	if platform == "" {
		ctx.SendChain(message.Text("未知平台: ", args[0]))
		return false
	}
	if len(args) >= 4 && (args[1] == "分类" || args[1] == "category") {
		cat := normalizeSafetyCategory(args[2])
		if !validSafetyCategory(cat) {
			ctx.SendChain(message.Text("未知安全分类: ", args[2]))
			return false
		}
		if currentConf.SafetyPlatformCategories == nil {
			currentConf.SafetyPlatformCategories = map[string]map[string]bool{}
		}
		if currentConf.SafetyPlatformCategories[platform] == nil {
			currentConf.SafetyPlatformCategories[platform] = map[string]bool{}
		}
		currentConf.SafetyPlatformCategories[platform][cat] = isOn(args[3])
		return true
	}
	if len(args) >= 2 && (args[1] == "排除" || args[1] == "exclude" || args[1] == "allow") {
		return handleSafetyExcludeCommand(ctx, platform, args[2:])
	}
	return handleSafetyWordsCommand(ctx, platform, args[1:])
}

func handleSafetyWordsCommand(ctx *zero.Ctx, platform string, args []string) bool {
	if len(args) < 3 {
		if platform == "" {
			ctx.SendChain(message.Text("usage: /媒体解析 safety global adult add|del word"))
		} else {
			ctx.SendChain(message.Text("usage: /媒体解析 safety platform twitter category ad on|off；自定义词请用 global"))
		}
		return false
	}
	cat := migrateSafetyCategoryID(args[0])
	if !validSafetyCategory(cat) {
		ctx.SendChain(message.Text("未知安全分类: ", args[0]))
		return false
	}
	action := strings.ToLower(strings.TrimSpace(args[1]))
	word := strings.TrimSpace(strings.Join(args[2:], " "))
	if word == "" {
		ctx.SendChain(message.Text("屏蔽词不能为空"))
		return false
	}
	if currentConf.SafetyCustomCategories == nil {
		currentConf.SafetyCustomCategories = map[string]safetyCustomCategory{}
	}
	if currentConf.SafetyGlobalCategories == nil {
		currentConf.SafetyGlobalCategories = map[string]bool{}
	}
	id := "custom_" + cat
	item := currentConf.SafetyCustomCategories[id]
	if strings.TrimSpace(item.Label) == "" {
		item.Label = "自定义-" + safetyCategoryLabel(cat)
	}
	item.Words = updateSafetyWordList(item.Words, action, word)
	currentConf.SafetyCustomCategories[id] = item
	currentConf.SafetyGlobalCategories[id] = true
	return true
}

func updateSafetyWordList(words []string, action, word string) []string {
	normalized := normalizeSafetyText(word)
	out := []string{}
	for _, existing := range words {
		if normalizeSafetyText(existing) == normalized {
			continue
		}
		out = append(out, existing)
	}
	switch action {
	case "删除", "del", "delete", "remove":
		return uniqueSafetyWords(out)
	default:
		out = append(out, word)
		return uniqueSafetyWords(out)
	}
}

func handleSafetyExcludeCommand(ctx *zero.Ctx, platform string, args []string) bool {
	if len(args) < 3 {
		if platform == "" {
			ctx.SendChain(message.Text("usage: /濯掍綋瑙ｆ瀽 safety exclude adult add|del word"))
		} else {
			ctx.SendChain(message.Text("usage: /濯掍綋瑙ｆ瀽 safety platform twitter exclude adult add|del word"))
		}
		return false
	}
	cat := normalizeSafetyCategory(args[0])
	if !validSafetyCategory(cat) {
		ctx.SendChain(message.Text("鏈煡瀹夊叏鍒嗙被: ", args[0]))
		return false
	}
	action := strings.ToLower(strings.TrimSpace(args[1]))
	word := strings.TrimSpace(strings.Join(args[2:], " "))
	if word == "" {
		ctx.SendChain(message.Text("exclude word cannot be empty"))
		return false
	}
	if platform == "" {
		if currentConf.SafetyExcludeGlobal == nil {
			currentConf.SafetyExcludeGlobal = map[string][]string{}
		}
		currentConf.SafetyExcludeGlobal[cat] = updateSafetyWordList(currentConf.SafetyExcludeGlobal[cat], action, word)
		return true
	}
	if currentConf.SafetyExcludePlatform == nil {
		currentConf.SafetyExcludePlatform = map[string]map[string][]string{}
	}
	if currentConf.SafetyExcludePlatform[platform] == nil {
		currentConf.SafetyExcludePlatform[platform] = map[string][]string{}
	}
	currentConf.SafetyExcludePlatform[platform][cat] = updateSafetyWordList(currentConf.SafetyExcludePlatform[platform][cat], action, word)
	return true
}
