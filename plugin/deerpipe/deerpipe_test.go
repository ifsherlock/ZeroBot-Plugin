package deerpipe

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func useTempStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	oldData, oldCfg := dataPath, cfgPath
	dataPath = filepath.Join(dir, "data.json")
	cfgPath = filepath.Join(dir, "config.json")
	dataMu.Lock()
	oldStore := store
	store = deerStore{Users: map[string]*deerUser{}, LastMonth: monthKey(time.Now())}
	dataMu.Unlock()
	t.Cleanup(func() {
		dataMu.Lock()
		store = oldStore
		dataMu.Unlock()
		dataPath, cfgPath = oldData, oldCfg
	})
}

func TestCheckInTodayRepeats(t *testing.T) {
	useTempStore(t)
	now := time.Now()
	ok, records := checkIn(100, 200, "tester", now, 0, 1)
	if !ok || records[now.Day()] != 1 {
		t.Fatalf("first check-in: ok=%v records=%v", ok, records)
	}
	ok, records = checkIn(100, 200, "tester", now, 0, 1)
	if !ok || records[now.Day()] != 2 {
		t.Fatalf("repeat check-in should increment: ok=%v records=%v", ok, records)
	}
}

func TestCheckInMultiTimes(t *testing.T) {
	useTempStore(t)
	now := time.Now()
	ok, records := checkIn(100, 200, "tester", now, 0, 6)
	if !ok || records[now.Day()] != 6 {
		t.Fatalf("multi check-in: ok=%v records=%v", ok, records)
	}
	ok, records = checkIn(100, 200, "tester", now, 0, 0)
	if !ok || records[now.Day()] != 7 {
		t.Fatalf("times<1 should fall back to 1: ok=%v records=%v", ok, records)
	}
}

func TestDeerCmdPattern(t *testing.T) {
	re := regexp.MustCompile(deerCmdPattern)
	good := map[string]int{
		"🦌":                   1,
		"鹿":                   1,
		"🦌🦌🦌🦌🦌🦌":              6,
		"🦌 🦌 鹿":               3,
		"鹿鹿🦌":                 3,
		" 🦌 ":                 1,
		"🦌 [CQ:at,qq=123]":    1,
		"🦌🦌🦌 [CQ:at,qq=123] ": 3,
	}
	for input, want := range good {
		m := re.FindStringSubmatch(input)
		if m == nil {
			t.Fatalf("%q should match", input)
		}
		if got := countDeer(m[1]); got != want {
			t.Fatalf("countDeer(%q) = %d, want %d", input, got, want)
		}
	}
	for _, bad := range []string{"", "🦌历", "鹿榜", "补🦌 3", "小🦌", "🦌x", "🦌帮助"} {
		if re.MatchString(bad) {
			t.Fatalf("%q should not match", bad)
		}
	}
}

func TestCheckInMakeup(t *testing.T) {
	useTempStore(t)
	now := time.Date(time.Now().Year(), time.Now().Month(), 15, 12, 0, 0, 0, time.Local)
	ok, records := checkIn(0, 300, "p", now, 3, 1)
	if !ok || records[3] != 1 {
		t.Fatalf("makeup day 3 should succeed: ok=%v records=%v", ok, records)
	}
	ok, _ = checkIn(0, 300, "p", now, 3, 1)
	if ok {
		t.Fatal("makeup already-checked day should fail")
	}
}

func TestCheckInScopedPerGroup(t *testing.T) {
	useTempStore(t)
	now := time.Now()
	_, _ = checkIn(100, 200, "a", now, 0, 1)
	records := getRecords(101, 200, now)
	if len(records) != 0 {
		t.Fatalf("records should be scoped per group, got %v", records)
	}
}

func TestTopRank(t *testing.T) {
	useTempStore(t)
	now := time.Date(time.Now().Year(), time.Now().Month(), 20, 12, 0, 0, 0, time.Local)
	for uid := int64(1); uid <= 7; uid++ {
		for i := int64(0); i < uid; i++ {
			_, _ = checkIn(500, uid, "u", now, 0, 1)
		}
	}
	_, _ = checkIn(501, 99, "other", now, 0, 1)
	rank := topRank(500, now, 5)
	if len(rank) != 5 {
		t.Fatalf("rank size = %d, want 5", len(rank))
	}
	if rank[0].UserID != 7 || rank[0].Count != 7 {
		t.Fatalf("rank top = %+v, want uid 7 count 7", rank[0])
	}
	for _, item := range rank {
		if item.UserID == 99 {
			t.Fatal("rank should only include same group users")
		}
	}
}

