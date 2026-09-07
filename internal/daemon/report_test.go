// report_test.go 覆盖底片导出的 daemon 侧行为：writeReport 渲染落盘、
// wire 协议 "report" 方法。周期落盘与删除讣告已由影子库取代（见
// shadow_test.go），相关测试随之退役。
package daemon

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
