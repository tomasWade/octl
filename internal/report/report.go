// Package report 负责日报底片（raw）与讣告（deleted）的落盘渲染。
// 严格机械层：只搬运事实（统计行 + 元数据 + 用户消息骨架原文），
// 不做任何判断——蒸馏与叙事归日报 skill（LLM 层）。
//
// 文件语义（覆盖/追加）由调用方语义决定：
//   - WriteDailyRaw：date 整体覆盖，临时文件 + rename 原子替换
//   - AppendObituary：session 级追加，永不覆盖
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tomasWade/octl/internal/types"
)

// DefaultDir 是底片与讣告的默认存放目录。
const DefaultDir = "~/.local/share/opencode/daily"

// ExpandDir 展开 DefaultDir 中的 ~ 前缀为用户主目录。
func ExpandDir(dir string) (string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	return dir, nil
}

// DailyStats 是 raw 头部的统计行数据（机器可读注释 + 人读列表）。
type DailyStats struct {
	From, To     int64 // unix 毫秒，[From, To) 窗口
	Active       int
	New          int
	Archived     int
	Stuck        int
	Zombies      int
	ProjectCount int
}

// SessionRecord 是 raw 中一条 session 的记录单元，也用于讣告。
type SessionRecord struct {
	SessionID   string
	Title       string
	ProjectName string
	Directory   string
	MsgCount    int
	FirstMs     int64
	LastMs      int64
	// FirstUserExcerpt / LastAssistantExcerpt 来自 daily digest 的摘录
	//（空表示无）。讣告场景可留空。
	FirstUserExcerpt     string
	LastAssistantExcerpt string
	// Skeleton 用户消息骨架（raw 为窗口内、讣告为全史）。
	Skeleton []types.SkeletonEntry
}

// DailyRawPath 返回某日的 raw 文件路径。
func DailyRawPath(dir string, date time.Time) string {
	return filepath.Join(dir, date.Format("2006-01-02")+".raw.md")
}

// WriteDailyRaw 渲染并整体覆盖某日的 raw 底片。date 取本地时区自然日
// （仅用于文件名）。返回写入的文件路径。
func WriteDailyRaw(dir string, date time.Time, stats DailyStats, sessions []SessionRecord) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	var b strings.Builder
	writeRawBody(&b, date, stats, sessions)

	path := DailyRawPath(dir, date)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("rename %s: %w", path, err)
	}
	return path, nil
}

func writeRawBody(b *strings.Builder, date time.Time, stats DailyStats, sessions []SessionRecord) {
	fmt.Fprintf(b, "# raw %s\n\n", date.Format("2006-01-02"))
	// 机器可读统计行：日报 skill/脚本解析用（json 注释，人可忽略）。
	fmt.Fprintf(b, "<!-- stats: {\"from\":%d,\"to\":%d,\"active\":%d,\"new\":%d,\"archived\":%d,\"stuck\":%d,\"zombies\":%d,\"projects\":%d} -->\n\n",
		stats.From, stats.To, stats.Active, stats.New, stats.Archived, stats.Stuck, stats.Zombies, stats.ProjectCount)

	fmt.Fprintf(b, "- 活跃 %d · 新开 %d · 归档 %d · 卡住 %d · 僵尸 %d · 项目 %d\n",
		stats.Active, stats.New, stats.Archived, stats.Stuck, stats.Zombies, stats.ProjectCount)
	fmt.Fprintf(b, "- 窗口 [%s, %s)\n\n", fmtTime(stats.From), fmtTime(stats.To))

	if len(sessions) == 0 {
		b.WriteString("(no active sessions)\n")
		return
	}
	byProject := map[string][]SessionRecord{}
	var order []string
	for _, s := range sessions {
		p := s.ProjectName
		if _, ok := byProject[p]; !ok {
			order = append(order, p)
		}
		byProject[p] = append(byProject[p], s)
	}
	for _, p := range order {
		fmt.Fprintf(b, "## %s\n\n", p)
		for _, s := range byProject[p] {
			writeSessionBlock(b, s, "15:04")
		}
	}
}

// writeSessionBlock 渲染单条 session 块。timeLayout 决定骨架时间戳的
// 粒度：raw 用 "15:04"（当日），讣告用 "01-02 15:04"（全史跨日）。
func writeSessionBlock(b *strings.Builder, s SessionRecord, timeLayout string) {
	title := s.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(b, "### %s | %s | %dmsg | %s ~ %s\n\n", s.SessionID, title, s.MsgCount, fmtTime(s.FirstMs), fmtTime(s.LastMs))
	if s.FirstUserExcerpt != "" {
		fmt.Fprintf(b, "- 首条: %s\n", oneLine(s.FirstUserExcerpt))
	}
	if s.LastAssistantExcerpt != "" {
		fmt.Fprintf(b, "- 末条: %s\n", oneLine(s.LastAssistantExcerpt))
	}
	if len(s.Skeleton) > 0 {
		fmt.Fprintf(b, "- 骨架（%d 条用户消息）:\n", len(s.Skeleton))
		for _, e := range s.Skeleton {
			fmt.Fprintf(b, "  - %s %s\n", time.UnixMilli(e.TimeMs).Format(timeLayout), oneLine(e.Text))
		}
	} else {
		b.WriteString("- 骨架: (none)\n")
	}
	b.WriteString("\n")
}

// AppendObituary 向 deleted/<date>.md 追加一条 session 讣告（全史骨架）。
// O_APPEND 追加写，永不覆盖既有内容。
func AppendObituary(dir string, date time.Time, rec SessionRecord) error {
	d := filepath.Join(dir, "deleted")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", d, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s | %s | %s\n\n", rec.SessionID, rec.Title, rec.Directory)
	writeSessionBlock(&b, rec, "01-02 15:04")

	f, err := os.OpenFile(filepath.Join(d, date.Format("2006-01-02")+".md"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open obituary: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		return fmt.Errorf("append obituary: %w", err)
	}
	return nil
}

// fmtTime unix 毫秒 → "MM-DD HH:MM"（本地时区）；0 值渲染为 "-"。
func fmtTime(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}

// oneLine 把多行文本压成单行（骨架行内不允许换行）。
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\r\n", "\n")), " ")
}
