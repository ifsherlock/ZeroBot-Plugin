// Package deerpipe 🦌管签到，移植自 nonebot-plugin-deer-pipe
package deerpipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	accessNone      = "none"
	accessBlacklist = "blacklist"
	accessWhitelist = "whitelist"

	defaultBanMaxDays = 30
)

var (
	engine = control.AutoRegister(&ctrl.Options[*zero.Ctx]{
		DisableOnDefault: false,
		Brief:            "🦌管签到",
		Help: "每月🦌管签到，移植自 nonebot-plugin-deer-pipe。\n" +
			"- 🦌 / 鹿 → 🦌管1次\n" +
			"- 🦌 @xxx → 帮xxx🦌管1次（仅群聊）\n" +
			"- 补🦌 x → 补🦌本月x日\n" +
			"- 🦌历 [@xxx] → 看本月🦌日历\n" +
			"- 🦌榜 → 看本月本群🦌排行榜（仅群聊）\n" +
			"- 帮🦌 on|off [@xxx] → 禁止/允许被别人帮🦌\n" +
			"- 禁🦌 @xxx [时长] → 禁止xxx一段时间内🦌（仅群管理）\n" +
			"- 🦌帮助 → 打开帮助\n" +
			"* 以上命令中的“🦌”均可换成“鹿”字",
		PrivateDataFolder: "deerpipe",
	})

	cfgMu    sync.RWMutex
	cfg      deerConfig
	cfgPath  string
	atUserRE = regexp.MustCompile(`\[CQ:at,(?:\S*?,)?qq=(\d+)(?:,\S*?)?\]`)
	unitRE   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(天|日|小时|时|分钟|分|秒|d|h|m|s)`)
)

type deerAccess struct {
	PrivateMode        string  `json:"private_mode"`
	PrivateWhitelist   []int64 `json:"private_whitelist"`
	PrivateBlacklist   []int64 `json:"private_blacklist"`
	GroupMode          string  `json:"group_mode"`
	GroupWhitelist     []int64 `json:"group_whitelist"`
	GroupBlacklist     []int64 `json:"group_blacklist"`
	GroupUserMode      string  `json:"group_user_mode"`
	GroupUserWhitelist []int64 `json:"group_user_whitelist"`
	GroupUserBlacklist []int64 `json:"group_user_blacklist"`
}

type deerConfig struct {
	Enabled        bool       `json:"enabled"`
	PrivateEnabled bool       `json:"private_enabled"`
	HelpEnabled    bool       `json:"help_enabled"`
	MakeupEnabled  bool       `json:"makeup_enabled"`
	RankEnabled    bool       `json:"rank_enabled"`
	BanMaxDays     int        `json:"ban_max_days"`
	Access         deerAccess `json:"access"`
}

func init() {
	cfgPath = filepath.Join(engine.DataFolder(), "config.json")
	dataPath = filepath.Join(engine.DataFolder(), "data.json")
	loadConfig()
	loadData()

	// 🦌 / 鹿 [@xxx] 签到
	engine.OnRegex(`^\s*(?:🦌|鹿)\s*((?:\[CQ:at,[^\]]*\]\s*)*)$`).SetBlock(true).Handle(handleDeer)
	// 补🦌 x
	engine.OnRegex(`^\s*补\s*(?:🦌|鹿)\s*(\d+)\s*$`).SetBlock(true).Handle(handleDeerPast)
	// 🦌历 [@xxx]
	engine.OnRegex(`^\s*(?:🦌|鹿)历\s*((?:\[CQ:at,[^\]]*\]\s*)*)$`).SetBlock(true).Handle(handleDeerCalendar)
	// 🦌榜
	engine.OnRegex(`^\s*(?:🦌|鹿)榜\s*$`, zero.OnlyGroup).SetBlock(true).Handle(handleDeerRank)
	// 帮🦌 on|off [@xxx]
	engine.OnRegex(`^\s*帮\s*(?:🦌|鹿)\s*(on|off|开启|关闭|开|关)\s*((?:\[CQ:at,[^\]]*\]\s*)*)$`, zero.OnlyGroup).SetBlock(true).Handle(handleSetCanBeHelped)
	// 禁🦌 @xxx [时长]
	engine.OnRegex(`^\s*禁\s*(?:🦌|鹿)\s*(\[CQ:at,[^\]]*\])\s*(\S*)\s*$`, zero.OnlyGroup).SetBlock(true).Handle(handleNoDeer)
	// 🦌帮助
	engine.OnFullMatchGroup([]string{"🦌帮助", "鹿帮助"}).SetBlock(true).Handle(handleDeerHelp)

	logrus.Infoln("[deerpipe] ready")
}

func defaultDeerConfig() deerConfig {
	return deerConfig{
		Enabled:        true,
		PrivateEnabled: true,
		HelpEnabled:    true,
		MakeupEnabled:  true,
		RankEnabled:    true,
		BanMaxDays:     defaultBanMaxDays,
		Access: deerAccess{
			PrivateMode:   accessNone,
			GroupMode:     accessNone,
			GroupUserMode: accessNone,
		},
	}
}

func loadConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = defaultDeerConfig()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = saveConfigLocked()
		}
		return
	}
	var saved deerConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		logrus.Warnf("[deerpipe] load config failed: %v", err)
		return
	}
	cfg = normalizeConfig(saved)
	_ = saveConfigLocked()
}

func saveConfigLocked() error {
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, cfgPath)
}

func normalizeConfig(in deerConfig) deerConfig {
	out := in
	out.Access.PrivateMode = normalizeAccessMode(in.Access.PrivateMode)
	out.Access.GroupMode = normalizeAccessMode(in.Access.GroupMode)
	out.Access.GroupUserMode = normalizeAccessMode(in.Access.GroupUserMode)
	out.Access.PrivateWhitelist = uniqueIDs(in.Access.PrivateWhitelist)
	out.Access.PrivateBlacklist = uniqueIDs(in.Access.PrivateBlacklist)
	out.Access.GroupWhitelist = uniqueIDs(in.Access.GroupWhitelist)
	out.Access.GroupBlacklist = uniqueIDs(in.Access.GroupBlacklist)
	out.Access.GroupUserWhitelist = uniqueIDs(in.Access.GroupUserWhitelist)
	out.Access.GroupUserBlacklist = uniqueIDs(in.Access.GroupUserBlacklist)
	if out.BanMaxDays <= 0 || out.BanMaxDays > 365 {
		out.BanMaxDays = defaultBanMaxDays
	}
	return out
}

func normalizeAccessMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != accessBlacklist && mode != accessWhitelist {
		return accessNone
	}
	return mode
}

func uniqueIDs(in []int64) []int64 {
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

func containsID(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func snapshotConfig() deerConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

// allowAccess 检查 QQ 个人/群/群成员黑白名单，与 mediaparser 同构。
func allowAccess(c deerConfig, userID, groupID int64) bool {
	if !c.Enabled {
		return false
	}
	if groupID == 0 {
		if !c.PrivateEnabled {
			return false
		}
		switch c.Access.PrivateMode {
		case accessWhitelist:
			return containsID(c.Access.PrivateWhitelist, userID)
		case accessBlacklist:
			return !containsID(c.Access.PrivateBlacklist, userID)
		default:
			return true
		}
	}
	switch c.Access.GroupMode {
	case accessWhitelist:
		if !containsID(c.Access.GroupWhitelist, groupID) {
			return false
		}
	case accessBlacklist:
		if containsID(c.Access.GroupBlacklist, groupID) {
			return false
		}
	}
	switch c.Access.GroupUserMode {
	case accessWhitelist:
		return containsID(c.Access.GroupUserWhitelist, userID)
	case accessBlacklist:
		return !containsID(c.Access.GroupUserBlacklist, userID)
	default:
		return true
	}
}

func accessOK(ctx *zero.Ctx) bool {
	c := snapshotConfig()
	if allowAccess(c, ctx.Event.UserID, ctx.Event.GroupID) {
		return true
	}
	logrus.Debugf("[deerpipe] skip access user=%d group=%d", ctx.Event.UserID, ctx.Event.GroupID)
	return false
}

func parseAtTargets(seg string) []int64 {
	matches := atUserRE.FindAllStringSubmatch(seg, -1)
	out := make([]int64, 0, len(matches))
	for _, m := range matches {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func regexMatched(ctx *zero.Ctx) []string {
	matched, _ := ctx.State["regex_matched"].([]string)
	return matched
}

func replyChain(ctx *zero.Ctx, segments ...message.Segment) {
	chain := make([]message.Segment, 0, len(segments)+1)
	if ctx.Event.MessageID != nil {
		chain = append(chain, message.Reply(ctx.Event.MessageID))
	}
	chain = append(chain, segments...)
	ctx.SendChain(chain...)
}

func displayName(ctx *zero.Ctx, uid int64) string {
	name := ""
	if ctx != nil {
		name = strings.TrimSpace(ctx.CardOrNickName(uid))
	}
	if name == "" {
		name = strconv.FormatInt(uid, 10)
	}
	return name
}

// handleDeer 🦌 / 🦌 @xxx
func handleDeer(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	c := snapshotConfig()
	now := time.Now()
	matched := regexMatched(ctx)
	targets := []int64{}
	if len(matched) > 1 {
		targets = parseAtTargets(matched[1])
	}
	gid := ctx.Event.GroupID
	uid := ctx.Event.UserID
	// 帮别人🦌仅限群聊；私聊出现 @ 直接忽略
	if len(targets) > 0 && gid == 0 {
		return
	}
	if len(targets) > 0 && !c.HelpEnabled {
		replyChain(ctx, message.Text("帮🦌功能未开启捏"))
		return
	}
	target := uid
	helping := len(targets) > 0
	if helping {
		target = targets[0]
	}
	user := getUser(gid, target)
	if helping && user.HelpDisabled {
		replyChain(ctx, message.Text("该用户不准别人帮🦌捏"))
		return
	}
	if gid != 0 && user.NoDeerUntil > now.Unix() {
		replyChain(ctx, message.Text("该用户已被禁🦌至"+formatUntil(user.NoDeerUntil)))
		return
	}
	name := displayName(ctx, target)
	_, records := checkIn(gid, target, name, now, 0)
	img, err := renderCalendar(now, records, name, fetchAvatar(target))
	if err != nil {
		logrus.Warnf("[deerpipe] render calendar failed: %v", err)
		replyChain(ctx, message.Text("成功🦌了，但是🦌历画不出来捏"))
		return
	}
	if helping {
		replyChain(ctx, message.Text("成功帮"), message.At(target), message.Text("🦌了"), message.ImageBytes(img))
		return
	}
	replyChain(ctx, message.Text("成功🦌了"), message.ImageBytes(img))
}

func deerHelpTargetPermitted(ctx *zero.Ctx, hasTarget bool) bool {
	if !hasTarget {
		return true
	}
	return zero.AdminPermission(ctx)
}

// handleDeerPast 补🦌 x
func handleDeerPast(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	c := snapshotConfig()
	if !c.MakeupEnabled {
		replyChain(ctx, message.Text("补🦌功能未开启捏"))
		return
	}
	now := time.Now()
	matched := regexMatched(ctx)
	if len(matched) < 2 {
		return
	}
	day, err := strconv.Atoi(matched[1])
	if err != nil || day < 1 || day >= now.Day() {
		replyChain(ctx, message.Text("不是合法的补🦌日期捏"))
		return
	}
	gid := ctx.Event.GroupID
	uid := ctx.Event.UserID
	name := displayName(ctx, uid)
	ok, records := checkIn(gid, uid, name, now, day)
	img, rerr := renderCalendar(now, records, name, fetchAvatar(uid))
	if rerr != nil {
		logrus.Warnf("[deerpipe] render calendar failed: %v", rerr)
		if ok {
			replyChain(ctx, message.Text("成功补🦌"))
		} else {
			replyChain(ctx, message.Text("不能补🦌已经🦌过的日子捏"))
		}
		return
	}
	if ok {
		replyChain(ctx, message.Text("成功补🦌"), message.ImageBytes(img))
		return
	}
	replyChain(ctx, message.Text("不能补🦌已经🦌过的日子捏"), message.ImageBytes(img))
}

// handleDeerCalendar 🦌历 [@xxx]
func handleDeerCalendar(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	now := time.Now()
	matched := regexMatched(ctx)
	targets := []int64{}
	if len(matched) > 1 {
		targets = parseAtTargets(matched[1])
	}
	gid := ctx.Event.GroupID
	uid := ctx.Event.UserID
	if len(targets) > 0 && gid == 0 {
		return
	}
	target := uid
	if len(targets) > 0 {
		target = targets[0]
	}
	name := displayName(ctx, target)
	records := getRecords(gid, target, now)
	img, err := renderCalendar(now, records, name, fetchAvatar(target))
	if err != nil {
		logrus.Warnf("[deerpipe] render calendar failed: %v", err)
		replyChain(ctx, message.Text("🦌历画不出来捏"))
		return
	}
	replyChain(ctx, message.ImageBytes(img))
}

// handleDeerRank 🦌榜
func handleDeerRank(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	c := snapshotConfig()
	if !c.RankEnabled {
		replyChain(ctx, message.Text("🦌榜功能未开启捏"))
		return
	}
	now := time.Now()
	gid := ctx.Event.GroupID
	rank := topRank(gid, now, 5)
	if len(rank) == 0 {
		replyChain(ctx, message.Text("本群本月还没有人🦌过捏"))
		return
	}
	rows := make([]rankRow, 0, len(rank))
	for _, item := range rank {
		name := displayName(ctx, item.UserID)
		if name == strconv.FormatInt(item.UserID, 10) && item.Name != "" {
			name = item.Name
		}
		rows = append(rows, rankRow{Name: name, Avatar: fetchAvatar(item.UserID), Count: item.Count})
	}
	img, err := renderRank(rows)
	if err != nil {
		logrus.Warnf("[deerpipe] render rank failed: %v", err)
		replyChain(ctx, message.Text("🦌榜画不出来捏"))
		return
	}
	replyChain(ctx, message.ImageBytes(img))
}

// handleSetCanBeHelped 帮🦌 on|off [@xxx]
func handleSetCanBeHelped(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	matched := regexMatched(ctx)
	if len(matched) < 3 {
		return
	}
	allowed := matched[1] == "on" || matched[1] == "开启" || matched[1] == "开"
	targets := parseAtTargets(matched[2])
	gid := ctx.Event.GroupID
	uid := ctx.Event.UserID
	target := uid
	if len(targets) > 0 {
		if !deerHelpTargetPermitted(ctx, true) {
			replyChain(ctx, message.Text("权限不足"))
			return
		}
		target = targets[0]
	}
	setCanBeHelped(gid, target, allowed)
	verb := "已禁止"
	if allowed {
		verb = "已允许"
	}
	if target != uid {
		replyChain(ctx, message.Text(verb+"帮"), message.At(target), message.Text("🦌"))
		return
	}
	replyChain(ctx, message.Text(verb+"帮🦌"))
}

// handleNoDeer 禁🦌 @xxx [时长]
func handleNoDeer(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	if !zero.AdminPermission(ctx) {
		replyChain(ctx, message.Text("权限不足"))
		return
	}
	c := snapshotConfig()
	now := time.Now()
	matched := regexMatched(ctx)
	if len(matched) < 3 {
		return
	}
	targets := parseAtTargets(matched[1])
	if len(targets) == 0 {
		return
	}
	target := targets[0]
	gid := ctx.Event.GroupID
	durText := strings.TrimSpace(matched[2])
	if durText == "" {
		setNoDeerUntil(gid, target, 0)
		replyChain(ctx, message.Text("已解禁"), message.At(target), message.Text("的🦌权"))
		return
	}
	dur, err := parseDuration(durText)
	if err != nil {
		replyChain(ctx, message.Text("时间段表达式解析错误"))
		return
	}
	maxDur := time.Duration(c.BanMaxDays) * 24 * time.Hour
	if dur > maxDur {
		replyChain(ctx, message.Text(fmt.Sprintf("时间段过长：最大允许时间为%d天", c.BanMaxDays)))
		return
	}
	until := now.Add(dur).Unix()
	setNoDeerUntil(gid, target, until)
	replyChain(ctx, message.Text("已禁止"), message.At(target), message.Text("的🦌权至"+formatUntil(until)))
}

func handleDeerHelp(ctx *zero.Ctx) {
	if !accessOK(ctx) {
		return
	}
	replyChain(ctx, message.Text(
		"== 🦌管插件帮助 ==\n"+
			"[🦌] 🦌管1次\n"+
			"[🦌 @xxx] 帮xxx🦌管1次（仅群组）\n"+
			"[补🦌 x] 补🦌本月x日\n"+
			"[🦌历] 看本月🦌日历\n"+
			"[🦌历 @xxx] 看xxx的本月🦌日历（仅群组）\n"+
			"[🦌榜] 看本月本群🦌排行榜（仅群组）\n"+
			"[帮🦌 <on|off>] 禁止/允许别人帮🦌（仅群组）\n"+
			"[帮🦌 <on|off> @xxx] 禁止/允许别人帮xxx🦌（仅群组管理员）\n"+
			"[禁🦌 @xxx [yyy]] xxx接下来一段时间yyy内禁止🦌，不提供yyy时视为解禁（仅群组管理员，yyy如 30m、2h、1天）\n"+
			"[🦌帮助] 打开帮助\n\n"+
			"* 以上命令中的“🦌”均可换成“鹿”字\n\n"+
			"移植自 https://github.com/SamuNatsu/nonebot-plugin-deer-pipe"))
}

func formatUntil(unix int64) string {
	return time.Unix(unix, 0).Format("2006-01-02T15:04:05")
}

// parseDuration 解析 pytimeparse 风格的时长：30m、2h32m、1d、1天、90 等。
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	rest := s
	matches := unitRE.FindAllStringSubmatch(s, -1)
	if len(matches) > 0 {
		total := time.Duration(0)
		for _, m := range matches {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				return 0, err
			}
			var unit time.Duration
			switch m[2] {
			case "天", "日", "d":
				unit = 24 * time.Hour
			case "小时", "时", "h":
				unit = time.Hour
			case "分钟", "分", "m":
				unit = time.Minute
			case "秒", "s":
				unit = time.Second
			}
			total += time.Duration(v * float64(unit))
			rest = strings.Replace(rest, m[0], "", 1)
		}
		if strings.TrimSpace(rest) != "" {
			return 0, errors.New("invalid duration")
		}
		if total <= 0 {
			return 0, errors.New("non-positive duration")
		}
		return total, nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
		return time.Duration(v * float64(time.Second)), nil
	}
	return 0, errors.New("invalid duration")
}
