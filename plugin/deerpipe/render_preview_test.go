package deerpipe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRenderPreview 输出样例图到 DEER_PREVIEW_DIR，仅本地调试用。
func TestRenderPreview(t *testing.T) {
	dir := os.Getenv("DEER_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set DEER_PREVIEW_DIR to render preview images")
	}
	if err := ensureFonts(); err != nil {
		t.Skipf("font unavailable: %v", err)
	}
	now := time.Now()
	records := map[int]int{}
	for day := 1; day <= now.Day(); day++ {
		if day%2 == 1 {
			records[day] = 1
		}
	}
	records[now.Day()] = 3
	img, err := renderCalendar(now, records, "测试🦌友", nil)
	if err != nil {
		t.Fatalf("renderCalendar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calendar.png"), img, 0644); err != nil {
		t.Fatal(err)
	}
	rank, err := renderRank([]rankRow{
		{Name: "第一名选手", Count: 66},
		{Name: "runner-up", Count: 42},
		{Name: "第三名", Count: 18},
		{Name: "四号", Count: 5},
		{Name: "五号", Count: 1},
	})
	if err != nil {
		t.Fatalf("renderRank: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rank.png"), rank, 0644); err != nil {
		t.Fatal(err)
	}
}
