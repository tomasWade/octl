// daily.go 实现 "daily" 请求的时间窗口聚合：从 DB 派生窗口内的事实
// （新建 / 活跃 / 归档 / 僵尸），叠加 daemon 内存态的卡住 session，
// 并按摘录名额规则截取文本素材。octl 只负责事实，叙事由消费方完成。
package daemon

import (
	"fmt"
	"sort"
	"time"

	"github.com/tomasWade/octl/internal/types"
)

// 日报聚合的可调参数。
const (
	// 单条摘录的 rune 上限：用户首条消息说清"要干什么"通常很短；
	// assistant 末条消息承载"干到什么程度"，给更宽的篇幅。
	dailyExcerptUserLimit      = 120
	dailyExcerptAssistantLimit = 500
	// 全局带文本摘录的 session 上限，按窗口内消息数降序分配；
	// 未进名额的 session 只带计数（HasExcerpt=false）。var 以便测试下调。
	dailyZombieIdleMs    = int64(48 * time.Hour / time.Millisecond)
	dailyZombieRecentMs  = int64(30 * 24 * time.Hour / time.Millisecond)
	dailyZombieListLimit = 20
)

// dailyExcerptSessionLimit 是全局带文本摘录的 session 上限（测试可下调，
// 避免真的种 100+ 个 session 验证名额）。
var dailyExcerptSessionLimit = 100

