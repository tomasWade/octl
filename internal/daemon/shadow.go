// shadow.go 实现影子库的对账与保留期：事件当触发器（session.created /
// session.idle / session.deleted 定向对账），水位线增量对账当内容源与
// 完整性兜底（daemon 停机期的事件不可重放，靠 `>=` 水位重扫补齐）。
// 删除永不删行：三来源（实时事件 / tombstone 确认 / octl 删除 action）
// 统一到 shadow.MarkDeleted。
package daemon

import (
	"log"
	"strconv"
	"time"

	"github.com/tomasWade/octl/internal/db"
	"github.com/tomasWade/octl/internal/shadow"
	"github.com/tomasWade/octl/internal/types"
)

// shadowSyncInterval 是增量对账周期。对账按水位线只拉增量，空转近零成本；
// 周期只决定"事件触发之外的最大滞后"，5 分钟绰绰有余。
const shadowSyncInterval = 5 * time.Minute

// shadowUpsertSessionRows 是镜像核心：upsert + 条件复活。两个入口共用：
//   - shadowUpsertDBRows：水位对账路径（db.ShadowSessionRow 投影）
//   - shadowUpsertLiveSessions：30s DB 同步路径（types.Session 全量新鲜行）
//     ——归档（opencode 的 setArchived 只写 time_archived 不 bump
//     time_updated）和改标题等不触发水位的字段变化靠这条路径收敛。
func (sm *StateManager) shadowUpsertSessionRows(rows []shadow.SessionRow) {
	if sm.shadowDB == nil || len(rows) == 0 {
		return
	}
	if err := sm.shadowDB.UpsertSessions(rows); err != nil {
		log.Printf("[daemon] shadow upsert sessions: %v", err)
		return
	}
	if _, err := sm.shadowDB.ResurrectIfNewer(rows); err != nil {
		log.Printf("[daemon] shadow resurrect: %v", err)
	}
}

func (sm *StateManager) shadowUpsertDBRows(sessions []db.ShadowSessionRow) {
	if len(sessions) == 0 {
		return
	}
	rows := make([]shadow.SessionRow, len(sessions))
	for i, s := range sessions {
		rows[i] = shadow.SessionRow{
			ID: s.ID, Title: s.Title, ProjectID: s.ProjectID, Directory: s.Directory,
			CreatedMs: s.CreatedMs, UpdatedMs: s.UpdatedMs, ArchivedMs: s.ArchivedMs,
			MsgCount: s.MsgCount, Cost: s.Cost,
		}
	}
	sm.shadowUpsertSessionRows(rows)
}

func (sm *StateManager) shadowUpsertLiveSessions(sessions []types.Session) {
	if len(sessions) == 0 {
		return
	}
	rows := make([]shadow.SessionRow, len(sessions))
	for i, s := range sessions {
		rows[i] = shadow.SessionRow{
			ID: s.ID, Title: s.Title, ProjectID: s.ProjectID, Directory: s.Directory,
			CreatedMs: s.TimeCreated, UpdatedMs: s.TimeUpdated, ArchivedMs: s.TimeArchived,
			MsgCount: int64(s.MessageCount), Cost: s.Cost,
		}
	}
	sm.shadowUpsertSessionRows(rows)
}

