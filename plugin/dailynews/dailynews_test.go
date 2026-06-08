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
