package mediaparser

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	safetyCategoryAdult           = "adult"
	safetyCategoryAdultScam       = "adult_scam"
	safetyCategoryMinor           = "minor_risk"
	safetyCategoryViolence        = "violence"
	safetyCategoryWeaponExplosive = "weapon_explosive"
	safetyCategoryIllegalURLAd    = "illegal_url_ad"

	currentSafetyCustomSeedVersion = 2
)

type safetyHit struct {
	Category string
	Keyword  string
	Source   string
}

type safetyCategoryDef struct {
	ID       string
	Label    string
	Keywords []string
}

type safetyCustomCategory struct {
	Label    string   `json:"label"`
	Words    []string `json:"words"`
	Excludes []string `json:"excludes"`
}

var safetyCategoryDefs = []safetyCategoryDef{
	{
		ID:    safetyCategoryAdult,
		Label: "色情/NSFW/R18",
		Keywords: []string{
			"nsfw", "r18", "r-18", "18+", "adult content", "explicit", "porn", "xxx",
			"nude", "nudity", "lewd", "hentai", "ecchi", "onlyfans", "fansly",
			"色情", "成人内容", "露骨", "黄图", "黄推", "福利姬", "擦边", "成人向",
			"私房", "裸聊", "约炮", "成人视频", "成人网站", "成人论坛",
			"成人向け", "エロ", "えろ", "ヘンタイ", "変態", "ヌード", "センシティブ",
			"성인", "성인물", "19금", "야짤", "후방주의", "음란", "노출", "누드",
		},
	},
	{
		ID:    safetyCategoryAdultScam,
		Label: "黄推诈骗/导流",
		Keywords: []string{
			"黄推", "诈骗黄推", "外围", "上门服务", "同城约", "裸聊诈骗", "激情视频",
			"成人视频群", "福利视频群", "telegram福利", "tg福利", "引流", "私信发资源",
			"约拍私房", "成人交友", "付费私聊", "unlock content", "premium snap",
			"link in bio", "dm for menu", "adult telegram", "sex work",
		},
	},
	{
		ID:    safetyCategoryMinor,
		Label: "未成年高风险成人词",
		Keywords: []string{
			"未成年色情", "儿童色情", "幼女", "萝莉", "炼铜", "幼态成人",
			"ロリ", "ロリコン", "児童ポルノ", "未成年者",
			"미성년", "아동", "로리", "미성년자",
			"child porn", "cp porn", "loli", "lolicon", "underage nude",
		},
	},
	{
		ID:    safetyCategoryViolence,
		Label: "极端暴力/血腥",
		Keywords: []string{
			"极端暴力", "血腥", "虐杀", "斩首", "分尸", "肢解", "处决视频", "自残直播",
			"gore", "graphic violence", "beheading", "dismemberment", "execution video",
			"グロ", "残虐", "流血", "斬首",
			"고어", "잔혹", "참수", "유혈",
		},
	},
	{
		ID:    safetyCategoryWeaponExplosive,
		Label: "涉枪涉爆",
		Keywords: []string{
			"枪支交易", "买枪", "卖枪", "炸药配方", "爆炸物制作", "自制炸弹", "管制刀具交易",
			"buy gun", "ghost gun", "homemade explosive", "bomb making", "ied tutorial",
			"銃販売", "爆薬作成", "爆弾作成",
			"총기거래", "폭탄제조", "폭발물제조",
		},
	},
	{
		ID:    safetyCategoryIllegalURLAd,
		Label: "非法网址/广告导流",
		Keywords: []string{
			"偷拍视频", "成人视频网址", "色情网盘", "非法网址", "博彩平台", "网赌", "澳门现金网",
			"最新地址", "防走失", "备用域名", "跳转链接", "成人导航", "看片网址",
			"crypto giveaway", "airdrop scam", "free onlyfans leak", "leaked nudes",
			"出会い系", "裏垢女子", "無修正リンク",
			"성인사이트", "불법토토", "도박사이트",
		},
	},
}

func defaultSafetyPlatformCategories() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range []string{"twitter", "instagram", "tiktok", "youtube"} {
		out[p] = map[string]bool{
			safetyCategoryAdultScam:    true,
			safetyCategoryIllegalURLAd: true,
		}
	}
	return out
}

func defaultSafetyGlobalCategories() map[string]bool {
	return map[string]bool{
		safetyCategoryAdult:    true,
		safetyCategoryMinor:    true,
		safetyCategoryViolence: true,
	}
}