// buildDailyDigest 聚合 [from, to) 窗口内的活动事实。
func (sm *StateManager) buildDailyDigest(from, to int64) (*DailyDigest, error) {
	sessions, err := sm.db.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("daily: list sessions: %w", err)
	}
	projects, err := sm.db.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("daily: list projects: %w", err)
	}
	activity, err := sm.db.GetMessageActivity(from, to)
	if err != nil {
		return nil, fmt.Errorf("daily: message activity: %w", err)
	}

	digest := &DailyDigest{From: from, To: to}

	// 窗口内消息活动按 session 索引；消息表可能包含已从 session 表
	// 删除的孤儿消息，聚合时以 session 表为准跳过。
	actBySession := make(map[string]types.MessageActivity, len(activity))
	for _, a := range activity {
		actBySession[a.SessionID] = a
	}

	// 分类：新建 / 归档 / 活跃（窗口内有消息）。
	type activeEntry struct {
		session  types.Session
		activity types.MessageActivity
	}
	var (
		newSessions []types.Session
		archived    []types.Session
		active      []activeEntry
		sessionByID = make(map[string]types.Session, len(sessions))
	)
	for _, s := range sessions {
		sessionByID[s.ID] = s
		if s.TimeCreated >= from && s.TimeCreated < to {
			newSessions = append(newSessions, s)
		}
		if s.TimeArchived >= from && s.TimeArchived < to {
			archived = append(archived, s)
		}
		if a, ok := actBySession[s.ID]; ok {
			active = append(active, activeEntry{session: s, activity: a})
		}
	}

	// 摘录名额：全局按窗口内消息数降序取前 N。
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].activity.Count > active[j].activity.Count
	})
	excerptCap := len(active)
	if excerptCap > dailyExcerptSessionLimit {
		excerptCap = dailyExcerptSessionLimit
	}
	excerptIDs := make(map[string]bool, excerptCap)
	for i, e := range active {
		if i >= dailyExcerptSessionLimit {
			break
		}
		excerptIDs[e.session.ID] = true
	}

	// 活跃 session 的 DailySession 视图（带可选摘录）。
	activeViews := make([]DailySession, len(active))
	for i, e := range active {
		v := DailySession{
			SessionID:     e.session.ID,
			Title:         e.session.Title,
			MsgCount:      e.activity.Count,
			FirstActivity: e.activity.FirstAt,
			LastActivity:  e.activity.LastAt,
		}
		if excerptIDs[e.session.ID] {
			firstUser, lastAssistant, err := sm.db.GetSessionExcerpts(e.session.ID, from, to)
			if err != nil {
				return nil, fmt.Errorf("daily: excerpts %s: %w", e.session.ID, err)
			}
			v.FirstUserExcerpt = truncateRunes(firstUser, dailyExcerptUserLimit)
			v.LastAssistantExcerpt = truncateRunes(lastAssistant, dailyExcerptAssistantLimit)
			v.HasExcerpt = true
		}
		activeViews[i] = v
	}
	digest.TotalActive = len(activeViews)
	for _, v := range activeViews {
		if v.HasExcerpt {
			digest.Excerpted++
		}
	}

	// 按 project 分组；只保留窗口内有任何事实（新/活跃/归档）的 project。
	projectByID := make(map[string]types.Project, len(projects))
	for _, p := range projects {
		projectByID[p.ID] = p
	}
	activeByProject := make(map[string][]DailySession)
	costByProject := make(map[string]float64)
	for i, e := range active {
		pid := e.session.ProjectID
		activeByProject[pid] = append(activeByProject[pid], activeViews[i])
		costByProject[pid] += e.session.Cost
	}
	newByProject := make(map[string][]types.Session)
	for _, s := range newSessions {
		newByProject[s.ProjectID] = append(newByProject[s.ProjectID], s)
	}
	archivedByProject := make(map[string][]types.Session)
	for _, s := range archived {
		archivedByProject[s.ProjectID] = append(archivedByProject[s.ProjectID], s)
	}

	projectActivity := make(map[string]int64)
	for pid, list := range activeByProject {
		for _, v := range list {
			if v.LastActivity > projectActivity[pid] {
				projectActivity[pid] = v.LastActivity
			}
		}
	}
	for pid, list := range newByProject {
		for _, s := range list {
			if s.TimeCreated > projectActivity[pid] {
				projectActivity[pid] = s.TimeCreated
			}
		}
	}
	for pid, list := range archivedByProject {
		for _, s := range list {
			if s.TimeArchived > projectActivity[pid] {
				projectActivity[pid] = s.TimeArchived
			}
		}
	}

	var projectIDs []string
	seenProject := map[string]bool{}
	for pid := range activeByProject {
		if !seenProject[pid] {
			seenProject[pid] = true
			projectIDs = append(projectIDs, pid)
		}
	}
	for pid := range newByProject {
		if !seenProject[pid] {
			seenProject[pid] = true
			projectIDs = append(projectIDs, pid)
		}
	}
	for pid := range archivedByProject {
		if !seenProject[pid] {
			seenProject[pid] = true
			projectIDs = append(projectIDs, pid)
		}
	}
	// 按窗口内最新活动时间降序；查不到 project 记录时回退 projectID 显示。
	sort.SliceStable(projectIDs, func(i, j int) bool {
		return projectActivity[projectIDs[i]] > projectActivity[projectIDs[j]]
	})

	for _, pid := range projectIDs {
		p := projectByID[pid]
		dp := DailyProject{
			ProjectID:      pid,
			Name:           projectName(p),
			Worktree:       p.Worktree,
			ActiveSessions: activeByProject[pid],
			SessionCostSum: costByProject[pid],
		}
		if dp.ActiveSessions == nil {
			dp.ActiveSessions = []DailySession{}
		}
		dp.NewSessions = sessionRefs(newByProject[pid])
		dp.ArchivedSessions = sessionRefs(archivedByProject[pid])
		digest.Projects = append(digest.Projects, dp)
	}

	// 僵尸：未归档、闲置超阈值、近期有过活动。按最近活动降序，限量。
	now := time.Now()
	var zombieCands []types.Session
	for _, s := range sessions {
		if s.TimeArchived != 0 || s.TimeUpdated <= 0 {
			continue
		}
		idleMs := now.UnixMilli() - s.TimeUpdated
		if idleMs >= dailyZombieIdleMs && idleMs <= dailyZombieRecentMs {
			zombieCands = append(zombieCands, s)
		}
	}
	sort.SliceStable(zombieCands, func(i, j int) bool {
		return zombieCands[i].TimeUpdated > zombieCands[j].TimeUpdated
	})
	for i, s := range zombieCands {
		if i >= dailyZombieListLimit {
			break
		}
		digest.Zombies = append(digest.Zombies, DailySessionRef{
			SessionID:    s.ID,
			Title:        s.Title,
			ProjectID:    s.ProjectID,
			LastActivity: s.TimeUpdated,
		})
	}

	// 卡住：daemon 内存态中 PERMISSION / ERROR 的 session。
	for _, st := range sm.currentSnapshot() {
		if st.Status == StatusPermission || st.Status == StatusError {
			digest.StuckStates = append(digest.StuckStates, st)
		}
	}

	// wire 契约：无 omitempty 的列表字段永远发 []，不发 null。
	if digest.Projects == nil {
		digest.Projects = []DailyProject{}
	}

	return digest, nil
}

// sessionRefs 把 session 列表转为轻量引用（新增 / 归档分类用）。
// 永远返回非 nil 切片：wire 契约里列表字段不发 null（消费方 len() 不设防）。
func sessionRefs(sessions []types.Session) []DailySessionRef {
	refs := make([]DailySessionRef, 0, len(sessions))
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].TimeCreated > sessions[j].TimeCreated
	})
	for _, s := range sessions {
		refs = append(refs, DailySessionRef{
			SessionID:   s.ID,
			Title:       s.Title,
			ProjectID:   s.ProjectID,
			TimeCreated: s.TimeCreated,
		})
	}
	return refs
}

// truncateRunes 按 rune 截断字符串到上限，超长时追加省略号。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
