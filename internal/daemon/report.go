// report.go：日报底片落盘的 daemon 侧实现。
//
// 职责边界：本文件只做机械搬运——buildDailyDigest 聚合 + 逐线骨架拉取
// + report 包渲染落盘。判断（叙事、蒸馏）归日报 skill（LLM 层）。
//
// 触发路径（全部收敛到 writeReport）：
//   - "report" request（CLI `octl report` 手动触发 / 日报 skill 叙事前刷新）
//   - daemon 周期检查（当日 raw 缺失或超过 reportInterval 未刷新 → 覆盖写）
//   - daemon 启动时补写昨日终版（昨日 raw 缺失或停在昨日内）
package daemon

import (
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/tomasWade/octl/internal/report"
)

// reportInterval 是 daemon 周期检查的最小间隔；当日 raw 的覆盖周期为
// 4 小时（文件 mtime 距今超过 4h 才重写）。
const (
	reportCheckInterval = 10 * time.Minute
	reportMaxAge        = 4 * time.Hour
)

// writeReport 生成 [from, to) 窗口的底片并整体覆盖落盘。
// dirOverride 为空时使用默认目录。date（文件名日期）取窗口起点的本地日期。
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

	var records []report.SessionRecord
	for _, p := range digest.Projects {
		for _, s := range p.ActiveSessions {
			skeleton, err := sm.db.GetUserSkeleton(s.SessionID, from, to)
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

// writeObituary 在删除 session 前写全史讣告。元数据查不到（如 session
// 已不在 DB）时跳过该条，返回 nil 不阻断删除流程。
func (sm *StateManager) writeObituary(sessionID, dirOverride string) error {
	sess, err := sm.db.GetSession(sessionID)
	if err != nil {
		return nil // nolint: 查不到元数据就无法归档，删除继续
	}
	skeleton, err := sm.db.GetUserSkeleton(sessionID, 0, math.MaxInt64)
	if err != nil {
		return fmt.Errorf("skeleton %s: %w", sessionID, err)
	}
	if dirOverride == "" {
		dirOverride = defaultReportDir()
	}
	dir, err := report.ExpandDir(dirOverride)
	if err != nil {
		return err
	}
	return report.AppendObituary(dir, time.Now(), report.SessionRecord{
		SessionID:   sess.ID,
		Title:       sess.Title,
		Directory:   sess.Directory,
		MsgCount:    sess.MessageCount,
		FirstMs:     sess.TimeCreated,
		LastMs:      sess.TimeUpdated,
		Skeleton:    skeleton,
		ProjectName: sess.ProjectID,
	})
}

// maybeWriteReports 是周期触发的落盘检查：当日 raw 缺失或超 4h 未刷新
// 则覆盖写；昨日终版缺失或停在昨日内则补写。失败只记日志。
func (sm *StateManager) maybeWriteReports(now time.Time) {
	// 当日：窗口 [今日00:00, now)。
	today := now.Local()
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	rawPath := report.DailyRawPath(defaultReportDir(), todayStart)
	if st, err := os.Stat(rawPath); err != nil || now.Sub(st.ModTime()) > reportMaxAge {
		if _, werr := sm.writeReport(todayStart.UnixMilli(), now.UnixMilli(), defaultReportDir()); werr != nil {
			log.Printf("[daemon] report: today: %v", werr)
		}
	}

	// 昨日：窗口 [昨00:00, 今00:00)。文件不存在且昨日无活动时跳过
	//（避免每天开机都生成空底片）；文件存在但停在昨日内则刷新到终版。
	yEnd := todayStart
	yStart := yEnd.AddDate(0, 0, -1)
	yPath := report.DailyRawPath(defaultReportDir(), yStart)
	st, err := os.Stat(yPath)
	if os.IsNotExist(err) {
		if digest, derr := sm.buildDailyDigest(yStart.UnixMilli(), yEnd.UnixMilli()); derr == nil && digest.TotalActive > 0 {
			if _, werr := sm.writeReport(yStart.UnixMilli(), yEnd.UnixMilli(), defaultReportDir()); werr != nil {
				log.Printf("[daemon] report: yesterday backfill: %v", werr)
			}
		}
	} else if err == nil && st.ModTime().Before(yEnd) {
		if _, werr := sm.writeReport(yStart.UnixMilli(), yEnd.UnixMilli(), defaultReportDir()); werr != nil {
			log.Printf("[daemon] report: yesterday refresh: %v", werr)
		}
	}
}

// reportDirOverride 供测试注入默认底片目录；空时用真实默认值。
var reportDirOverride string

// setDefaultReportDirForTest 注入/恢复默认底片目录（空串恢复默认）。
func setDefaultReportDirForTest(dir string) { reportDirOverride = dir }

// defaultReportDir 返回底片目录（测试注入值或展开的默认路径）。
func defaultReportDir() string {
	if reportDirOverride != "" {
		return reportDirOverride
	}
	d, err := report.ExpandDir("")
	if err != nil {
		return ""
	}
	return d
}