func defaultSafetyCustomGlobalSeeds() map[string][]string {
	return map[string][]string{
		safetyCategoryAdult: uniqueSafetyWords([]string{
			"AV", "Adult", "sex", "intercourse", "fuck", "blowjob", "bj", "cunnilingus", "anal", "creampie",
			"cum", "orgasm", "squirt", "masturbation", "handjob", "facial", "gangbang", "rape",
			"penis", "vagina", "clitoris", "dick", "cock", "pussy", "cunt", "boobs", "tits", "nipple", "breasts", "booty",
			"bdsm", "bondage", "fetish", "submissive", "dominant", "cuckold", "erotic", "milf", "dilf", "nsfwart", "hentaiart",
			"福利姬", "外围", "国产乱伦", "探花", "裸聊", "色图", "黄片", "番号",
			"性交", "做爱", "啪啪", "打炮", "内射", "口交", "肛交", "自慰", "打飞机", "高潮", "潮吹", "颜射", "中出", "迷奸", "强奸", "轮奸",
			"阴茎", "阴道", "阴蒂", "睾丸", "鸡巴", "乳房", "奶头", "乳头", "巨乳", "爆乳", "露点", "露阴",
			"调教", "性奴", "绿帽", "主仆", "捆绑", "肉便器", "里番", "同人h",
			"裏垢", "裏垢女子", "裏垢男子", "オナペ", "パイ凸", "マン凸",
			"セックス", "中出し", "ハメ撮り", "フェラ", "クンニ", "アナル", "オナニー", "手コキ", "本番", "潮吹き", "パイズリ", "騎乗位", "レイプ",
			"ちんこ", "おちんちん", "クリトリス", "まんこ", "おまんこ", "おっぱい", "乳首", "巨乳", "爆乳",
			"調教", "奴隷", "寝取られ", "NTR", "緊縛", "エロアニメ", "エロ漫画", "同人誌", "エロパロ",
			"일탈", "일탈계", "섹트", "야짤", "섹스", "성관계", "질내사정", "오랄", "펠라", "쿠니", "애널", "자위", "딸딸이", "오나니", "절정", "분수", "얼싸", "파이즈리", "강간",
			"음경", "자지", "꼬추", "보지", "클리", "젖꼭지", "유두", "거유", "폭유", "야애니", "야만화", "동인지", "한국야동",
		}),
	}
}

func defaultSafetyCustomPlatformSeeds() map[string]map[string][]string {
	xSeeds := map[string][]string{
		safetyCategoryAdultScam: uniqueSafetyWords([]string{
			"onlyfans.com", "fansly.com", "linktr.ee", "t.co", "telegram", "telegram福利", "tg福利",
			"付费私聊", "私信发资源", "约拍私房", "成人交友", "unlock content", "premium snap", "dm for menu", "link in bio",
			"腹肌", "薄肌", "薄肌男", "男菩萨", "女菩萨", "男菩薩", "女菩薩",
			"#腹肌", "#薄肌", "#nsfw", "#NSFW", "#男菩萨", "#女菩萨", "#男菩薩", "#女菩薩",
			" 오프", " 조건",
		}),
		safetyCategoryIllegalURLAd: uniqueSafetyWords([]string{
			"onlyfans.com", "fansly.com", "linktr.ee", "free onlyfans leak", "leaked nudes", "crypto giveaway", "airdrop scam",
		}),
		"political_sensitive": uniqueSafetyWords([]string{
			"习近平", "中共", "共产党", "六四", "天安门事件", "台独", "港独", "藏独", "疆独", "法轮功",
			"xi jinping", "ccp", "communist party of china", "tiananmen", "june 4th", "free tibet", "free hong kong",
		}),
	}
	out := map[string]map[string][]string{}
	for _, platform := range []string{"twitter", "instagram", "tiktok", "youtube"} {
		out[platform] = cloneSafetyCustomMap(xSeeds)
	}
	return out
}

func seedSafetyCustomWords(cfg *config) bool {
	changed := mergeSafetyCustomCategorySeeds(cfg, defaultSafetyCustomGlobalSeeds())
	return migrateSafetyPlatformWords(cfg, defaultSafetyCustomPlatformSeeds()) || changed
}