func TestCleanupMonthRollover(t *testing.T) {
	useTempStore(t)
	dataMu.Lock()
	store.LastMonth = "2000-01"
	store.Users["1:2"] = &deerUser{Records: map[string]map[string]int{"2000-01": {"5": 2}}}
	store.Users["1:3"] = &deerUser{HelpDisabled: true, Records: map[string]map[string]int{"2000-01": {"6": 1}}}
	dataMu.Unlock()
	records := getRecords(1, 2, time.Now())
	if len(records) != 0 {
		t.Fatalf("old month records should be cleared, got %v", records)
	}
	dataMu.Lock()
	_, plainKept := store.Users["1:2"]
	flagged, flaggedKept := store.Users["1:3"]
	dataMu.Unlock()
	if plainKept {
		t.Fatal("stateless user should be removed after rollover")
	}
	if !flaggedKept || !flagged.HelpDisabled {
		t.Fatal("user with help_disabled should survive rollover")
	}
}

func TestBanAndHelpFlags(t *testing.T) {
	useTempStore(t)
	until := time.Now().Add(time.Hour).Unix()
	setNoDeerUntil(10, 20, until)
	setCanBeHelped(10, 20, false)
	user := getUser(10, 20)
	if user.NoDeerUntil != until || !user.HelpDisabled {
		t.Fatalf("flags not persisted: %+v", user)
	}
	setCanBeHelped(10, 20, true)
	setNoDeerUntil(10, 20, 0)
	user = getUser(10, 20)
	if user.NoDeerUntil != 0 || user.HelpDisabled {
		t.Fatalf("flags not reset: %+v", user)
	}
}

