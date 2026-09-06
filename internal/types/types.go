// Package types provides data model definitions for the opencode session manager.
package types

// Session 表示数据库中存储的一个 opencode 会话。
type Session struct {
	ID               string  `json:"id"`
	ProjectID        string  `json:"projectId"`
	ParentID         string  `json:"parentId"`
	Slug             string  `json:"slug"`
	Directory        string  `json:"directory"`
	Title            string  `json:"title"`
	Version          string  `json:"version"`
	Agent            string  `json:"agent"`
	Model            string  `json:"model"`
	Cost             float64 `json:"cost"`
	TokensInput      int64   `json:"tokensInput"`
	TokensOutput     int64   `json:"tokensOutput"`
	TokensReasoning  int64   `json:"tokensReasoning"`
	TokensCacheRead  int64   `json:"tokensCacheRead"`
	TokensCacheWrite int64   `json:"tokensCacheWrite"`
	// 在数据库中存储为 unix 毫秒整数
	TimeCreated int64 `json:"timeCreated"`
	// 在数据库中存储为 unix 毫秒整数
	TimeUpdated int64 `json:"timeUpdated"`
	// 在数据库中存储为 unix 毫秒整数
	TimeCompacting int64 `json:"timeCompacting"`
	// 在数据库中存储为 unix 毫秒整数
	TimeArchived int64  `json:"timeArchived"`
	Path         string `json:"path"`
	WorkspaceID  string `json:"workspaceId"`
	// MessageCount 是一个计算/聚合字段，不是 session 表中的直接列。
	MessageCount     int    `json:"messageCount"`
	SummaryAdditions int    `json:"summaryAdditions"`
	SummaryDeletions int    `json:"summaryDeletions"`
	SummaryFiles     int    `json:"summaryFiles"`
	SummaryDiffs     string `json:"summaryDiffs"`
}

// SessionStats 提供关于会话的聚合统计信息。
type SessionStats struct {
	TotalSessions        int     `json:"totalSessions"`
	ActiveSessions       int     `json:"activeSessions"`
	TotalCost            float64 `json:"totalCost"`
	TotalTokensInput     int64   `json:"totalTokensInput"`
	TotalTokensOutput    int64   `json:"totalTokensOutput"`
	TotalTokensReasoning int64   `json:"totalTokensReasoning"`
	TotalTokensCacheRead int64   `json:"totalTokensCacheRead"`
}

// ModelUsage 表示特定模型的用量统计信息。
type ModelUsage struct {
	ModelID    string  `json:"modelId"`
	ProviderID string  `json:"providerId"`
	TokenCount int64   `json:"tokenCount"`
	Cost       float64 `json:"cost"`
}

// Project 表示数据库中的一个 opencode 项目。
type Project struct {
	ID          string `json:"id"`          // "global" 或 git 根提交哈希
	Worktree    string `json:"worktree"`    // worktree 的绝对路径
	Vcs         string `json:"vcs"`         // "git" 或 ""
	Name        string `json:"name"`        // 项目名称（可能为空）
	TimeCreated int64  `json:"timeCreated"` // unix 毫秒
	TimeUpdated int64  `json:"timeUpdated"` // unix 毫秒
}

// MessagePart 表示会话中的单个消息部分。
type MessagePart struct {
	PartID      string `json:"partId"`
	MessageID   string `json:"messageId"`
	SessionID   string `json:"sessionId"`
	Role        string `json:"role"`
	Agent       string `json:"agent"`
	ModelInfo   string `json:"modelInfo"` // JSON 编码的模型对象
	Text        string `json:"text"`
	TimeCreated int64  `json:"timeCreated"`
}

// MessageActivity 表示一个 session 在时间窗口内的消息活动聚合，
// 供日报（daily digest）使用。
type MessageActivity struct {
	SessionID string `json:"sessionId"`
	Count     int    `json:"count"`   // 窗口内消息数
	FirstAt   int64  `json:"firstAt"` // 窗口内首条消息时间，unix 毫秒
	LastAt    int64  `json:"lastAt"`  // 窗口内末条消息时间，unix 毫秒
}

// SkeletonEntry 用户消息骨架的一条：消息时间（unix 毫秒）与文本
// （该消息全部 text part 按序拼接，已截断）。供日报底片/讣告落盘消费。
type SkeletonEntry struct {
	TimeMs int64  `json:"timeMs"`
	Text   string `json:"text"`
}