func mergeSafetyCustomCategorySeeds(cfg *config, seeds map[string][]string) bool {
	changed := false
	if cfg.SafetyCustomCategories == nil {
		cfg.SafetyCustomCategories = map[string]safetyCustomCategory{}
	}
	for cat, words := range seeds {
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
		item.Words = uniqueSafetyWords(append(item.Words, words...))
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
	changed := mergeSafetyCustomCategorySeeds(cfg, cfg.SafetyCustomGlobal)
	for cat, words := range cfg.SafetyExcludeGlobal {
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
		item.Excludes = uniqueSafetyWords(append(item.Excludes, words...))
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
			if len(words) == 0 {
				continue
			}
			id := "custom_" + name + "_" + normalizeSafetyCategory(cat)
			item := cfg.SafetyCustomCategories[id]
			if strings.TrimSpace(item.Label) == "" {
				item.Label = platformDisplayName(name) + " 自定义-" + safetyCategoryLabel(cat)
				changed = true
			}
			before := len(item.Excludes)
			item.Excludes = uniqueSafetyWords(append(item.Excludes, words...))
			if len(item.Excludes) != before {
				changed = true
			}
			cfg.SafetyCustomCategories[id] = item
			if len(item.Excludes) > 0 || len(item.Words) > 0 {
				if !cfg.SafetyPlatformCategories[name][id] {
					cfg.SafetyPlatformCategories[name][id] = true
					changed = true
				}
			}
		}
	}
	if len(cfg.SafetyCustomGlobal) > 0 || len(cfg.SafetyCustomPlatform) > 0 || len(cfg.SafetyExcludeGlobal) > 0 || len(cfg.SafetyExcludePlatform) > 0 {
		cfg.SafetyCustomGlobal = map[string][]string{}
		cfg.SafetyCustomPlatform = map[string]map[string][]string{}
		cfg.SafetyExcludeGlobal = map[string][]string{}
		cfg.SafetyExcludePlatform = map[string]map[string][]string{}
		changed = true
	}
	return changed
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
		if cfg.SafetyPlatformCategories[name] == nil {
			cfg.SafetyPlatformCategories[name] = map[string]bool{}
		}
		for cat, words := range cats {
			if len(words) == 0 {
				continue
			}
			id := "custom_" + name + "_" + normalizeSafetyCategory(cat)
			item := cfg.SafetyCustomCategories[id]
			if strings.TrimSpace(item.Label) == "" {
				item.Label = platformDisplayName(name) + " 自定义-" + safetyCategoryLabel(cat)
				changed = true
			}
			before := len(item.Words)
			item.Words = uniqueSafetyWords(append(item.Words, words...))
			if len(item.Words) != before {
				changed = true
			}
			cfg.SafetyCustomCategories[id] = item
			if len(item.Words) > 0 {
				if !cfg.SafetyPlatformCategories[name][id] {
					cfg.SafetyPlatformCategories[name][id] = true
					changed = true
				}
			}
		}
	}
	return changed
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
	id = normalizeSafetyCategory(id)
	for _, def := range safetyCategoryDefs {
		if def.ID == id {
			return true
		}
	}
	return false
}

func safetyCategoryLabel(id string) string {
	id = normalizeSafetyCategory(id)
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
		item.Label = strings.TrimSpace(item.Label)
		if item.Label == "" {
			item.Label = id
		}
		item.Words = uniqueSafetyWords(item.Words)
		item.Excludes = uniqueSafetyWords(item.Excludes)
		out[id] = item
	}
	return out
}

func normalizeSafetyMap(in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range in {
		k = normalizeSafetyCategory(k)
		if validSafetyCategory(k) || k != "" {
			out[k] = v
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
		cat = normalizeSafetyCategory(cat)
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
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}
		key := normalizeSafetyText(word)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, word)
	}
	sort.Slice(out, func(i, j int) bool {
		return normalizeSafetyText(out[i]) < normalizeSafetyText(out[j])
	})
	return out
}

func safetyBlocked(cfg config, meta mediaMeta, raw string) (safetyHit, bool) {
	if !cfg.SafetyFilterEnabled {
		return safetyHit{}, false
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
		for _, word := range def.Keywords {
			if safetyTextContains(normalized, word) {
				if safetyExcluded(cfg, platform, def.ID, normalized) {
					continue
				}
				return safetyHit{Category: def.ID, Keyword: word, Source: "builtin"}, true
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
	word = normalizeSafetyText(word)
	return word != "" && strings.Contains(normalizedText, word)
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
		ctx.SendChain(message.Text("usage: /媒体解析 safety platform twitter category adult_scam on|off"))
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
			ctx.SendChain(message.Text("usage: /媒体解析 safety platform twitter adult add|del word"))
		}
		return false
	}
	cat := normalizeSafetyCategory(args[0])
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
	if platform == "" {
		if currentConf.SafetyCustomGlobal == nil {
			currentConf.SafetyCustomGlobal = map[string][]string{}
		}
		currentConf.SafetyCustomGlobal[cat] = updateSafetyWordList(currentConf.SafetyCustomGlobal[cat], action, word)
		return true
	}
	if currentConf.SafetyCustomPlatform == nil {
		currentConf.SafetyCustomPlatform = map[string]map[string][]string{}
	}
	if currentConf.SafetyCustomPlatform[platform] == nil {
		currentConf.SafetyCustomPlatform[platform] = map[string][]string{}
	}
	currentConf.SafetyCustomPlatform[platform][cat] = updateSafetyWordList(currentConf.SafetyCustomPlatform[platform][cat], action, word)
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