func TestMonthCalendarMatchesPython(t *testing.T) {
	// Python calendar.monthcalendar(2026, 7):
	// [[0,0,1,2,3,4,5],[6,...,12],[13..19],[20..26],[27..31,0,0]]
	weeks := monthCalendar(2026, time.July)
	if len(weeks) != 5 {
		t.Fatalf("2026-07 weeks = %d, want 5", len(weeks))
	}
	if weeks[0] != [7]int{0, 0, 1, 2, 3, 4, 5} {
		t.Fatalf("2026-07 first week = %v", weeks[0])
	}
	if weeks[4] != [7]int{27, 28, 29, 30, 31, 0, 0} {
		t.Fatalf("2026-07 last week = %v", weeks[4])
	}
	// 2026-02 starts on Sunday and has 28 days => 5 weeks, first week [0,0,0,0,0,0,1]
	weeks = monthCalendar(2026, time.February)
	if weeks[0] != [7]int{0, 0, 0, 0, 0, 0, 1} {
		t.Fatalf("2026-02 first week = %v", weeks[0])
	}
	if weeks[len(weeks)-1][0] == 0 {
		t.Fatalf("2026-02 last week = %v", weeks[len(weeks)-1])
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"30m":    30 * time.Minute,
		"2h32m":  2*time.Hour + 32*time.Minute,
		"1d":     24 * time.Hour,
		"1天":     24 * time.Hour,
		"3天12小时": 3*24*time.Hour + 12*time.Hour,
		"90":     90 * time.Second,
		"1.5h":   90 * time.Minute,
	}
	for input, want := range cases {
		got, err := parseDuration(input)
		if err != nil || got != want {
			t.Fatalf("parseDuration(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5m", "0"} {
		if _, err := parseDuration(bad); err == nil {
			t.Fatalf("parseDuration(%q) should fail", bad)
		}
	}
}

func TestAllowAccess(t *testing.T) {
	base := defaultDeerConfig()

	c := base
	c.Enabled = false
	if allowAccess(c, 1, 0) {
		t.Fatal("disabled plugin should deny")
	}

	c = base
	c.PrivateEnabled = false
	if allowAccess(c, 1, 0) {
		t.Fatal("private disabled should deny private")
	}
	if !allowAccess(c, 1, 100) {
		t.Fatal("private disabled should not affect group")
	}

	c = base
	c.Access.PrivateMode = accessWhitelist
	c.Access.PrivateWhitelist = []int64{5}
	if !allowAccess(c, 5, 0) || allowAccess(c, 6, 0) {
		t.Fatal("private whitelist mismatch")
	}

	c = base
	c.Access.PrivateMode = accessBlacklist
	c.Access.PrivateBlacklist = []int64{5}
	if allowAccess(c, 5, 0) || !allowAccess(c, 6, 0) {
		t.Fatal("private blacklist mismatch")
	}

	c = base
	c.Access.GroupMode = accessWhitelist
	c.Access.GroupWhitelist = []int64{100}
	if !allowAccess(c, 1, 100) || allowAccess(c, 1, 101) {
		t.Fatal("group whitelist mismatch")
	}

	c = base
	c.Access.GroupMode = accessBlacklist
	c.Access.GroupBlacklist = []int64{100}
	if allowAccess(c, 1, 100) || !allowAccess(c, 1, 101) {
		t.Fatal("group blacklist mismatch")
	}

	c = base
	c.Access.GroupUserMode = accessWhitelist
	c.Access.GroupUserWhitelist = []int64{7}
	if !allowAccess(c, 7, 100) || allowAccess(c, 8, 100) {
		t.Fatal("group user whitelist mismatch")
	}

	c = base
	c.Access.GroupUserMode = accessBlacklist
	c.Access.GroupUserBlacklist = []int64{7}
	if allowAccess(c, 7, 100) || !allowAccess(c, 8, 100) {
		t.Fatal("group user blacklist mismatch")
	}

	c = base
	c.Access.GroupMode = accessWhitelist
	c.Access.GroupWhitelist = []int64{100}
	c.Access.GroupUserMode = accessBlacklist
	c.Access.GroupUserBlacklist = []int64{7}
	if allowAccess(c, 7, 100) || !allowAccess(c, 8, 100) || allowAccess(c, 8, 101) {
		t.Fatal("combined group + group user mismatch")
	}
}

func TestNormalizeConfig(t *testing.T) {
	in := deerConfig{
		BanMaxDays: -1,
		Access: deerAccess{
			PrivateMode:      "WHITELIST",
			GroupMode:        "bogus",
			PrivateWhitelist: []int64{3, 1, 3, 0, -5},
		},
	}
	out := normalizeConfig(in)
	if out.Access.PrivateMode != accessWhitelist {
		t.Fatalf("private mode = %q", out.Access.PrivateMode)
	}
	if out.Access.GroupMode != accessNone {
		t.Fatalf("group mode = %q", out.Access.GroupMode)
	}
	if len(out.Access.PrivateWhitelist) != 2 || out.Access.PrivateWhitelist[0] != 1 || out.Access.PrivateWhitelist[1] != 3 {
		t.Fatalf("whitelist = %v", out.Access.PrivateWhitelist)
	}
	if out.BanMaxDays != defaultBanMaxDays {
		t.Fatalf("ban max days = %d", out.BanMaxDays)
	}
}

func TestParseAtTargets(t *testing.T) {
	targets := parseAtTargets("[CQ:at,qq=123] [CQ:at,name=@abc,qq=456]")
	if len(targets) != 2 || targets[0] != 123 || targets[1] != 456 {
		t.Fatalf("targets = %v", targets)
	}
	if len(parseAtTargets("plain text")) != 0 {
		t.Fatal("plain text should have no targets")
	}
}

func TestStatsSnapshot(t *testing.T) {
	useTempStore(t)
	now := time.Now()
	_, _ = checkIn(1, 2, "a", now, 0, 1)
	_, _ = checkIn(1, 2, "a", now, 0, 1)
	_, _ = checkIn(1, 3, "b", now, 0, 1)
	setNoDeerUntil(1, 4, now.Add(time.Hour).Unix())
	setCanBeHelped(1, 5, false)
	stats := statsSnapshot(now)
	if stats.ActiveUsers != 2 || stats.TodayChecks != 3 || stats.MonthChecks != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.BannedUsers != 1 || stats.HelpDisabled != 1 {
		t.Fatalf("flag stats = %+v", stats)
	}
}

func TestRenderCalendarSmoke(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skip image render in CI")
	}
	if err := ensureFonts(); err != nil {
		t.Skipf("font unavailable: %v", err)
	}
	now := time.Now()
	img, err := renderCalendar(now, map[int]int{1: 1, now.Day(): 1000}, "测试用户", nil)
	if err != nil {
		t.Fatalf("renderCalendar: %v", err)
	}
	if len(img) == 0 {
		t.Fatal("empty calendar image")
	}
	rank, err := renderRank([]rankRow{{Name: "第一名", Count: 42}, {Name: "second", Count: 7}})
	if err != nil {
		t.Fatalf("renderRank: %v", err)
	}
	if len(rank) == 0 {
		t.Fatal("empty rank image")
	}
}
