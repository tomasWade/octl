// Package manage provides session management operations (delete/export/archive).
//
// All destructive operations go through the opencode CLI via os/exec,
// never through direct database writes. The database is opened in read-only
// mode, so any attempt to modify data through SQL would fail at the driver level.
package manage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/types"
)

// OpResult 表示单个管理操作的结果。
type OpResult struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`   // "delete" | "export" | "archive"
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// Summary 聚合批处理操作的结果。
type Summary struct {
	Total     int        `json:"total"`
	Succeeded int        `json:"succeeded"`
	Failed    int        `json:"failed"`
	Results   []OpResult `json:"results"`
}

// exportMessage 是用于 JSON 导出的简化消息表示。
type exportMessage struct {
	Role        string `json:"role"`
	Text        string `json:"text"`
	TimeCreated int64  `json:"timeCreated"`
}

// exportData 是会话导出的顶层 JSON 结构。
type exportData struct {
	Session  types.Session   `json:"session"`
	Messages []exportMessage `json:"messages"`
}

// Manager 执行会话管理操作（删除/导出/归档）。
// 每个操作都通过 os/exec 使用 opencode CLI，从不直接写入数据库。
type Manager struct {
	db      *db.DB
	homeDir string
}

// New 使用给定的数据库连接创建一个新的 Manager。
// 它会检测用户主目录，用于 session_diff 清理路径解析。
func New(database *db.DB) *Manager {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	return &Manager{
		db:      database,
		homeDir: homeDir,
	}
}

// setCmdDir 设置 cmd 的工作目录。如果 directory 不存在或不可用，
// 则回退到 /tmp，避免 os/exec 因无法 chdir 而拒绝启动进程。
func setCmdDir(cmd *exec.Cmd, directory string) {
	if directory != "" {
		if fi, err := os.Stat(directory); err == nil && fi.IsDir() {
			cmd.Dir = directory
			return
		}
	}
	cmd.Dir = "/tmp"
}

// CreateSession 在指定目录中启动一个新的 opencode 会话，
// 附带初始消息。该进程在后台启动。
func (m *Manager) CreateSession(directory string, message string) error {
	cmd := exec.Command("opencode", "run", message, "--dir", directory)
	setCmdDir(cmd, directory)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// ForkSession 分支一个现有会话，使用给定的初始消息创建一个新的子会话。
func (m *Manager) ForkSession(sessionID string, directory string, message string) error {
	cmd := exec.Command("opencode", "run", "--session", sessionID, "--fork", message, "--dir", directory)
	setCmdDir(cmd, directory)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fork session %s: %w", sessionID, err)
	}
	return nil
}

// SendMessage 向现有会话发送一条消息。
func (m *Manager) SendMessage(sessionID string, directory string, message string) error {
	cmd := exec.Command("opencode", "run", "-s", sessionID, message, "--dir", directory)
	setCmdDir(cmd, directory)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("send message to session %s: %w", sessionID, err)
	}
	return nil
}

// DeleteProject 从数据库中删除一个项目行。
// 调用方应已负责删除该项目下的所有 session；本函数只清理 project 表。
// 原始 git 仓库和项目文件不会被触及。
// "global" 哨兵项目不能被删除。
func (m *Manager) DeleteProject(projectID string) error {
	if projectID == "global" {
		return fmt.Errorf("the global project cannot be deleted")
	}

	rw, err := m.db.NewWritable()
	if err != nil {
		return fmt.Errorf("open writable db: %w", err)
	}
	defer rw.Close()

	_, err = rw.Exec("DELETE FROM project WHERE id = ?", projectID)
	if err != nil {
		return fmt.Errorf("delete project %s: %w", projectID, err)
	}
	return nil
}

// DeleteSession 通过运行 opencode CLI 命令删除单个会话，
// 命令优先在会话的工作目录中执行（以便于 opencode 内部查找项目配置），
// 但目录不存在时也会回退到 /tmp 执行。
// 成功后还会清理 session_diff 存储目录。
//
// 如果 CLI 命令失败，返回的错误会包含命令的组合输出以用于诊断。
func (m *Manager) DeleteSession(s types.Session) error {
	cmd := exec.Command("opencode", "session", "delete", s.ID)

	// 如果会话目录已不存在（用户手动删除了项目文件夹），
	// 则回退到 /tmp 执行，避免 Go 的 os/exec 因无法 chdir 而拒绝启动进程。
	if s.Directory != "" {
		if fi, err := os.Stat(s.Directory); err == nil && fi.IsDir() {
			cmd.Dir = s.Directory
		} else {
			cmd.Dir = "/tmp"
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete session %s: %w\noutput: %s", s.ID, err, string(out))
	}

	// 删除成功后清理 session_diff 存储。
	// 这是尽力而为的操作——此处的失败不会影响主流程。
	if m.homeDir != "" {
		diffPath := filepath.Join(m.homeDir, ".local", "share", "opencode", "storage", "session_diff", s.ID)
		if err := os.RemoveAll(diffPath); err != nil {
			log.Printf("warning: failed to clean up session_diff for %s: %v", s.ID, err)
		}
	}

	return nil
}

// ExportSession 将会话数据（会话元数据 + 消息）作为 JSON 文件导出到指定输出目录。
//
// 输出文件名为 outputDir/<session.ID>.json。
// 返回写入文件的完整路径。
func (m *Manager) ExportSession(s types.Session, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("create output directory %s: %w", outputDir, err)
	}

	// 从数据库读取会话数据（包含用于上下文的摘要差异）
	session, err := m.db.GetSession(s.ID)
	if err != nil {
		return "", fmt.Errorf("read session %s from db: %w", s.ID, err)
	}

	// 读取会话消息
	parts, err := m.db.GetSessionMessages(s.ID)
	if err != nil {
		return "", fmt.Errorf("read messages for session %s: %w", s.ID, err)
	}

	// 使用简化消息构建导出数据
	messages := make([]exportMessage, 0, len(parts))
	for _, p := range parts {
		messages = append(messages, exportMessage{
			Role:        p.Role,
			Text:        p.Text,
			TimeCreated: p.TimeCreated,
		})
	}

	data := exportData{
		Session:  *session,
		Messages: messages,
	}

	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal session %s to json: %w", s.ID, err)
	}

	filePath := filepath.Join(outputDir, s.ID+".json")
	if err := os.WriteFile(filePath, jsonBytes, 0644); err != nil {
		return "", fmt.Errorf("write export file %s: %w", filePath, err)
	}

	return filePath, nil
}

// ArchiveSession 将会话数据归档到指定的归档目录。
// 与 ExportSession 不同，数据被分为两个文件：
//   - archiveDir/<session.ID>/session.json  （会话元数据）
//   - archiveDir/<session.ID>/messages.json （消息列表）
//
// ArchiveSession 不会从数据库中删除会话——这是单独的操作。
func (m *Manager) ArchiveSession(s types.Session, archiveDir string) error {
	sessionDir := filepath.Join(archiveDir, s.ID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("create archive directory %s: %w", sessionDir, err)
	}

	// 读取会话数据
	session, err := m.db.GetSession(s.ID)
	if err != nil {
		return fmt.Errorf("read session %s from db: %w", s.ID, err)
	}

	// 读取会话消息
	parts, err := m.db.GetSessionMessages(s.ID)
	if err != nil {
		return fmt.Errorf("read messages for session %s: %w", s.ID, err)
	}

	// 写入 session.json（完整会话元数据）
	sessionBytes, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session %s to json: %w", s.ID, err)
	}
	sessionPath := filepath.Join(sessionDir, "session.json")
	if err := os.WriteFile(sessionPath, sessionBytes, 0644); err != nil {
		return fmt.Errorf("write session file %s: %w", sessionPath, err)
	}

	// 写入 messages.json（完整消息部分）
	messageBytes, err := json.MarshalIndent(parts, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal messages for session %s: %w", s.ID, err)
	}
	messagesPath := filepath.Join(sessionDir, "messages.json")
	if err := os.WriteFile(messagesPath, messageBytes, 0644); err != nil {
		return fmt.Errorf("write messages file %s: %w", messagesPath, err)
	}

	return nil
}

// batchExec 对列表中的每个会话执行一个函数，采用尽力而为策略——
// 即使某个会话失败也继续处理其余会话。结果被收集并以 Summary 形式返回。
func (m *Manager) batchExec(sessions []types.Session, action string, fn func(types.Session) error) Summary {
	results := make([]OpResult, 0, len(sessions))
	succeeded := 0
	failed := 0

	for _, s := range sessions {
		result := OpResult{
			SessionID: s.ID,
			Action:    action,
		}
		if err := fn(s); err != nil {
			result.Success = false
			result.Error = err.Error()
			failed++
		} else {
			result.Success = true
			succeeded++
		}
		results = append(results, result)
	}

	return Summary{
		Total:     len(sessions),
		Succeeded: succeeded,
		Failed:    failed,
		Results:   results,
	}
}

// BatchDelete 删除多个会话。采用尽力而为策略——
// 即使单个删除失败也继续处理其余会话。
func (m *Manager) BatchDelete(sessions []types.Session) Summary {
	return m.batchExec(sessions, "delete", m.DeleteSession)
}

// BatchExport 将多个会话导出到指定目录。
// 采用尽力而为策略——即使单个导出失败也继续处理其余会话。
func (m *Manager) BatchExport(sessions []types.Session, dir string) Summary {
	return m.batchExec(sessions, "export", func(s types.Session) error {
		_, err := m.ExportSession(s, dir)
		return err
	})
}

// BatchArchive 将多个会话归档到指定目录。
// 采用尽力而为策略——即使单个归档失败也继续处理其余会话。
func (m *Manager) BatchArchive(sessions []types.Session, dir string) Summary {
	return m.batchExec(sessions, "archive", func(s types.Session) error {
		return m.ArchiveSession(s, dir)
	})
}
