// report.go：日报底片导出的 daemon 侧实现（按需）。
//
// 职责边界：本文件只做机械搬运——buildDailyDigest 聚合 + 逐线骨架拉取
// + report 包渲染落盘。判断（叙事、蒸馏）归日报 skill（LLM 层）。
//
// 触发路径：仅 "report" request（CLI `octl report` 手动导出）。周期落盘
// 与删除讣告已由影子库取代——影子库持续镜像全史且删除不丢行，底片随时
// 可从它再生，不再需要定时快照与临终抢救。
package daemon

import (
	"fmt"
	"log"
	"time"

	"github.com/tomasWade/octl/internal/manage"
	"github.com/tomasWade/octl/internal/report"
	"github.com/tomasWade/octl/internal/types"
)

// writeReport 生成 [from, to) 窗口的底片并整体覆盖落盘。
// dirOverride 为空时使用默认目录。date（文件名日期）取窗口起点的本地日期。
// 骨架数据源：影子库优先（含已删线的全史），未接入时退回 opencode DB。
func (sm *StateManager) writeReport(from, to int64, dirOverride string) (*ReportResult, error) {
	digest, err := sm.buildDailyDigest(from, to)
	if err != nil {
		return nil, fmt.Errorf("build digest: %w", err)
	}

	dir, err := report.ExpandDir(dirOverride)
	if err != nil {
		return nil, err
	}

	newCount, archivedCount := 0, 0
	for _, p := range digest.Projects {
		newCount += len(p.NewSessions)
		archivedCount += len(p.ArchivedSessions)
	}
	stats := report.DailyStats{
		From: from, To: to,
		Active: digest.TotalActive, New: newCount,
		Archived: archivedCount,
		Stuck:    len(digest.StuckStates), Zombies: len(digest.Zombies),
		ProjectCount: len(digest.Projects),
	}

	skeletonOf := sm.skeletonFn()

	var records []report.SessionRecord
	for _, p := range digest.Projects {
		for _, s := range p.ActiveSessions {
			skeleton, err := skeletonOf(s.SessionID, from, to)
			if err != nil {
				// 单线骨架失败不阻断整份底片：记日志，该线骨架留空。
				log.Printf("[daemon] report: skeleton %s: %v", s.SessionID, err)
				skeleton = nil
			}
			records = append(records, report.SessionRecord{
				SessionID:            s.SessionID,
				Title:                s.Title,
				ProjectName:          p.Name,
				MsgCount:             s.MsgCount,
				FirstMs:              s.FirstActivity,
				LastMs:               s.LastActivity,
				FirstUserExcerpt:     s.FirstUserExcerpt,
				LastAssistantExcerpt: s.LastAssistantExcerpt,
				Skeleton:             skeleton,
			})
		}
	}

	path, err := report.WriteDailyRaw(dir, time.UnixMilli(from), stats, records)
	if err != nil {
		return nil, err
	}
	return &ReportResult{
		Path: path, From: from, To: to,
		Active: digest.TotalActive, New: newCount,
		Archived: archivedCount, Sessions: len(records),
	}, nil
}

// skeletonFn 返回骨架查询函数：影子库优先，未接入时退回 opencode DB。
func (sm *StateManager) skeletonFn() func(sessionID string, from, to int64) ([]types.SkeletonEntry, error) {
	if sm.shadowDB != nil {
		return sm.shadowDB.UserSkeleton
	}
	return sm.db.GetUserSkeleton
}

// shadowPurgeAction 处理 "purge"：影子库的真删除出口。只允许清理已从
// opencode 消失的 session（活库里的先正常 delete），防止"看起来删了、
// 实际 opencode 里还活着"的错觉。
func (sm *StateManager) shadowPurgeAction(action ActionMsg) manage.Summary {
	summary := manage.Summary{Results: []manage.OpResult{}}
	if sm.shadowDB == nil {
		summary.Total = len(action.SessionIDs)
		summary.Failed = len(action.SessionIDs)
		for _, id := range action.SessionIDs {
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: false,
				Error: "shadow db not attached",
			})
		}
		return summary
	}
	alive, err := sm.db.ListSessions()
	if err != nil {
		summary.Total = len(action.SessionIDs)
		summary.Failed = len(action.SessionIDs)
		for _, id := range action.SessionIDs {
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: false,
				Error: fmt.Sprintf("list sessions: %v", err),
			})
		}
		return summary
	}
	aliveSet := make(map[string]struct{}, len(alive))
	for _, s := range alive {
		aliveSet[s.ID] = struct{}{}
	}
	summary.Total = len(action.SessionIDs)
	for _, id := range action.SessionIDs {
		if _, ok := aliveSet[id]; ok {
			summary.Failed++
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: false,
				Error: "session still alive in opencode; delete it first",
			})
			continue
		}
		n, err := sm.shadowDB.PurgeSessions([]string{id})
		switch {
		case err != nil:
			summary.Failed++
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: false,
				Error: fmt.Sprintf("shadow db: %v", err),
			})
		case n == 0:
			summary.Failed++
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: false,
				Error: "not found in shadow archive",
			})
		default:
			summary.Succeeded++
			summary.Results = append(summary.Results, manage.OpResult{
				SessionID: id, Action: "purge", Success: true,
			})
		}
	}
	return summary
}
