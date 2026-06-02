package mediaparser

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	zip "github.com/alexmullins/zip"
	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	defaultMediaShieldReplyText   = "已打包，解压密码：{password}"
	defaultMediaShieldEmoji       = "😏"
	currentMediaShieldSeedVersion = 1
)

type mediaShieldReasonInfo struct {
	Active  bool
	Passive bool
	Hit     safetyHit
}

func defaultMediaShieldKeywords() []string {
	return []string{"色色", "打包", "色图", "setu", "大雷", "大奶", "大屁股"}
}

func defaultMediaShieldPassiveWords() []string {
	words := []string{}
	for _, def := range safetyCategoryDefs {
		if def.ID == safetyCategoryAdult {
			words = append(words, safetyBuiltinWords(def)...)
			break
		}
	}
	words = append(words, defaultMediaShieldPassiveExtraWords()...)
	return uniqueSafetyWords(words)
}

func defaultMediaShieldPassiveExtraWords() []string {
	return decodeSafetyBuiltinWords([]string{
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
}

func normalizeMediaShieldConfig(cfg *config) bool {
	changed := false
	if cfg.MediaShieldGroupEnabled == nil {
		cfg.MediaShieldGroupEnabled = map[int64]bool{}
		changed = true
	}
	if strings.TrimSpace(cfg.MediaShieldReplyText) == "" {
		cfg.MediaShieldReplyText = defaultMediaShieldReplyText
		changed = true
	}
	if strings.TrimSpace(cfg.MediaShieldEmoji) == "" {
		cfg.MediaShieldEmoji = defaultMediaShieldEmoji
		changed = true
	}
	words := uniqueSafetyWords(cfg.MediaShieldKeywords)
	if len(words) == 0 {
		words = defaultMediaShieldKeywords()
	}
	if !stringSlicesEqual(cfg.MediaShieldKeywords, words) {
		cfg.MediaShieldKeywords = words
		changed = true
	}
	if cfg.MediaShieldSeedVersion < currentMediaShieldSeedVersion {
		passiveWords := uniqueSafetyWords(cfg.MediaShieldPassiveWords)
		if len(passiveWords) == 0 {
			passiveWords = defaultMediaShieldPassiveWords()
		}
		if !stringSlicesEqual(cfg.MediaShieldPassiveWords, passiveWords) {
			cfg.MediaShieldPassiveWords = passiveWords
			changed = true
		}
		cfg.MediaShieldSeedVersion = currentMediaShieldSeedVersion
		changed = true
	} else {
		passiveWords := uniqueSafetyWords(cfg.MediaShieldPassiveWords)
		if !stringSlicesEqual(cfg.MediaShieldPassiveWords, passiveWords) {
			cfg.MediaShieldPassiveWords = passiveWords
			changed = true
		}
	}
	passiveExcludes := uniqueSafetyWords(cfg.MediaShieldPassiveExcludes)
	if !stringSlicesEqual(cfg.MediaShieldPassiveExcludes, passiveExcludes) {
		cfg.MediaShieldPassiveExcludes = passiveExcludes
		changed = true
	}
	return changed
}

func mediaShieldShouldHandle(cfg config, meta mediaMeta, raw string, hit safetyHit, blocked bool, groupID int64) bool {
	if !mediaShieldAvailableForGroup(cfg, meta, groupID) {
		return false
	}
	if mediaShieldRiskBlocked(cfg, meta, raw) {
		return false
	}
	if cfg.MediaShieldPassive && mediaShieldHasTwitterSensitiveMarker(meta, raw) {
		return true
	}
	if cfg.MediaShieldPassive && mediaShieldPassiveTriggered(cfg, meta, raw) && mediaShieldCanTakeoverBlocked(hit, blocked) {
		return true
	}
	return cfg.MediaShieldActive && mediaShieldActiveTriggered(cfg, raw)
}

func mediaShieldReason(cfg config, meta mediaMeta, raw string, hit safetyHit, blocked bool, groupID int64) mediaShieldReasonInfo {
	active := false
	passive := false
	if mediaShieldAvailableForGroup(cfg, meta, groupID) && !mediaShieldRiskBlocked(cfg, meta, raw) {
		passive = cfg.MediaShieldPassive && (mediaShieldHasTwitterSensitiveMarker(meta, raw) || (mediaShieldPassiveTriggered(cfg, meta, raw) && mediaShieldCanTakeoverBlocked(hit, blocked)))
		active = cfg.MediaShieldActive && mediaShieldActiveTriggered(cfg, raw)
	}
	return mediaShieldReasonInfo{
		Active:  active,
		Passive: passive,
		Hit:     hit,
	}
}

func mediaShieldAvailableForGroup(cfg config, meta mediaMeta, groupID int64) bool {
	if !cfg.MediaShieldEnabled || normalizePlatformName(meta.Platform) != "twitter" {
		return false
	}
	if groupID == 0 {
		return true
	}
	return cfg.MediaShieldGroupEnabled[groupID]
}

func mediaShieldHasTwitterSensitiveMarker(meta mediaMeta, raw string) bool {
	if normalizePlatformName(meta.Platform) != "twitter" {
		return false
	}
	return strings.Contains(safetyScanText(meta, raw), safetyMarkerTwitterSensitive)
}

func mediaShieldRiskBlocked(cfg config, meta mediaMeta, raw string) bool {
	customRiskCategories := map[string]safetyCustomCategory{}
	riskEnabled := map[string]bool{
		safetyCategoryPolitics: true,
		safetyCategoryViolence: true,
	}
	for id, item := range cfg.SafetyCustomCategories {
		id = normalizeCustomSafetyCategoryID(id)
		if id != "custom_"+safetyCategoryPolitics && id != "custom_"+safetyCategoryViolence {
			continue
		}
		if id == "" {
			continue
		}
		customRiskCategories[id] = item
		riskEnabled[id] = true
	}
	riskCfg := cfg
	riskCfg.SafetyFilterEnabled = true
	riskCfg.SafetyTwitterSensitive = false
	riskCfg.SafetyGlobalCategories = riskEnabled
	riskCfg.SafetyPlatformCategories = map[string]map[string]bool{}
	riskCfg.SafetyCustomGlobal = filterMediaShieldRiskCustomWords(cfg.SafetyCustomGlobal)
	riskCfg.SafetyCustomPlatform = filterMediaShieldRiskCustomPlatformWords(cfg.SafetyCustomPlatform)
	riskCfg.SafetyCustomCategories = customRiskCategories
	riskCfg.SafetyExcludeGlobal = filterMediaShieldRiskCustomWords(cfg.SafetyExcludeGlobal)
	riskCfg.SafetyExcludePlatform = filterMediaShieldRiskCustomPlatformWords(cfg.SafetyExcludePlatform)
	_, blocked := safetyBlocked(riskCfg, meta, raw)
	return blocked
}

func filterMediaShieldRiskCustomWords(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for cat, words := range in {
		cat = normalizeSafetyCategory(cat)
		if cat == safetyCategoryPolitics || cat == safetyCategoryViolence {
			out[cat] = words
		}
	}
	return out
}

func filterMediaShieldRiskCustomPlatformWords(in map[string]map[string][]string) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for platform, cats := range in {
		filtered := filterMediaShieldRiskCustomWords(cats)
		if len(filtered) > 0 {
			out[platform] = filtered
		}
	}
	return out
}

func mediaShieldCanTakeoverBlocked(hit safetyHit, blocked bool) bool {
	if !blocked {
		return true
	}
	cat := normalizeSafetyCategory(hit.Category)
	return cat == safetyCategoryAdult || strings.Contains(cat, "adult")
}

func mediaShieldPassiveTriggered(cfg config, meta mediaMeta, raw string) bool {
	text := normalizeSafetyText(safetyScanText(meta, raw))
	if safetyWordsContain(text, cfg.MediaShieldPassiveExcludes) {
		return false
	}
	return safetyWordsContain(text, cfg.MediaShieldPassiveWords)
}

func mediaShieldActiveTriggered(cfg config, raw string) bool {
	text := normalizeSafetyText(raw)
	for _, word := range cfg.MediaShieldKeywords {
		if safetyTextContains(text, word) {
			return true
		}
	}
	return false
}

func sendMediaShieldPackage(ctx *zero.Ctx, cfg config, meta *mediaMeta, reason mediaShieldReasonInfo) error {
	if ctx == nil || meta == nil {
		return nil
	}
	if reason.Active && strings.TrimSpace(cfg.MediaShieldEmoji) != "" {
		ctx.SendChain(message.Text(strings.TrimSpace(cfg.MediaShieldEmoji)))
	}
	if err := sendMediaShieldCard(ctx, cfg, *meta); err != nil {
		logrus.Warnf("[mediaparser] media_shield_card_failed platform=%s title=%q error=%v", meta.Platform, truncate(meta.Title, 80), err)
	}
	meta.ForceLocal = true
	if err := processDownloads(cfg, meta); err != nil {
		return fmt.Errorf("download shield media: %w", err)
	}
	files := mediaShieldLocalFiles(meta)
	if len(files) == 0 {
		return fmt.Errorf("no local media files")
	}
	password, err := mediaShieldPassword()
	if err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	archive := cacheFile(meta, "shield", 0, ".zip")
	if err := createMediaShieldZip(files, archive, password); err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	scheduleDelete(archive, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
	if err := uploadMediaShieldArchive(ctx, archive); err != nil {
		return fmt.Errorf("upload zip: %w", err)
	}
	ctx.SendChain(message.Text(mediaShieldReplyText(cfg, password)))
	return nil
}

func sendMediaShieldCard(ctx *zero.Ctx, cfg config, meta mediaMeta) error {
	card, err := renderInfoCard(meta)
	if err != nil {
		return err
	}
	defer scheduleDelete(card, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
	img, err := imaging.Open(card)
	if err != nil {
		return err
	}
	img = imaging.Blur(img, 16)
	shieldCard := cacheFile(&meta, "shield_card", 0, ".png")
	if err := os.MkdirAll(filepath.Dir(shieldCard), 0755); err != nil {
		return err
	}
	if err := imaging.Save(img, shieldCard); err != nil {
		return err
	}
	target := oneBotLocalMediaTarget(shieldCard)
	if isOfficialQQBotEvent(ctx) {
		target = fileURI(shieldCard)
	}
	ctx.SendChain(message.Image(target))
	scheduleDelete(shieldCard, time.Duration(cfg.CacheTTLMinutes)*time.Minute)
	return nil
}

func mediaShieldLocalFiles(meta *mediaMeta) []string {
	if meta == nil {
		return nil
	}
	files := []string{}
	for i, mode := range meta.VideoModes {
		if mode == "local" && i < len(meta.FilePaths) && meta.FilePaths[i] != "" {
			files = append(files, meta.FilePaths[i])
		}
	}
	offset := len(meta.VideoURLs)
	for i, mode := range meta.ImageModes {
		pos := offset + i
		if mode == "local" && pos < len(meta.FilePaths) && meta.FilePaths[pos] != "" {
			files = append(files, meta.FilePaths[pos])
		}
	}
	return files
}

func createMediaShieldZip(files []string, out, password string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	used := map[string]int{}
	for _, path := range files {
		if err := addMediaShieldZipFile(zw, path, password, used); err != nil {
			return err
		}
	}
	return nil
}

func addMediaShieldZipFile(zw *zip.Writer, path, password string, used map[string]int) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	name := sanitizeMediaShieldZipName(filepath.Base(path), used)
	w, err := zw.Encrypt(name, password)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func sanitizeMediaShieldZipName(name string, used map[string]int) string {
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "media"
	}
	base, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
	if base == "" {
		base = "media"
	}
	next := name
	for {
		if used[next] == 0 {
			used[next] = 1
			return next
		}
		used[name]++
		next = fmt.Sprintf("%s_%d%s", base, used[name], ext)
	}
}

func uploadMediaShieldArchive(ctx *zero.Ctx, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	name := filepath.Base(path)
	if ctx.Event != nil && ctx.Event.GroupID != 0 {
		ret := ctx.UploadThisGroupFile(abs, name, "")
		if ret.Status == "failed" {
			return fmt.Errorf("group upload failed: %s%s", ret.Message, ret.Wording)
		}
		return nil
	}
	if ctx.Event != nil && ctx.Event.UserID != 0 {
		if ret := ctx.UploadPrivateFile(ctx.Event.UserID, abs, name); strings.TrimSpace(ret) != "" && strings.Contains(strings.ToLower(ret), "failed") {
			return fmt.Errorf("private upload failed: %s", ret)
		}
	}
	return nil
}

func mediaShieldPassword() (string, error) {
	var b strings.Builder
	for i := 0; i < 6; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		b.WriteString(n.String())
	}
	return b.String(), nil
}

func mediaShieldReplyText(cfg config, password string) string {
	text := strings.TrimSpace(cfg.MediaShieldReplyText)
	if text == "" {
		text = defaultMediaShieldReplyText
	}
	text = strings.ReplaceAll(text, "{password}", password)
	if !strings.Contains(text, password) {
		text += "\n解压密码：" + password
	}
	return text
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