// shadowReconcile 单轮增量对账：session / message / part 三条水位线各自
// 增量拉取，镜像进影子库后推进水位。任何一步失败整轮失败、水位不动，
// 下轮重扫（`>=` + 幂等 upsert，重放无害）。
//
// part 为什么需要独立水位：opencode 里 part.time_updated 可能晚于
// message.time_updated（流式收尾时部件还会更新而消息行不再 bump），
// 只扫消息水位会永远错过这些迟到的部件。
//
// 并发安全：本函数与定向对账（事件 goroutine）可能交错。影子库 upsert
// 是单调的（旧快照不覆盖新），交错最坏导致一次冗余写，不会回退数据。
func (sm *StateManager) shadowReconcile() error {
	if sm.shadowDB == nil {
		return nil
	}

	// session 增量
	swm := sm.shadowDB.GetWatermark("session")
	sessions, err := sm.db.SessionsUpdatedSince(swm)
	if err != nil {
		return err
	}
	if len(sessions) > 0 {
		sm.shadowUpsertDBRows(sessions)
		for _, s := range sessions {
			if s.UpdatedMs > swm {
				swm = s.UpdatedMs
			}
		}
		if err := sm.shadowDB.SetWatermark("session", swm); err != nil {
			return err
		}
	}

	// message 增量（元数据 + 触发部件抓取）
	mwm := sm.shadowDB.GetWatermark("message")
	msgs, err := sm.db.MessagesUpdatedSince(mwm)
	if err != nil {
		return err
	}
	msgByID := make(map[string]shadow.MessageRow, len(msgs))
	var mrows []shadow.MessageRow
	var ids []string
	affectedSessions := make(map[string]struct{})
	for _, m := range msgs {
		if _, dup := msgByID[m.ID]; dup {
			continue
		}
		msgByID[m.ID] = shadow.MessageRow{
			ID: m.ID, SessionID: m.SessionID, Role: m.Role, Agent: m.Agent,
			ModelJSON: m.ModelJSON, CreatedMs: m.CreatedMs, UpdatedMs: m.UpdatedMs,
		}
		ids = append(ids, m.ID)
		affectedSessions[m.SessionID] = struct{}{}
	}
	if len(mrows) == 0 {
		mrows = make([]shadow.MessageRow, 0, len(msgByID))
	}
	for _, r := range msgByID {
		mrows = append(mrows, r)
	}

	var prows []shadow.PartRow
	if len(ids) > 0 {
		parts, err := sm.db.TextPartsOfMessages(ids)
		if err != nil {
			return err
		}
		prows = make([]shadow.PartRow, len(parts))
		for i, p := range parts {
			prows[i] = shadow.PartRow{
				ID: p.ID, MessageID: p.MessageID, SessionID: p.SessionID,
				Kind: p.Kind, Text: p.Text, CreatedMs: p.CreatedMs, UpdatedMs: p.UpdatedMs,
			}
		}
	}
	if len(mrows) > 0 {
		if err := sm.shadowDB.UpsertMessages(mrows); err != nil {
			return err
		}
	}
	if len(prows) > 0 {
		if err := sm.shadowDB.UpsertParts(prows); err != nil {
			return err
		}
	}
	if len(msgs) > 0 {
		for _, m := range msgs {
			if m.UpdatedMs > mwm {
				mwm = m.UpdatedMs
			}
		}
		if err := sm.shadowDB.SetWatermark("message", mwm); err != nil {
			return err
		}
	}

	// part 增量（独立水位：抓消息水位路径错过的迟到部件）
	pwm := sm.shadowDB.GetWatermark("part")
	lateParts, err := sm.db.PartsUpdatedSince(pwm)
	if err != nil {
		return err
	}
	if len(lateParts) > 0 {
		lateMsgs := make([]shadow.MessageRow, 0, len(lateParts))
		lateProws := make([]shadow.PartRow, 0, len(lateParts))
		seenMsg := make(map[string]bool, len(lateParts))
		for _, lp := range lateParts {
			if !seenMsg[lp.Message.ID] {
				seenMsg[lp.Message.ID] = true
				lateMsgs = append(lateMsgs, shadow.MessageRow{
					ID: lp.Message.ID, SessionID: lp.Message.SessionID, Role: lp.Message.Role,
					Agent: lp.Message.Agent, ModelJSON: lp.Message.ModelJSON,
					CreatedMs: lp.Message.CreatedMs, UpdatedMs: lp.Message.UpdatedMs,
				})
				affectedSessions[lp.Message.SessionID] = struct{}{}
			}
			lateProws = append(lateProws, shadow.PartRow{
				ID: lp.Part.ID, MessageID: lp.Part.MessageID, SessionID: lp.Part.SessionID,
				Kind: lp.Part.Kind, Text: lp.Part.Text, CreatedMs: lp.Part.CreatedMs, UpdatedMs: lp.Part.UpdatedMs,
			})
		}
		if err := sm.shadowDB.UpsertMessages(lateMsgs); err != nil {
			return err
		}
		if err := sm.shadowDB.UpsertParts(lateProws); err != nil {
			return err
		}
		for _, lp := range lateParts {
			if lp.Part.UpdatedMs > pwm {
				pwm = lp.Part.UpdatedMs
			}
		}
		if err := sm.shadowDB.SetWatermark("part", pwm); err != nil {
			return err
		}
	}

	if len(affectedSessions) > 0 {
		sids := make([]string, 0, len(affectedSessions))
		for id := range affectedSessions {
			sids = append(sids, id)
		}
		if err := sm.shadowDB.RefreshMsgCounts(sids); err != nil {
			return err
		}
	}
	return nil
}

// shadowReconcileNow 是对账的日志包装入口（周期与启动时调用）。
func (sm *StateManager) shadowReconcileNow() {
	if sm.shadowDB == nil {
		return
	}
	if err := sm.shadowReconcile(); err != nil {
		log.Printf("[daemon] shadow reconcile: %v", err)
	}
}

