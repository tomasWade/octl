// daily.go 实现 "daily" 请求的时间窗口聚合：从数据源（影子库优先，
// 未接入时退回 opencode DB）派生窗口内的事实（新建 / 活跃 / 归档 /
// 删除 / 僵尸），叠加 daemon 内存态的卡住 session，并按摘录名额规则
// 截取文本素材。octl 只负责事实，叙事由消费方完成。
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
	// 僵尸判定：闲置超 48h 且 30 天内动过的未归档 session。
	dailyZombieIdleMs    = int64(48 * time.Hour / time.Millisecond)
	dailyZombieRecentMs  = int64(30 * 24 * time.Hour / time.Millisecond)
	dailyZombieListLimit = 20
)

// dailyExcerptSessionLimit 是全局带文本摘录的 session 上限（测试可下调，
// 避免真的种 100+ 个 session 验证名额）。
var dailyExcerptSessionLimit = 100

// dailySession 是聚合用的 session 载体：opencode 元数据 + 影子库生命
// 周期标记（deletedAtMs=0 表示活着）。
type dailySession struct {
	s           types.Session
	deletedAtMs int64
}

// dailySource 是 buildDailyDigest 的数据源抽象：影子库路径与旧 DB 路径
// 共用同一套聚合逻辑。
type dailySource struct {
	sessions []dailySession
	activity []types.MessageActivity
	excerpts func(sessionID string, from, to int64) (firstUser, lastAssistant string, err error)
}

// dailySourceOf 选择数据源：接入影子库时以它为准（含已删 session，
// 日报因此能看到被删线的当日贡献）；否则退回 opencode DB 旧路径。
func (sm *StateManager) dailySourceOf(from, to int64) (*dailySource, error) {
	if sm.shadowDB != nil {
		rows, err := sm.shadowDB.ListSessions()
		if err != nil {
			return nil, fmt.Errorf("daily: shadow list sessions: %w", err)
		}
		sessions := make([]dailySession, len(rows))
		for i, r := range rows {
			sessions[i] = dailySession{
				s: types.Session{
					ID:           r.ID,
					Title:        r.Title,
					ProjectID:    r.ProjectID,
					Directory:    r.Directory,
					Cost:         r.Cost,
					TimeCreated:  r.CreatedMs,
					TimeUpdated:  r.UpdatedMs,
					TimeArchived: r.ArchivedMs,
					MessageCount: int(r.MsgCount),
				},
				deletedAtMs: r.DeletedAtMs,
			}
		}
		activity, err := sm.shadowDB.MessageActivity(from, to)
		if err != nil {
			return nil, fmt.Errorf("daily: shadow message activity: %w", err)
		}
		return &dailySource{
			sessions: sessions,
			activity: activity,
			excerpts: sm.shadowDB.SessionExcerpts,
		}, nil
	}

	sessions, err := sm.db.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("daily: list sessions: %w", err)
	}
	converted := make([]dailySession, len(sessions))
	for i, s := range sessions {
		converted[i] = dailySession{s: s}
	}
	activity, err := sm.db.GetMessageActivity(from, to)
	if err != nil {
		return nil, fmt.Errorf("daily: message activity: %w", err)
	}
	return &dailySource{
		sessions: converted,
		activity: activity,
		excerpts: sm.db.GetSessionExcerpts,
	}, nil
}

