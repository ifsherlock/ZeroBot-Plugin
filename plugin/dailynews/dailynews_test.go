package dailynews

import (
	"testing"
	"time"
)

func TestNormalizeScheduleMigratesClockToCron(t *testing.T) {
	task := normalizeSchedule(newsSchedule{
		ID:       "morning",
		SourceID: "60s",
		Target:   "group:123456",
		Time:     "08:30",
		Enabled:  true,
	}, map[string]newsSource{"60s": {ID: "60s", Encoding: "image-proxy"}})

	if task.Cron != "30 8 * * *" {
		t.Fatalf("cron = %q, want %q", task.Cron, "30 8 * * *")
	}
	if task.Time != "08:30" {
		t.Fatalf("time = %q, want %q", task.Time, "08:30")
	}
	if task.Format != "image" {
		t.Fatalf("format = %q, want image", task.Format)
	}
}

func TestCronScheduleMatches(t *testing.T) {
	now := time.Date(2026, 6, 8, 8, 30, 12, 0, time.Local) // Monday
	cases := []struct {
		name string
		cron string
		want bool
	}{
		{name: "daily", cron: "30 8 * * *", want: true},
		{name: "wrong minute", cron: "31 8 * * *", want: false},
		{name: "weekday range", cron: "30 8 * * 1-5", want: true},
		{name: "weekend only", cron: "30 8 * * 0,6", want: false},
		{name: "step", cron: "*/10 8 * * *", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scheduleMatches(newsSchedule{Cron: tc.cron}, now)
			if got != tc.want {
				t.Fatalf("scheduleMatches(%q) = %v, want %v", tc.cron, got, tc.want)
			}
		})
	}
}

func TestScheduleAlreadyRan(t *testing.T) {
	task := newsSchedule{Time: "08:30", Cron: "30 8 * * *", LastRun: "2026-06-08"}
	if !scheduleAlreadyRan(task, "2026-06-08 08:30", "2026-06-08") {
		t.Fatal("legacy date-only last_run should block migrated daily task")
	}

	task.LastRun = "2026-06-08 08:30"
	if !scheduleAlreadyRan(task, "2026-06-08 08:30", "2026-06-08") {
		t.Fatal("minute stamp should block repeated run in same minute")
	}

	task.LastRun = "2026-06-08 08:29"
	if scheduleAlreadyRan(task, "2026-06-08 08:30", "2026-06-08") {
		t.Fatal("different minute stamp should not block")
	}
}

func TestNormalizeConfigDeduplicatesSchedulesByID(t *testing.T) {
	cfg := defaultConfig()
	cfg.Schedules = []newsSchedule{
		{ID: "morning", SourceID: "60s", Target: "私聊:1", Cron: "30 8 * * *", Format: "image", Enabled: true},
		{ID: "morning", SourceID: "duanzi", Target: "私聊:1", Cron: "13 14 * * *", Format: "text", Enabled: true},
	}

	got := normalizeConfig(cfg)
	if len(got.Schedules) != 1 {
		t.Fatalf("schedule count = %d, want 1", len(got.Schedules))
	}
	task := got.Schedules[0]
	if task.SourceID != "duanzi" || task.Cron != "13 14 * * *" {
		t.Fatalf("schedule = %+v, want last task to win", task)
	}
}

func TestFormatJSONNewsUnwrapsAPIDataText(t *testing.T) {
	data := []byte(`{"code":200,"message":"获取成功。平台公告不应展示。","data":{"index":148,"duanzi":"真正的段子正文"}}`)
	got := formatJSONNews(data)
	if got != "真正的段子正文" {
		t.Fatalf("formatJSONNews = %q, want duanzi text", got)
	}
}

func TestFormatJSONNewsUnwrapsAPIDataNews(t *testing.T) {
	data := []byte(`{"code":200,"message":"获取成功。平台公告不应展示。","data":{"date":"2026-06-08","day_of_week":"星期一","lunar_date":"四月廿三","news":["第一条","第二条"],"tip":"每日一句"}}`)
	got := formatJSONNews(data)
	want := "2026-06-08 星期一 四月廿三\n1. 第一条\n2. 第二条\n\n每日一句"
	if got != want {
		t.Fatalf("formatJSONNews = %q, want %q", got, want)
	}
}
