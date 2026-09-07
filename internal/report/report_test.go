package report

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/types"
)

// timeSkeletonPattern 骨架行 "  - 15:04 文本" 的时间部分必须是数字。
var timeSkeletonPattern = regexp.MustCompile(`^  - \d{2}:\d{2} \S`)

func testDate() time.Time {
	return time.Date(2026, 9, 5, 0, 0, 0, 0, time.Local)
}

func TestWriteDailyRaw_Render(t *testing.T) {
	dir := t.TempDir()
	stats := DailyStats{From: 1000, To: 61000, Active: 2, New: 1, Archived: 0, Stuck: 1, Zombies: 0, ProjectCount: 1}
	sessions := []SessionRecord{
		{
			SessionID: "ses_a", Title: "主线", ProjectName: "global", MsgCount: 3,
			FirstMs: 2000, LastMs: 60000,
			FirstUserExcerpt:     "起点",
			LastAssistantExcerpt: "停靠点",
			Skeleton: []types.SkeletonEntry{
				{TimeMs: 2000, Text: "第一条 用户消息"},
				{TimeMs: 3000, Text: "第二条\n带换行"},
			},
		},
		{
			SessionID: "ses_b", Title: "", ProjectName: "global",
			Skeleton: nil, // 无骨架线
		},
	}
	path, err := WriteDailyRaw(dir, testDate(), stats, sessions)
	if err != nil {
		t.Fatalf("WriteDailyRaw: %v", err)
	}
	if filepath.Base(path) != "2026-09-05.raw.md" {
		t.Errorf("path base = %q, want 2026-09-05.raw.md", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := string(b)
	for _, want := range []string{
		"# raw 2026-09-05",
		`<!-- stats: {"from":1000,"to":61000,"active":2,"new":1,"archived":0,"stuck":1,"zombies":0,"projects":1} -->`,
		"- 活跃 2 · 新开 1 · 归档 0 · 卡住 1 · 僵尸 0 · 项目 1",
		"## global",
		"### ses_a | 主线 | 3msg",
		"- 首条: 起点",
		"- 末条: 停靠点",
		"- 骨架（2 条用户消息）:",
		"第一条 用户消息", // 骨架行含文本（时间部分随时区变化，不作断言）
		"第二条 带换行",  // 换行被压成空格（oneLine）
		"### ses_b | (untitled) | 0msg",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("raw 缺少 %q:\n%s", want, out)
		}
	}
	// 骨架时间戳必须是真实时间（\d{2}:\d{2}），不是 layout 字面量
	//（回归：曾把 "HH:MM" 当 layout 输出成字面文本）。
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  - ") {
			if !timeSkeletonPattern.MatchString(line) {
				t.Errorf("骨架行时间戳异常: %q", line)
			}
		}
	}
	// 无 .tmp 残留（原子写）。
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp 文件残留: %v", err)
	}
}

func TestWriteDailyRaw_Overwrite(t *testing.T) {
	dir := t.TempDir()
	stats := DailyStats{From: 1, To: 2, Active: 1}
	_, err := WriteDailyRaw(dir, testDate(), stats, nil)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	stats.Active = 99
	path, err := WriteDailyRaw(dir, testDate(), stats, nil)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"active":99`) {
		t.Errorf("覆盖写未生效：\n%s", b)
	}
}

func TestWriteDailyRaw_Empty(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteDailyRaw(dir, testDate(), DailyStats{From: 1, To: 2}, nil)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "(no active sessions)") {
		t.Errorf("空态缺失:\n%s", b)
	}
}

func TestExpandDir(t *testing.T) {
	if _, err := ExpandDir(""); err != nil {
		t.Fatalf("empty dir should use default: %v", err)
	}
	d, err := ExpandDir("/abs/path")
	if err != nil || d != "/abs/path" {
		t.Errorf("abs passthrough: %q %v", d, err)
	}
	home, _ := os.UserHomeDir()
	d, err = ExpandDir("~/x")
	if err != nil || d != filepath.Join(home, "x") {
		t.Errorf("tilde expand: %q %v", d, err)
	}
}