// buildDailyDigest 聚合 [from, to) 窗口内的活动事实。
func (sm *StateManager) buildDailyDigest(from, to int64) (*DailyDigest, error) {
	src, err := sm.dailySourceOf(from, to)
	if err != nil {
		return nil, err
	}
	projects, err := sm.db.GetAllProjects()
	if err != nil {
		return nil, fmt.Errorf("daily: list projects: %w", err)
	}

	digest := &DailyDigest{From: from, To: to}

	// 窗口内消息活动按 session 索引。影子库路径下消息表与 session 表
	// 同源（都在影子库），孤儿消息天然不存在；旧路径沿用"以 session
	// 表为准跳过孤儿"的行为。
	actBySession := make(map[string]types.MessageActivity, len(src.activity))
	for _, a := range src.activity {
		actBySession[a.SessionID] = a
	}

	// 分类：新建 / 归档 / 删除 / 活跃（窗口内有消息）。
	type activeEntry struct {
		session  dailySession
		activity types.MessageActivity
	}
	var (
		newSessions []dailySession
		archived    []dailySession
		deleted     []dailySession
		active      []activeEntry
	)
	for _, ds := range src.sessions {
		s := ds.s
		if s.TimeCreated >= from && s.TimeCreated < to {
			newSessions = append(newSessions, ds)
		}
		if s.TimeArchived >= from && s.TimeArchived < to {
			archived = append(archived, ds)
		}
		if ds.deletedAtMs >= from && ds.deletedAtMs < to {
			deleted = append(deleted, ds)
		}
		if a, ok := actBySession[s.ID]; ok {
			active = append(active, activeEntry{session: ds, activity: a})
		}
	}

	// 摘录名额：全局按窗口内消息数降序取前 N。
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].activity.Count > active[j].activity.Count
	})
	excerptIDs := make(map[string]bool, len(active))
	for i, e := range active {
		if i >= dailyExcerptSessionLimit {
			break
		}
		excerptIDs[e.session.s.ID] = true
	}

	// 活跃 session 的 DailySession 视图（带可选摘录；被删线带标记）。
	activeViews := make([]DailySession, len(active))
	for i, e := range active {
		v := DailySession{
			SessionID:     e.session.s.ID,
			Title:         e.session.s.Title,
			MsgCount:      e.activity.Count,
			FirstActivity: e.activity.FirstAt,
			LastActivity:  e.activity.LastAt,
			Deleted:       e.session.deletedAtMs != 0,
			DeletedAtMs:   e.session.deletedAtMs,
		}
		if excerptIDs[e.session.s.ID] {
			firstUser, lastAssistant, err := src.excerpts(e.session.s.ID, from, to)
			if err != nil {
				return nil, fmt.Errorf("daily: excerpts %s: %w", e.session.s.ID, err)
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

	// 按 project 分组；只保留窗口内有任何事实（新/活跃/归档/删除）的 project。
	projectByID := make(map[string]types.Project, len(projects))
	for _, p := range projects {
		projectByID[p.ID] = p
	}
	activeByProject := make(map[string][]DailySession)
	costByProject := make(map[string]float64)
	for i, e := range active {
		pid := e.session.s.ProjectID
		activeByProject[pid] = append(activeByProject[pid], activeViews[i])
		costByProject[pid] += e.session.s.Cost
	}
	newByProject := make(map[string][]dailySession)
	for _, ds := range newSessions {
		newByProject[ds.s.ProjectID] = append(newByProject[ds.s.ProjectID], ds)
	}
	archivedByProject := make(map[string][]dailySession)
	for _, ds := range archived {
		archivedByProject[ds.s.ProjectID] = append(archivedByProject[ds.s.ProjectID], ds)
	}
	deletedByProject := make(map[string][]dailySession)
	for _, ds := range deleted {
		deletedByProject[ds.s.ProjectID] = append(deletedByProject[ds.s.ProjectID], ds)
	}

	projectActivity := make(map[string]int64)
	for pid, list := range activeByProject {
		for _, v := range list {
			last := v.LastActivity
			if v.DeletedAtMs > last {
				last = v.DeletedAtMs
			}
			if last > projectActivity[pid] {
				projectActivity[pid] = last
			}
		}
	}
	for _, m := range []map[string][]dailySession{newByProject, archivedByProject, deletedByProject} {
		for pid, list := range m {
			for _, ds := range list {
				t := ds.s.TimeCreated
				if ds.deletedAtMs > t {
					t = ds.deletedAtMs
				}
				if ds.s.TimeArchived > t {
					t = ds.s.TimeArchived
				}
				if t > projectActivity[pid] {
					projectActivity[pid] = t
				}
			}
		}
	}

	var projectIDs []string
	seenProject := map[string]bool{}
	collect := func(pid string) {
		if !seenProject[pid] {
			seenProject[pid] = true
			projectIDs = append(projectIDs, pid)
		}
	}
	for pid := range activeByProject {
		collect(pid)
	}
	for _, m := range []map[string][]dailySession{newByProject, archivedByProject, deletedByProject} {
		for pid := range m {
			collect(pid)
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
		dp.NewSessions = sessionRefs(dailyToPlain(newByProject[pid]))
		dp.ArchivedSessions = sessionRefs(dailyToPlain(archivedByProject[pid]))
		dp.DeletedSessions = deletedRefs(deletedByProject[pid])
		digest.Projects = append(digest.Projects, dp)
	}

	// 僵尸：未归档、未删除、闲置超阈值、近期有过活动。按最近活动降序，限量。
	now := time.Now()
	var zombieCands []dailySession
	for _, ds := range src.sessions {
		s := ds.s
		if s.TimeArchived != 0 || ds.deletedAtMs != 0 || s.TimeUpdated <= 0 {
			continue
		}
		idleMs := now.UnixMilli() - s.TimeUpdated
		if idleMs >= dailyZombieIdleMs && idleMs <= dailyZombieRecentMs {
			zombieCands = append(zombieCands, ds)
		}
	}
	sort.SliceStable(zombieCands, func(i, j int) bool {
		return zombieCands[i].s.TimeUpdated > zombieCands[j].s.TimeUpdated
	})
	for i, ds := range zombieCands {
		if i >= dailyZombieListLimit {
			break
		}
		digest.Zombies = append(digest.Zombies, DailySessionRef{
			SessionID:    ds.s.ID,
			Title:        ds.s.Title,
			ProjectID:    ds.s.ProjectID,
			LastActivity: ds.s.TimeUpdated,
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

// dailyToPlain 剥掉生命周期标记，还原 types.Session 列表。
func dailyToPlain(list []dailySession) []types.Session {
	out := make([]types.Session, 0, len(list))
	for _, ds := range list {
		out = append(out, ds.s)
	}
	return out
}

// deletedRefs 把窗口内删除的 session 转为引用列表（LastActivity 承载
// 删除时刻，消费方据此判断"删于窗口内"）。
func deletedRefs(list []dailySession) []DailySessionRef {
	refs := make([]DailySessionRef, 0, len(list))
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].deletedAtMs > list[j].deletedAtMs
	})
	for _, ds := range list {
		refs = append(refs, DailySessionRef{
			SessionID:    ds.s.ID,
			Title:        ds.s.Title,
			ProjectID:    ds.s.ProjectID,
			TimeCreated:  ds.s.TimeCreated,
			LastActivity: ds.deletedAtMs,
		})
	}
	return refs
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
