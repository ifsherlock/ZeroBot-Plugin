package deerpipe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// deerUser 与原插件 User + DeerRecord 对应，按 (群, 用户) 独立计数。
type deerUser struct {
	Name         string                    `json:"name,omitempty"`
	HelpDisabled bool                      `json:"help_disabled,omitempty"`
	NoDeerUntil  int64                     `json:"no_deer_until,omitempty"`
	Records      map[string]map[string]int `json:"records,omitempty"` // "2006-01" -> day -> count
}

type deerStore struct {
	LastMonth string               `json:"last_month,omitempty"`
	Users     map[string]*deerUser `json:"users"`
}

var (
	dataMu   sync.Mutex
	store    deerStore
	dataPath string
)

func userKey(groupID, userID int64) string {
	return strconv.FormatInt(groupID, 10) + ":" + strconv.FormatInt(userID, 10)
}

func monthKey(now time.Time) string {
	return now.Format("2006-01")
}

func loadData() {
	dataMu.Lock()
	defer dataMu.Unlock()
	store = deerStore{Users: map[string]*deerUser{}}
	data, err := os.ReadFile(dataPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logrus.Warnf("[deerpipe] load data failed: %v", err)
		}
		return
	}
	if err := json.Unmarshal(data, &store); err != nil {
		logrus.Warnf("[deerpipe] parse data failed: %v", err)
		store = deerStore{Users: map[string]*deerUser{}}
		return
	}
	if store.Users == nil {
		store.Users = map[string]*deerUser{}
	}
	cleanupLocked(time.Now())
}

func saveDataLocked() {
	if err := os.MkdirAll(filepath.Dir(dataPath), 0755); err != nil {
		logrus.Warnf("[deerpipe] create data dir failed: %v", err)
		return
	}
	data, err := json.MarshalIndent(&store, "", "  ")
	if err != nil {
		logrus.Warnf("[deerpipe] marshal data failed: %v", err)
		return
	}
	tmp := dataPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		logrus.Warnf("[deerpipe] write data failed: %v", err)
		return
	}
	if err := os.Rename(tmp, dataPath); err != nil {
		logrus.Warnf("[deerpipe] rename data failed: %v", err)
	}
}

// cleanupLocked 与原插件 cleanup 对应：清掉非当前月份的记录，删除无记录且无状态的用户。
func cleanupLocked(now time.Time) {
	current := monthKey(now)
	if store.LastMonth == current {
		return
	}
	for key, user := range store.Users {
		for month := range user.Records {
			if month != current {
				delete(user.Records, month)
			}
		}
		if len(user.Records) == 0 && !user.HelpDisabled && user.NoDeerUntil <= now.Unix() {
			delete(store.Users, key)
		}
	}
	store.LastMonth = current
	saveDataLocked()
}

func getOrCreateLocked(groupID, userID int64) *deerUser {
	key := userKey(groupID, userID)
	user := store.Users[key]
	if user == nil {
		user = &deerUser{}
		store.Users[key] = user
	}
	return user
}

// getUser 返回用户状态快照。
func getUser(groupID, userID int64) deerUser {
	dataMu.Lock()
	defer dataMu.Unlock()
	cleanupLocked(time.Now())
	if user := store.Users[userKey(groupID, userID)]; user != nil {
		return *user
	}
	return deerUser{}
}

// getRecords 返回当前月份 day -> count。
func getRecords(groupID, userID int64, now time.Time) map[int]int {
	dataMu.Lock()
	defer dataMu.Unlock()
	cleanupLocked(now)
	out := map[int]int{}
	user := store.Users[userKey(groupID, userID)]
	if user == nil {
		return out
	}
	for dayText, count := range user.Records[monthKey(now)] {
		if day, err := strconv.Atoi(dayText); err == nil {
			out[day] = count
		}
	}
	return out
}

// checkIn 签到。day 为 0 表示签今天（计数 +times，连发多个🦌一次签多次）；
// 否则补签指定日期（已签过则失败，times 无效恒记 1）。
func checkIn(groupID, userID int64, name string, now time.Time, day, times int) (ok bool, records map[int]int) {
	dataMu.Lock()
	defer dataMu.Unlock()
	cleanupLocked(now)
	user := getOrCreateLocked(groupID, userID)
	if name != "" {
		user.Name = name
	}
	month := monthKey(now)
	if user.Records == nil {
		user.Records = map[string]map[string]int{}
	}
	if user.Records[month] == nil {
		user.Records[month] = map[string]int{}
	}
	if times < 1 {
		times = 1
	}
	target := day
	if target == 0 {
		target = now.Day()
	}
	dayText := strconv.Itoa(target)
	_, exists := user.Records[month][dayText]
	switch {
	case day == 0:
		user.Records[month][dayText] += times
		ok = true
	case exists:
		ok = false
	default:
		user.Records[month][dayText] = 1
		ok = true
	}
	if ok {
		saveDataLocked()
	}
	records = map[int]int{}
	for text, count := range user.Records[month] {
		if d, err := strconv.Atoi(text); err == nil {
			records[d] = count
		}
	}
	return ok, records
}

func setCanBeHelped(groupID, userID int64, allowed bool) {
	dataMu.Lock()
	defer dataMu.Unlock()
	user := getOrCreateLocked(groupID, userID)
	user.HelpDisabled = !allowed
	saveDataLocked()
}

func setNoDeerUntil(groupID, userID int64, until int64) {
	dataMu.Lock()
	defer dataMu.Unlock()
	user := getOrCreateLocked(groupID, userID)
	user.NoDeerUntil = until
	saveDataLocked()
}

type rankItem struct {
	UserID int64
	Name   string
	Count  int
}

// topRank 当前月份群内签到总次数排行。
func topRank(groupID int64, now time.Time, limit int) []rankItem {
	dataMu.Lock()
	defer dataMu.Unlock()
	cleanupLocked(now)
	prefix := strconv.FormatInt(groupID, 10) + ":"
	month := monthKey(now)
	out := make([]rankItem, 0, 8)
	for key, user := range store.Users {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		total := 0
		for _, count := range user.Records[month] {
			total += count
		}
		if total <= 0 {
			continue
		}
		uid, err := strconv.ParseInt(key[len(prefix):], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, rankItem{UserID: uid, Name: user.Name, Count: total})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].UserID < out[j].UserID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// statsSnapshot 提供给 WebUI 的运行统计。
type deerStats struct {
	ActiveUsers  int `json:"active_users"`
	TodayChecks  int `json:"today_checks"`
	MonthChecks  int `json:"month_checks"`
	BannedUsers  int `json:"banned_users"`
	HelpDisabled int `json:"help_disabled"`
}

func statsSnapshot(now time.Time) deerStats {
	dataMu.Lock()
	defer dataMu.Unlock()
	month := monthKey(now)
	today := strconv.Itoa(now.Day())
	stats := deerStats{}
	for _, user := range store.Users {
		records := user.Records[month]
		if len(records) > 0 {
			stats.ActiveUsers++
		}
		for day, count := range records {
			stats.MonthChecks += count
			if day == today {
				stats.TodayChecks += count
			}
		}
		if user.NoDeerUntil > now.Unix() {
			stats.BannedUsers++
		}
		if user.HelpDisabled {
			stats.HelpDisabled++
		}
	}
	return stats
}
