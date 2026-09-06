// report_test.go 覆盖底片落盘的 daemon 侧行为：writeReport 渲染落盘、
// 周期触发的节流与昨日补写、删除前讣告、wire 协议 "report" 方法。
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasWade/octl/internal/manage"
)

// TestWriteReport 验证落盘产物：文件名、统计头、骨架内容、目录覆盖参数。
func TestWriteReport(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "主线", 12000, 15000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "骨架第一条", 12000)
		dailyInsertMessage(wdb, "m2", "s1", "assistant", "回复", 13000)
		dailyInsertMessage(wdb, "m3", "s1", "user", "骨架第二条", 14000)
	})
	defer database.Close()

	sm := NewStateManager(database)
	dir := t.TempDir()
	res, err := sm.writeReport(10000, 20000, dir)
	if err != nil {
		t.Fatalf("writeReport: %v", err)
	}
	if res.Active != 1 || res.Sessions != 1 {
		t.Errorf("result = %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(dir, "1970-01-01.raw.md"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	out := string(b)
	for _, want := range []string{
		"# raw 1970-01-01",
		`"active":1`,
		"### s1 | 主线 | 3msg",
		"骨架第一条",
		"骨架第二条",
		"回复", // lastAssistantExcerpt
	} {
		if !strings.Contains(out, want) {
			t.Errorf("raw 缺少 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "骨架（2 条用户消息）") {
		t.Errorf("骨架应为 2 条用户消息（assistant 不计入）:\n%s", out)
	}
}

// TestMaybeWriteReports 节流语义：缺失即写、4h 内不重写、超 4h 覆盖、
// 昨日无活动不生成空底片。
func TestMaybeWriteReports(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "S1", 500, 15000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "hi", 15000)
	})
	defer database.Close()

	sm := NewStateManager(database)
	dir := t.TempDir()
	// TestMain 已把默认目录指向包级临时目录；本测试显式换到自己 的
	// TempDir 以便断言，结束后 defer 恢复到 TestMain 的注入值。
	origDefault := defaultReportDir()
	setDefaultReportDirForTest(dir)
	defer setDefaultReportDirForTest(origDefault)

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.Local)

	// 1) 缺失即写。
	sm.maybeWriteReports(now)
	todayPath := filepath.Join(dir, "2026-09-05.raw.md")
	if _, err := os.Stat(todayPath); err != nil {
		t.Fatalf("今日底片未生成: %v", err)
	}
	st1, _ := os.Stat(todayPath)

	// 2) 立刻再触发（模拟 10 分钟后）：4h 内不重写（mtime 不变）。
	later := now.Add(10 * time.Minute)
	os.Chtimes(todayPath, st1.ModTime().Add(-20*time.Minute), st1.ModTime().Add(-20*time.Minute))
	before, _ := os.Stat(todayPath)
	sm.maybeWriteReports(later)
	after, _ := os.Stat(todayPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("4h 内不应重写：mtime %v -> %v", before.ModTime(), after.ModTime())
	}

	// 3) 超 4h 覆盖写。
	os.Chtimes(todayPath, now.Add(-5*time.Hour), now.Add(-5*time.Hour))
	sm.maybeWriteReports(now)
	if after, _ = os.Stat(todayPath); !after.ModTime().After(now.Add(-5 * time.Hour)) {
		t.Errorf("超 4h 应覆盖写")
	}

	// 4) 昨日（9-04）在测试库里无消息（消息都在 1970）→ 不生成空底片。
	if _, err := os.Stat(filepath.Join(dir, "2026-09-04.raw.md")); !os.IsNotExist(err) {
		t.Errorf("昨日无活动不应生成底片")
	}
}

// TestDeleteWritesObituary 删除 action 前自动写全史讣告。
func TestDeleteWritesObituary(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "ses_doom", "global", "待删", 500, 15000, 0)
		dailyInsertMessage(wdb, "m1", "ses_doom", "user", "全史消息一", 600)
		dailyInsertMessage(wdb, "m2", "ses_doom", "user", "全史消息二", 15000)
	})
	defer database.Close()

	dir := t.TempDir()
	origDefault := defaultReportDir()
	setDefaultReportDirForTest(dir)
	defer setDefaultReportDirForTest(origDefault)

	sm := NewStateManager(database)
	sm.SetManager(manage.New(database))

	// 注意：测试库对 opencode CLI 不可见，CLI 删除会失败——但讣告在删除
	// 执行前已写，这正是"删除前定格"语义要验证的部分。
	summary := sm.handleDeleteAction(ActionMsg{Action: "delete", SessionIDs: []string{"ses_doom"}})
	_ = summary

	b, err := os.ReadFile(filepath.Join(dir, "deleted", time.Now().Format("2006-01-02")+".md"))
	if err != nil {
		t.Fatalf("讣告未生成: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "## ses_doom | 待删") {
		t.Errorf("讣告缺 session 头:\n%s", out)
	}
	for _, want := range []string{"全史消息一", "全史消息二"} {
		if !strings.Contains(out, want) {
			t.Errorf("讣告缺全史骨架 %q:\n%s", want, out)
		}
	}
}

// TestHandleRequest_Report wire 协议参数校验与正常应答。
func TestHandleRequest_Report(t *testing.T) {
	database := setupDBWithData(t, func(wdb *sql.DB) {
		wdb.Exec(`INSERT INTO project (id, worktree, vcs, name, time_created, time_updated)
			VALUES ('global', '/g', '', 'global', 1, 1000)`)
		dailyInsertSession(wdb, "s1", "global", "S1", 12000, 13000, 0)
		dailyInsertMessage(wdb, "m1", "s1", "user", "hi", 12000)
	})
	defer database.Close()

	sm := NewStateManager(database)
	dir := t.TempDir()

	t.Run("invalid window", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "report", ID: "r1", From: 20000, To: 10000})
		if !bytes.Contains(buf.Bytes(), []byte(`"ok":false`)) {
			t.Errorf("expected ok=false, got %q", buf.String())
		}
	})
	t.Run("valid with dir override", func(t *testing.T) {
		buf := &bytes.Buffer{}
		cl := &clientConn{enc: json.NewEncoder(buf)}
		sm.handleRequestMsg(cl, RequestMsg{Type: "request", Method: "report", ID: "r2", From: 10000, To: 20000, Dir: dir})
		if !bytes.Contains(buf.Bytes(), []byte(`"ok":true`)) {
			t.Errorf("expected ok=true, got %q", buf.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "1970-01-01.raw.md")); err != nil {
			t.Errorf("dir override 未生效: %v", err)
		}
	})
}