// shadowReconcileSessions 定向对账：只抢救给定 session（及其消息/部件）。
// 触发点：session.created（新生即镜像，压缩"创建后秒删"窗口）、
// session.idle（回合结束即定格成果）、session.deleted（删除前最后一搏，
// opencode 先删 DB 再广播事件，此处大概率扑空，但扑空无害）。
func (sm *StateManager) shadowReconcileSessions(ids []string) {
	if sm.shadowDB == nil || len(ids) == 0 {
		return
	}
	sessions, err := sm.db.SessionsByIDs(ids)
	if err != nil {
		log.Printf("[daemon] shadow targeted sessions: %v", err)
		return
	}
	if len(sessions) > 0 {
		rows := make([]shadow.SessionRow, len(sessions))
		for i, s := range sessions {
			rows[i] = shadow.SessionRow{
				ID: s.ID, Title: s.Title, ProjectID: s.ProjectID, Directory: s.Directory,
				CreatedMs: s.CreatedMs, UpdatedMs: s.UpdatedMs, ArchivedMs: s.ArchivedMs,
				MsgCount: s.MsgCount, Cost: s.Cost,
			}
		}
		if err := sm.shadowDB.UpsertSessions(rows); err != nil {
			log.Printf("[daemon] shadow targeted sessions: %v", err)
			return
		}
	}
	msgs, err := sm.db.MessagesOfSessions(ids)
	if err != nil {
		log.Printf("[daemon] shadow targeted messages: %v", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	mids := make([]string, len(msgs))
	mrows := make([]shadow.MessageRow, len(msgs))
	for i, m := range msgs {
		mids[i] = m.ID
		mrows[i] = shadow.MessageRow{
			ID: m.ID, SessionID: m.SessionID, Role: m.Role, Agent: m.Agent,
			ModelJSON: m.ModelJSON, CreatedMs: m.CreatedMs, UpdatedMs: m.UpdatedMs,
		}
	}
	parts, err := sm.db.TextPartsOfMessages(mids)
	if err != nil {
		log.Printf("[daemon] shadow targeted parts: %v", err)
		return
	}
	prows := make([]shadow.PartRow, len(parts))
	for i, p := range parts {
		prows[i] = shadow.PartRow{
			ID: p.ID, MessageID: p.MessageID, SessionID: p.SessionID,
			Kind: p.Kind, Text: p.Text, CreatedMs: p.CreatedMs, UpdatedMs: p.UpdatedMs,
		}
	}
	if err := sm.shadowDB.UpsertMessages(mrows); err != nil {
		log.Printf("[daemon] shadow targeted messages: %v", err)
		return
	}
	if err := sm.shadowDB.UpsertParts(prows); err != nil {
		log.Printf("[daemon] shadow targeted parts: %v", err)
		return
	}
	if err := sm.shadowDB.RefreshMsgCounts(ids); err != nil {
		log.Printf("[daemon] shadow targeted counts: %v", err)
	}
}

// shadowMarkDeleted 标记删除（三来源统一入口）。
func (sm *StateManager) shadowMarkDeleted(ids []string) {
	if sm.shadowDB == nil || len(ids) == 0 {
		return
	}
	if err := sm.shadowDB.MarkDeleted(ids, time.Now().UnixMilli()); err != nil {
		log.Printf("[daemon] shadow mark deleted: %v", err)
	}
}

// shadowRetentionSweep 保留期清理：retentionDays <= 0 表示永不过期。
// 实际删除至少间隔 24h（meta 节流），cutoff 按 max(最后活动, 删除时刻)。
// 仍活在 opencode 里的线即使闲置超期也不清——保留期只约束档案；
// 清掉活线会让"恢复使用"后的镜像历史残缺（旧消息已过水位，不会重灌）。
func (sm *StateManager) shadowRetentionSweep(now time.Time) {
	if sm.shadowDB == nil || sm.retentionDays <= 0 {
		return
	}
	if last, ok := sm.shadowDB.GetMeta("retention_last_run"); ok {
		if lastMs, err := strconv.ParseInt(last, 10, 64); err == nil {
			if now.UnixMilli()-lastMs < int64(24*time.Hour/time.Millisecond) {
				return
			}
		}
	}
	alive := make(map[string]struct{})
	if live, err := sm.db.ListSessions(); err == nil {
		for _, s := range live {
			alive[s.ID] = struct{}{}
		}
	} else {
		log.Printf("[daemon] shadow retention: skip sweep, list sessions: %v", err)
		return
	}
	cutoff := now.AddDate(0, 0, -sm.retentionDays).UnixMilli()
	n, err := sm.shadowDB.PurgeOlderThan(cutoff, alive)
	if err != nil {
		log.Printf("[daemon] shadow retention: %v", err)
		return
	}
	if err := sm.shadowDB.SetMeta("retention_last_run", strconv.FormatInt(now.UnixMilli(), 10)); err != nil {
		log.Printf("[daemon] shadow retention meta: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[daemon] shadow retention: purged %d session(s) older than %dd", n, sm.retentionDays)
	}
}
