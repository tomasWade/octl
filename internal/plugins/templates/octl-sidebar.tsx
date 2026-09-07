/** @jsxImportSource @opentui/solid */

const OCTL_PROTOCOL_VERSION = "{{OCTL_MD5}}";

import { appendFileSync } from "node:fs";
import { homedir } from "node:os";
import { exec } from "node:child_process";
import { promisify } from "node:util";
import { createConnection, type Socket } from "node:net";
import { createSignal, createMemo, onCleanup } from "solid-js";

const execAsync = promisify(exec);

const LOG_FILE = "/tmp/octl-sidebar.log";
const SOCKET_PATH = `${homedir()}/.local/share/opencode/octl.sock`;

export function truncate(str: string, max: number): string {
  if (str.length <= max) return str;
  return str.slice(0, max - 1) + "…";
}

export function formatRelativeTime(ts: number): string {
  const diff = Date.now() - ts;
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return "now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d`;
  const weeks = Math.floor(days / 7);
  return `${weeks}w`;
}

export function formatWorktree(path: string): string {
  if (!path) return "";
  const home = homedir();
  if (path.startsWith(home)) {
    return "~" + path.slice(home.length);
  }
  return path;
}

export function friendlyError(err: string): string {
  // 语义化错误（含中文）原样透传，避免被默认分支吞掉。
  if (/[\u4e00-\u9fff]/.test(err)) {
    return err;
  }
  if (err.includes("ENOENT") || err.includes("no such file")) {
    return "daemon not running";
  }
  if (err.includes("ECONNREFUSED")) {
    return "daemon not responding";
  }
  if (err.includes("old binary")) {
    return err;
  }
  if (err.includes("version mismatch")) {
    return err;
  }
  return "disconnected";
}

// 纯函数：判断 daemon 返回的错误是否为协议版本不一致（version mismatch / old binary）。
// sidebar 以此识别「需要重新生成插件并重启 opencode」的场景，停止无效重连。
export function isVersionMismatchError(err: string): boolean {
  return err.includes("version mismatch") || err.includes("old binary");
}

// 纯函数：返回指定 tab 的标题前景色。activeTab 为当前选中 tab，tab 为被查询的 tab。
// 选中 tab 高亮（加粗高亮色），未选中用弱化色。
export function tabTitleFg(activeTab: string, tab: string): string {
  if (activeTab === tab) return "#c0caf5";
  return "#565f89";
}

// 纯函数：切换 tab。返回新的 activeTab；传入无效 tab 时保持原值。
export function switchTab(activeTab: "all" | "favorites", tab: string): "all" | "favorites" {
  return tab === "all" || tab === "favorites" ? tab : activeTab;
}

// 纯函数：为新建 tmux session 生成合法名称。取 session title 直接命名，
// 清理特殊字符（仅保留字母数字与中文）后截取前 15 字；title 为空回退到
// 清理后的 sessionId，两者皆空返回 "unknown"。
export function makeTmuxSessionName(title: string, sessionId: string): string {
  let source = (title || "").trim();
  if (!source) {
    source = (sessionId || "").trim();
  }
  if (!source) {
    return "unknown";
  }

  const cleaned = source
    .replace(/[^a-zA-Z0-9\u4e00-\u9fa5]/g, "")
    .slice(0, 15);

  return cleaned || "unknown";
}

// 命令执行器类型：生产用 promisify(exec)，测试注入 mock 验证命令序列。
export type CommandRunner = (
  cmd: string
) => Promise<{ stdout: string; stderr: string }>;

// 跳转/打开指定 opencode session：已附着 tmux（pane 与 session 均已知）时
// switch-client + select-pane 直接跳转；未附着时以 title 前 15 字（清理特殊
// 字符）命名新建 tmux session（-c 落在 session 所属目录）并用 TUI 的
// `opencode --session` 打开，已存在同名 tmux session 时直接切换过去。
// 注意不能用 `opencode run --session`：run 是非交互命令，缺 message 参数会
// 直接报错退出，导致 tmux session 创建后立即死亡（真机验证过的坑）。
export async function focusSession(
  sessionId: string,
  title: string,
  tmuxPane: string | null | undefined,
  tmuxSession: string | null | undefined,
  directory?: string | null,
  runCommand: CommandRunner = (cmd) => execAsync(cmd),
  opts?: { autoSwitch?: boolean }
): Promise<void> {
  const autoSwitch = opts?.autoSwitch !== false;
  try {
    if (tmuxPane && tmuxSession && autoSwitch) {
      // 目标可能是别的 tmux session（switch-client）、同 session 的别的 window
      //（select-window，pane id 可直接作 window 目标）或同 window 的别的 pane
      //（select-pane）。tmuxSession 形如 "$29"，必须单引号包裹，否则 $29 会被
      // sh 当位置参数展开成 "9" 导致 can't find session。
      await runCommand(`tmux switch-client -t '${tmuxSession}'`);
      await runCommand(`tmux select-window -t '${tmuxPane}'`);
      await runCommand(`tmux select-pane -t '${tmuxPane}'`);
      return;
    }
    if (tmuxPane && tmuxSession && !autoSwitch) {
      // 点击源不在 tmux 内：跳转类命令（switch/select）都需要客户端上下文，
      // 执行只会报 "no current client"，直接不做。
      return;
    }

    const name = makeTmuxSessionName(title, sessionId);
    const cwdArgs = directory ? `-c "${directory}" ` : "";
    try {
      await runCommand(`tmux has-session -t '${name}'`);
    } catch (e) {
      await runCommand(
        `tmux new-session -d -s '${name}' ${cwdArgs}"opencode --session ${sessionId}"`
      );
    }
    if (autoSwitch) {
      await runCommand(`tmux switch-client -t '${name}'`);
    }
  } catch (err: any) {
    log(`focusSession error for ${sessionId}: ${err?.message || String(err)}`);
    throw err;
  }
}

// 纯函数：切换指定 project 的折叠状态，便于单元测试。
export function toggleCollapse(
  state: Record<string, boolean>,
  projectId: string,
): Record<string, boolean> {
  const next = { ...state };
  if (next[projectId]) {
    delete next[projectId];
  } else {
    next[projectId] = true;
  }
  return next;
}

// 纯函数：判断鼠标按下事件是否应当触发折叠切换（仅左键）。
export function shouldHandleMouseDown(button: number): boolean {
  return button === 0;
}

// 纯函数：构造 project 标题文本，包含下属 session 数量。
export function formatProjectTitle(icon: string, label: string, count: number): string {
  return `${icon} 📁${label} (${count})`;
}

// 纯函数：处理鼠标按下事件，左键时阻止冒泡并触发切换回调。
export function handleMouseDown(
  e: { button: number; stopPropagation(): void },
  onToggle: () => void,
): void {
  if (shouldHandleMouseDown(e.button)) {
    e.stopPropagation();
    onToggle();
  }
}

// 纯函数：构造 session 标题文本，包含直接 subsession 数量。
export function formatSessionTitle(icon: string, title: string, count: number): string {
  return `${icon} ${title} (${count})`;
}

// global project 的哨兵值。daemon/manage 侧均使用精确小写 "global"；
// 本函数作为防御性规范化比较，防止空格/大小写等异常值绕过全局 project 判断。
export const GLOBAL_PROJECT_ID = "global";

// 纯函数：判断给定 projectId 是否为 global 哨兵 project。
// 移除空白、零宽空格、BOM 等不可见字符后小写规范化再比较，避免尾随空格、
// 零宽空格或大小写造成绕过。
export function isGlobalProject(projectId?: string): boolean {
  if (!projectId) return false;
  const normalized = projectId
    .replace(/[\s\u200B-\u200D\uFEFF\u00A0]/g, "")
    .toLowerCase();
  return normalized === GLOBAL_PROJECT_ID;
}

// 纯函数：构造发送给 daemon 的 action payload，支持可选 projectId。
export function buildActionPayload(
  actionType: string,
  sessionIDs: string[],
  projectId?: string,
): { type: string; action: string; sessionIds: string[]; projectId?: string } {
  const payload: any = { type: "action", action: actionType, sessionIds: sessionIDs };
  if (projectId) payload.projectId = projectId;
  return payload;
}

// 纯函数：迭代收集 node 自身及全部后代 sessionId，首位为 node 自身。
// 使用显式栈避免深层链递归栈溢出；使用 visited Set 防御循环引用 / 自引用，并兼容 children 缺失的情况。
// DFS 先序：父节点先于子节点输出，子节点按 children 数组原顺序输出。
export function collectDescendantIDs<
  T extends { sessionId: string; children?: T[] },
>(node: T): string[] {
  const result: string[] = [];
  const visited = new Set<string>();
  const stack: T[] = [node];
  while (stack.length > 0) {
    const n = stack.pop()!;
    if (!n || !n.sessionId || visited.has(n.sessionId)) continue;
    visited.add(n.sessionId);
    result.push(n.sessionId);
    if (Array.isArray(n.children)) {
      // 反向入栈，保证出栈时按原数组顺序处理子节点。
      for (let i = n.children.length - 1; i >= 0; i--) {
        stack.push(n.children[i]);
      }
    }
  }
  return result;
}

// 纯函数：迭代收集 project 下全部 session ID（含后代），供 project 删除使用。
// 1. 先按 sessionId 去重（保留最后一个实例），避免 buildSessionTree 中重复 sessionId 的多个 root 导致 visited 误跳过子节点。
// 2. 重建 session 树后用显式栈 DFS 先序收集，避免深层链递归栈溢出；使用共享 visited 防止跨树重复。
export function collectProjectSessionIDs(p: { sessions?: SidebarSession[] }): string[] {
  const ids: string[] = [];
  if (!Array.isArray(p?.sessions)) return ids;

  // 按 sessionId 去重，保留数组中最后出现的实例，同时维持剩余 session 的相对顺序。
  const uniqueSessions: SidebarSession[] = [];
  const seenIds = new Set<string>();
  for (let i = p.sessions.length - 1; i >= 0; i--) {
    const s = p.sessions[i];
    if (!seenIds.has(s.sessionId)) {
      seenIds.add(s.sessionId);
      uniqueSessions.unshift(s);
    }
  }

  const roots = buildSessionTree(uniqueSessions);
  const visited = new Set<string>();
  const stack: SessionTreeNode[] = [];
  // 反向入栈，保证 root 按原顺序处理。
  for (let i = roots.length - 1; i >= 0; i--) {
    stack.push(roots[i]);
  }

  while (stack.length > 0) {
    const n = stack.pop()!;
    if (!n || !n.sessionId || visited.has(n.sessionId)) continue;
    visited.add(n.sessionId);
    ids.push(n.sessionId);
    if (Array.isArray(n.children)) {
      for (let i = n.children.length - 1; i >= 0; i--) {
        stack.push(n.children[i]);
      }
    }
  }
  return ids;
}

// 类型守卫：判断节点是否为 session 节点（自有 sessionId 属性）。
function isSessionNode(node: SessionTreeNode | SidebarProject): node is SessionTreeNode {
  return Object.prototype.hasOwnProperty.call(node, "sessionId");
}

// 纯函数：根据删除目标节点计算 delete action 参数；global project 返回 null 表示不可删除。
export function confirmDeleteAction(
  node: SessionTreeNode | SidebarProject,
): { ids: string[]; projectId?: string } | null {
  if (!node) {
    // 防御性默认：对 null/undefined 输入不可删除，避免运行时崩溃。
    return null;
  }
  if (isSessionNode(node)) {
    return { ids: collectDescendantIDs(node) };
  }
  if (isGlobalProject(node.projectId)) {
    return null;
  }
  return { ids: collectProjectSessionIDs(node), projectId: node.projectId };
}

// 纯函数：从 favorites map 中批量移除指定 ids，不可变地返回新对象。
export function removeIdsFromMap(
  ids: string[],
  map: Record<string, boolean>,
): Record<string, boolean> {
  const next = { ...map };
  for (const id of ids) {
    delete next[id];
  }
  return next;
}

// 纯函数：把扁平 sessions 按 parentId 重建为多级树。
export function buildSessionTree(flatSessions: SidebarSession[]): SessionTreeNode[] {
  const map = new Map<string, SessionTreeNode>();
  const roots: SessionTreeNode[] = [];
  for (const s of flatSessions) {
    const node: SessionTreeNode = { ...s, children: [] };
    map.set(node.sessionId, node);
    const parent = node.parentId ? map.get(node.parentId) : undefined;
    if (parent && parent.sessionId !== node.sessionId && node.depth > 0) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
}

// 状态过滤 chip 定义：过滤栏展示顺序按状态优先级从高到低。标签用 3 字母
// 缩写（sidebar 宽约 33 列，单行动态渲染需要紧凑）；PERMISSION 显示为 ASK，
// 与 TUI 的 🟡 ASK 图标语义一致（权限确认与提问统一映射）。
export const STATUS_CHIPS: ReadonlyArray<{ key: string; label: string }> = [
  { key: "ERROR", label: "ERR" },
  { key: "PERMISSION", label: "ASK" },
  { key: "RETRY", label: "RTY" },
  { key: "BUSY", label: "BSY" },
  { key: "IDLE", label: "IDL" },
  { key: "UNKNOWN", label: "UNK" },
  { key: "ARCHIVED", label: "ARC" },
];

// 纯函数：按计数筛选需要渲染的 chip（只保留 count > 0 的状态，保持定义顺序）。
// 零计数状态过滤结果必为空、无点击价值，隐藏后单行即可容纳；全零返回空数组
// （无任何 session 时过滤栏整体不渲染）。
export function visibleChips(
  counts: Record<string, number>,
): ReadonlyArray<{ key: string; label: string }> {
  return STATUS_CHIPS.filter((chip) => (counts?.[chip.key] ?? 0) > 0);
}

// 纯函数：判断状态过滤是否有实际生效的勾选（存在至少一个真值 key）。
// toggleStatusFilter 只产生真值 key（取消即删除）；防御性把 {X:false} 之类
// 脏数据视为未激活，与 sessionMatchesFilter 的判定保持一致。
export function filterActive(
  filter: Record<string, boolean> | undefined | null,
): boolean {
  if (!filter) return false;
  return Object.keys(filter).some((k) => filter[k]);
}

// 纯函数：判断 session 是否命中状态过滤。
// 无生效勾选时恒 true（不过滤）；有勾选时要求自身 status 命中。
export function sessionMatchesFilter(
  s: { status: string } | undefined | null,
  filter: Record<string, boolean>,
): boolean {
  if (!filterActive(filter)) return true;
  return !!s && filter[s.status] === true;
}

// 纯函数：不可变切换状态过滤勾选（未勾选加入 / 已勾选删除）。
export function toggleStatusFilter(
  state: Record<string, boolean>,
  status: string,
): Record<string, boolean> {
  const next = { ...state };
  if (next[status]) {
    delete next[status];
  } else {
    next[status] = true;
  }
  return next;
}

// 纯函数：过滤扁平 sessions，保留「自身 status 命中」的节点及其全部祖先链
// （剪枝保形：匹配项的父链全部保留，层级/缩进/折叠状态不丢；其余剪掉）。
// 树结构由调用方交给 buildSessionTree 重建，本函数只做扁平集合运算：
// 1. 建 id→session 索引；2. 从每个命中节点沿 parentId 向上标记祖先
// （visited 防御 parent 环与自引用，悬空 parentId 安全停止）；
// 3. 按原数组顺序输出被标记的 session。
export function filterSessionsKeepingAncestors(
  flatSessions: SidebarSession[],
  filter: Record<string, boolean>,
): SidebarSession[] {
  if (!Array.isArray(flatSessions)) return [];
  if (!filterActive(filter)) return flatSessions.slice();
  const byId = new Map<string, SidebarSession>();
  for (const s of flatSessions) {
    if (s && s.sessionId) byId.set(s.sessionId, s);
  }
  const keep = new Set<string>();
  for (const s of flatSessions) {
    if (!s || !s.sessionId || !sessionMatchesFilter(s, filter)) continue;
    let cur: SidebarSession | undefined = s;
    const visited = new Set<string>();
    while (cur && cur.sessionId && !visited.has(cur.sessionId)) {
      visited.add(cur.sessionId);
      keep.add(cur.sessionId);
      cur = cur.parentId ? byId.get(cur.parentId) : undefined;
    }
  }
  return flatSessions.filter((s) => s && s.sessionId && keep.has(s.sessionId));
}

// 纯函数：判断收藏条目是否命中状态过滤——叶子看自身 status，父条目
// （hasChildren）额外看 rowStatus 聚合。与「全部」树视图的祖先保留语义对齐：
// 子代命中时父行保留（树上父行靠祖先链规则，扁平收藏列表没有链，用聚合态
// 近似），保证两个 tab 过滤结果观感一致。
export function favoriteMatchesFilter(
  fav: { status: string; rowStatus: string; hasChildren: boolean } | undefined | null,
  filter: Record<string, boolean>,
): boolean {
  if (!filterActive(filter)) return true;
  if (!fav) return false;
  if (filter[fav.status] === true) return true;
  return fav.hasChildren === true && filter[fav.rowStatus] === true;
}

// 纯函数：统计全部 project 中各自身 status 的 session 数（chip 徽章计数，
// 不看 rowStatus 聚合）。只统计 STATUS_CHIPS 覆盖的已知状态，未知状态忽略；
// 每个已知状态恒有 key（缺省 0），chip 渲染无需判空。
export function countStatuses(
  projects: Array<{ sessions?: SidebarSession[] }> | undefined | null,
): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const chip of STATUS_CHIPS) counts[chip.key] = 0;
  if (!Array.isArray(projects)) return counts;
  for (const p of projects) {
    if (!p || !Array.isArray(p.sessions)) continue;
    for (const s of p.sessions) {
      if (!s || !s.status) continue;
      if (Object.prototype.hasOwnProperty.call(counts, s.status)) {
        counts[s.status]++;
      }
    }
  }
  return counts;
}

// 纯函数：把 daemon ViewMsg 中的原始 project 对象规范化为 SidebarProject，
// 同时对其 sessions 逐项透传 parentId / hasChildren / depth / isFavorite，
// 以及 tmux 跳转所需的 pid / tmuxPane / tmuxSession 附着信息。
export function normalizeProject(p: any): SidebarProject {
  return {
    projectId: p.projectId || "",
    name: p.name || "",
    worktree: p.worktree || "",
    timeUpdated: p.timeUpdated || 0,
    rowStatus: p.rowStatus || "UNKNOWN",
    sessions: Array.isArray(p.sessions)
      ? p.sessions.map((s: any) => ({
          sessionId: s.sessionId || "",
          title: s.title || "",
          directory: s.directory || "",
          timeUpdated: s.timeUpdated || 0,
          status: s.status || "UNKNOWN",
          rowStatus: s.rowStatus || "UNKNOWN",
          parentId: s.parentId || "",
          hasChildren: !!s.hasChildren,
          depth: s.depth || 0,
          isFavorite: s.isFavorite === true,
          pid: typeof s.pid === "number" ? s.pid : 0,
          tmuxPane: s.tmuxPane || null,
          tmuxSession: s.tmuxSession || null,
        }))
      : [],
  };
}

// 纯函数：判断 session 节点是否拥有可折叠子节点（hasChildren 且 children 非空）。
export function sessionHasChildren(
  node: { hasChildren: boolean; children?: unknown[] },
): boolean {
  return node.hasChildren && Array.isArray(node.children) && node.children.length > 0;
}

// 纯函数：根据 session 的行状态返回对应颜色。
// 有子节点时优先使用 rowStatus（聚合状态），否则使用自身 status。
export function sessionRowColor(
  node: { hasChildren: boolean; rowStatus: string; status: string },
): string {
  if (node.hasChildren) {
    return Object.prototype.hasOwnProperty.call(statusColors, node.rowStatus)
      ? statusColors[node.rowStatus]
      : "#565f89";
  }
  return Object.prototype.hasOwnProperty.call(statusColors, node.status)
    ? statusColors[node.status]
    : "#565f89";
}

// 纯函数：判断指定 session 在 expandedMap 中是否为展开状态。
export function isSessionExpanded(
  expandedMap: Record<string, boolean>,
  sessionId: string,
): boolean {
  return !!expandedMap[sessionId];
}

function log(msg: string) {
  const line = `[${new Date().toISOString()}] ${msg}\n`;
  try {
    appendFileSync(LOG_FILE, line);
  } catch {}
}

interface SidebarSession {
  sessionId: string;
  title: string;
  directory: string;
  timeUpdated: number;
  status: string;
  rowStatus: string;
  parentId: string;
  hasChildren: boolean;
  depth: number;
  isFavorite: boolean;
  pid: number;
  tmuxPane: string | null;
  tmuxSession: string | null;
}

interface SessionTreeNode extends SidebarSession {
  children: SessionTreeNode[];
}

interface SidebarProject {
  projectId: string;
  name: string;
  worktree: string;
  timeUpdated: number;
  rowStatus: string;
  sessions: SidebarSession[];
}

const statusColors: Record<string, string> = {
  ERROR: "#f7768e",
  PERMISSION: "#e0af68",
  RETRY: "#ff9e64",
  BUSY: "#7aa2f7",
  IDLE: "#9ece6a",
  UNKNOWN: "#565f89",
  ARCHIVED: "default",
};

function SessionRow(props: {
  node: SessionTreeNode;
  expandedMap: () => Record<string, boolean>;
  toggleSession: (sessionId: string) => void;
  favorites: () => Record<string, boolean>;
  toggleFavorite: (sessionId: string) => void;
  onRequestDelete: (node: SessionTreeNode) => void;
  onFocusSession: (node: SessionTreeNode) => void;
}) {
  const s = props.node;
  const hasChildren = sessionHasChildren(s);
  const expanded = () => isSessionExpanded(props.expandedMap(), s.sessionId);
  const color = () => sessionRowColor({ hasChildren, rowStatus: s.rowStatus, status: s.status });
  const titleBase = truncate(s.title || s.sessionId?.slice(0, 8) || "?", 28);
  const age = formatRelativeTime(s.timeUpdated || 0);
  // 本 session 是否已收藏（daemon IsFavorite 为主，本地信号作乐观更新兜底）。
  const isFavorite = () => s.isFavorite || !!props.favorites()[s.sessionId];
  // 标题颜色优先级：收藏标记 > 原色。
  const titleFg = () => {
    if (isFavorite()) return "#e0af68";
    return hasChildren ? color() : "#a9b1d6";
  };

  // 行尾 ★ 收藏按钮：独立 text 元素承载点击。@opentui 的 span 不是布局节点、
  // onMouseDown 只对 text/box 生效（span 挂事件无效），故 ★ 拆为兄弟 text；
  // 点击 ★ 只切换收藏不触发展开（主 text 与 ★ 为兄弟节点，handleMouseDown 的
  // stopPropagation 再阻断向上冒泡）。内容含前后空格（" ★ "）撑大点击命中区、
  // 保证 ★ 视觉完整不半星，同时替代 marginLeft 提供行间间距。
  const favoriteButton = (
    <text
      fg={isFavorite() ? "#e0af68" : "#565f89"}
      onMouseDown={(e) => handleMouseDown(e, () => props.toggleFavorite(s.sessionId))}
    >
      {isFavorite() ? ' ★ ' : ' ☆ '}
    </text>
  );

  // 行尾 X 删除按钮：独立 text 元素，左键点击触发删除确认；global project 下的
  // session 也显示 X，session 删除不限制 project。
  const deleteButton = (
    <text
      fg="#f7768e"
      onMouseDown={(e) => handleMouseDown(e, () => props.onRequestDelete(s))}
    >
      {' X '}
    </text>
  );

  // 行尾 ↗ tmux 跳转按钮：位于 ★ 与 X 之间。点击后已附着 tmux 的 session 直接
  // switch-client + select-pane 跳转，未附着的新建 tmux session 并用
  // opencode run --session 打开。stopPropagation 防止冒泡触发行展开。
  const jumpButton = (
    <text
      fg="#7aa2f7"
      onMouseDown={(e) => handleMouseDown(e, () => props.onFocusSession(s))}
    >
      {' ↗ '}
    </text>
  );

  return (
    <box flexDirection="column" paddingLeft={s.depth * 2}>
      <box flexDirection="row">
        {hasChildren ? (
          <>
            {/* 方案 A + span 分段着色：箭头、状态点与标题合并为单个 text，空格写在
                字符串（span）内部撑开间距，规避多 text 元素边界/宽度计算对 CJK 标题
                前空格被吞的问题。箭头/状态点 span 用 style={{fg}} 保留状态色（@opentui
                的 span 仅 style 属性生效，直接 fg= 是 no-op），标题 span 用 titleFg()；
                整行左键点击展开/折叠。 */}
            <text
              onMouseDown={(e) => handleMouseDown(e, () => props.toggleSession(s.sessionId))}
            >
              <span style={{ fg: color(), bold: true }}>{expanded() ? '▼' : '▶'}</span>
              <span style={{ fg: color() }}> ● </span>
              <span style={{ fg: titleFg() }}>
                {expanded()
                  ? `${titleBase} · ${age}`
                  : `${truncate(titleBase, 28)} (${s.children.length}) · ${age}`}
              </span>
            </text>
            {favoriteButton}
            {jumpButton}
            {deleteButton}
          </>
        ) : (
          <>
            {/* 方案 A 叶子行 + span 分段着色：状态点 span 用 color() 恢复状态色
                （修复 subsession 变灰），标题 span 用 titleFg()。叶子无子节点，
                主 text 不响应展开；收藏、跳转与删除均走行尾按钮。 */}
            <text>
              <span style={{ fg: color() }}>● </span>
              <span style={{ fg: titleFg() }}>{titleBase} · {age}</span>
            </text>
            {favoriteButton}
            {jumpButton}
            {deleteButton}
          </>
        )}
      </box>
      {hasChildren && expanded() && (
        <box flexDirection="column">
          {s.children.map((child) => (
            <SessionRow
              node={child}
              expandedMap={props.expandedMap}
              toggleSession={props.toggleSession}
              favorites={props.favorites}
              toggleFavorite={props.toggleFavorite}
              onRequestDelete={props.onRequestDelete}
              onFocusSession={props.onFocusSession}
            />
          ))}
        </box>
      )}
    </box>
  );
}

export function ProjectGroup(props: {
  project: SidebarProject;
  isCollapsed: boolean;
  onToggle: () => void;
  expandedMap: () => Record<string, boolean>;
  toggleSession: (sessionId: string) => void;
  favorites: () => Record<string, boolean>;
  toggleFavorite: (sessionId: string) => void;
  onRequestDelete: (node: SidebarProject) => void;
  onFocusSession: (node: SessionTreeNode) => void;
}) {
  const p = props.project;
  const sessions = Array.isArray(p.sessions) ? p.sessions : [];
  const sessionTree = createMemo(() => buildSessionTree(sessions));
  const worktree = formatWorktree(p.worktree);
  const label = worktree || p.projectId;
  const icon = createMemo(() => (props.isCollapsed ? "▶" : "▼"));
  const onTitleMouseDown = (e: { button: number; stopPropagation(): void }) => {
    handleMouseDown(e, props.onToggle);
  };
  return (
    <box flexDirection="column" paddingY={0.5}>
      <box
        flexDirection="row"
        onMouseDown={onTitleMouseDown}
      >
        <text bold fg="#c0caf5" marginRight={1} onMouseDown={onTitleMouseDown}>{icon()}</text>
        <text bold fg={Object.prototype.hasOwnProperty.call(statusColors, p.rowStatus) ? statusColors[p.rowStatus] : "#565f89"} marginRight={1}>●</text>
        <text bold fg="#c0caf5" onMouseDown={onTitleMouseDown}>📁{label} ({sessions.length})</text>
        {!isGlobalProject(p.projectId) && (
          <text
            fg="#f7768e"
            onMouseDown={(e) => handleMouseDown(e, () => props.onRequestDelete(p))}
          >
            {' X '}
          </text>
        )}
      </box>
      {!props.isCollapsed && (
        <box flexDirection="column">
          {sessions.length === 0 ? (
            <text fg="#a9b1d6">  (no sessions)</text>
          ) : (
            sessionTree().map((node) => (
              <SessionRow
                node={node}
                expandedMap={props.expandedMap}
                toggleSession={props.toggleSession}
                favorites={props.favorites}
                toggleFavorite={props.toggleFavorite}
                onRequestDelete={props.onRequestDelete}
                onFocusSession={props.onFocusSession}
              />
            ))
          )}
        </box>
      )}
    </box>
  );
}

// 单个状态过滤 chip：状态色圆点 + 缩写标签 + 实时计数，左键点击切换勾选。
// 激活时整 chip 以状态色加粗高亮，未激活弱化灰；间距写在字符串内（沿用既有
// 惯例，规避多 text 边界对空格的处理差异）。
function StatusFilterChip(props: {
  chipKey: string;
  label: string;
  filter: () => Record<string, boolean>;
  counts: () => Record<string, number>;
  onToggle: (status: string) => void;
}) {
  const active = () => !!props.filter()[props.chipKey];
  const color = Object.prototype.hasOwnProperty.call(statusColors, props.chipKey)
    ? statusColors[props.chipKey]
    : "#565f89";
  return (
    <text
      bold={active()}
      fg={active() ? color : "#565f89"}
      onMouseDown={(e) => handleMouseDown(e, () => props.onToggle(props.chipKey))}
    >
      {`● ${props.label} ${props.counts()[props.chipKey] ?? 0}  `}
    </text>
  );
}

// 状态过滤栏：单行、只渲染非零状态的 chip（visibleChips），点击切换勾选
// （可多选组合），作用于「全部」tab 的 project/session 树（剪枝保形，折叠
// 状态不丢）。默认全不勾 = 不过滤；有勾选时行首出现 ✕ 一键清除——前置而非
// 行尾，保证极端多 chip 撑满宽度时重置按钮仍可见可达。
function StatusFilterBar(props: {
  filter: () => Record<string, boolean>;
  counts: () => Record<string, number>;
  onToggle: (status: string) => void;
  onReset: () => void;
}) {
  const chips = () => visibleChips(props.counts());
  if (chips().length === 0) return null;
  return (
    <box flexDirection="row" paddingY={0.5}>
      {filterActive(props.filter()) && (
        <text
          fg="#f7768e"
          onMouseDown={(e) => handleMouseDown(e, props.onReset)}
        >
          {'✕ '}
        </text>
      )}
      {chips().map((chip) => (
        <StatusFilterChip
          chipKey={chip.key}
          label={chip.label}
          filter={props.filter}
          counts={props.counts}
          onToggle={props.onToggle}
        />
      ))}
    </box>
  );
}

function OctlSidebar(props: {
  projects: SidebarProject[];
  connected: boolean;
  error: string;
  favorites: () => Record<string, boolean>;
  toggleFavorite: (sessionId: string) => void;
  onConfirmDelete: (node: SessionTreeNode | SidebarProject) => void;
  onFocusSession: (node: Pick<SessionTreeNode, "sessionId" | "title" | "directory" | "pid" | "tmuxPane" | "tmuxSession">) => void;
}) {
  const [collapsed, setCollapsed] = createSignal<Record<string, boolean>>({});
  const [expandedSessions, setExpandedSessions] = createSignal<Record<string, boolean>>({});
  const [activeTab, setActiveTab] = createSignal<"all" | "favorites">("all");
  // 状态过滤：勾选集合（真值 key），作用于「全部」tab 的树；默认空 = 不过滤。
  const [statusFilter, setStatusFilter] = createSignal<Record<string, boolean>>({});
  // chip 徽章计数与过滤后的可见 project 列表。纯客户端计算：daemon 推送的
  // 完整 ViewMsg 已含全部 session 与状态，过滤只是渲染层筛选。
  const statusCounts = createMemo(() => countStatuses(props.projects));
  const visibleProjects = createMemo(() => {
    if (!filterActive(statusFilter())) return props.projects;
    return props.projects
      .map((p) => ({
        ...p,
        sessions: filterSessionsKeepingAncestors(p.sessions, statusFilter()),
      }))
      .filter((p) => p.sessions.length > 0);
  });
  const [confirmState, setConfirmState] = createSignal<{ node: SessionTreeNode | SidebarProject } | null>(null);

  const toggleProject = (projectId: string) => {
    setCollapsed((prev) => toggleCollapse(prev, projectId));
  };

  const toggleSession = (sessionId: string) => {
    setExpandedSessions((prev) => {
      const next = { ...prev };
      if (next[sessionId]) {
        delete next[sessionId];
      } else {
        next[sessionId] = true;
      }
      return next;
    });
  };

  // 状态 chip 勾选切换与一键清除（不可变更新，交给 memo 重算可见列表）。
  const toggleStatus = (status: string) => {
    setStatusFilter((prev) => toggleStatusFilter(prev, status));
  };

  const resetStatusFilter = () => {
    setStatusFilter({});
  };

  // 行尾 X 按钮触发的删除请求：直接打开确认条。
  const handleRequestDelete = (node: SessionTreeNode | SidebarProject) => {
    log(
      "handleRequestDelete: " +
        ("sessionId" in node ? node.sessionId : node.projectId),
    );
    setConfirmState({ node });
  };

  // 根据删除目标生成确认浮层提示文案。
  const confirmMessage = (node: SessionTreeNode | SidebarProject): string => {
    if ("sessionId" in node) {
      return `删除该 session 及其全部子 session？（共 ${collectDescendantIDs(node).length} 个）`;
    }
    if (isGlobalProject(node.projectId)) {
      return "global project 不可删除。";
    }
    return `删除该 project 下全部 session 及 project 记录？（共 ${collectProjectSessionIDs(node).length} 个 session）`;
  };

  return (
    <box paddingY={1} paddingX={1} flexDirection="column">
      <text bold fg="#c0caf5">Session Status</text>
      {/* tab 切换栏：手写 text + onMouseDown（tab_select 组件无鼠标交互）。
          选中 tab 高亮，未选中弱化。左键点击切换。 */}
      <box flexDirection="row" paddingY={1}>
        <text
          bold={activeTab() === "all"}
          fg={tabTitleFg(activeTab(), "all")}
          onMouseDown={(e) => handleMouseDown(e, () => setActiveTab((prev) => switchTab(prev, "all")))}
        >
          全部
        </text>
        <text fg="#565f89">  |  </text>
        <text
          bold={activeTab() === "favorites"}
          fg={tabTitleFg(activeTab(), "favorites")}
          onMouseDown={(e) => handleMouseDown(e, () => setActiveTab((prev) => switchTab(prev, "favorites")))}
        >
          收藏
        </text>
      </box>
      {/* 状态 chip 过滤栏：全局作用于两个 tab——「全部」树剪枝保形、「收藏」
          列表按同样语义筛选（favoriteMatchesFilter），勾选状态跨 tab 保持。 */}
      {props.connected && (
        <StatusFilterBar
          filter={statusFilter}
          counts={statusCounts}
          onToggle={toggleStatus}
          onReset={resetStatusFilter}
        />
      )}
      {props.error && <text fg="#a9b1d6">{friendlyError(props.error)}</text>}
      {!props.connected && !props.error && (
        <text fg="#a9b1d6">offline</text>
      )}
      {props.connected && props.projects.length === 0 && activeTab() === "all" && (
        <text fg="#a9b1d6">(no active sessions)</text>
      )}
      {props.connected && activeTab() === "favorites" && (
        <box flexDirection="column" paddingY={1}>
          <text bold fg="#e0af68">★ Favorites</text>
          {(() => {
            const favs = props.favorites();
            const keys = Object.keys(favs);
            if (keys.length === 0) {
              return <text fg="#a9b1d6">(no favorites)</text>;
            }

            // 从所有 project 的 sessions 中反查收藏 session 的标题和状态，
            // 已删除的 session（在 projects 中找不到）跳过不显示。
            // 父 session 复用聚合后的 rowStatus，与「全部」树视图保持一致；
            // 同时带上 directory/pid/tmux 附着信息供 ↗ 跳转。
            const sessionMap = new Map<string, { title: string; directory: string; status: string; rowStatus: string; hasChildren: boolean; pid: number; tmuxPane: string | null; tmuxSession: string | null }>();
            for (const p of props.projects) {
              if (!Array.isArray(p.sessions)) continue;
              for (const s of p.sessions) {
                if (favs[s.sessionId]) {
                  sessionMap.set(s.sessionId, { title: s.title, directory: s.directory, status: s.status, rowStatus: s.rowStatus, hasChildren: s.hasChildren, pid: s.pid, tmuxPane: s.tmuxPane, tmuxSession: s.tmuxSession });
                }
              }
            }
            const entries = keys
              .filter((k) => sessionMap.has(k))
              .map((k) => ({
                sessionId: k,
                title: sessionMap.get(k)!.title,
                directory: sessionMap.get(k)!.directory,
                status: sessionMap.get(k)!.status,
                rowStatus: sessionMap.get(k)!.rowStatus,
                hasChildren: sessionMap.get(k)!.hasChildren,
                pid: sessionMap.get(k)!.pid,
                tmuxPane: sessionMap.get(k)!.tmuxPane,
                tmuxSession: sessionMap.get(k)!.tmuxSession,
              }));
            if (entries.length === 0) {
              return <text fg="#a9b1d6">(no favorites)</text>;
            }

            // 状态过滤同样作用于收藏列表：叶子看自身 status，父条目额外看
            // rowStatus 聚合（favoriteMatchesFilter），与「全部」树的祖先保留
            // 语义对齐，两个 tab 过滤观感一致。全被滤掉时给出差异化空态提示。
            const filtered = entries.filter((fav) =>
              favoriteMatchesFilter(fav, statusFilter()),
            );
            if (filtered.length === 0) {
              return <text fg="#a9b1d6">(no matching favorites)</text>;
            }

            return filtered.map((fav) => (
              <box flexDirection="row">
                {/* 方案 A + span 分段着色：状态点与标题合并为单个 text，间距写在
                    span 字符串内（"● " 尾随空格），规避汉字标题前空格被吞的问题；
                    整条目左键点击切换收藏，行尾 ↗ 跳转到该 session 的 tmux pane。 */}
                <text
                  onMouseDown={(e) => handleMouseDown(e, () => props.toggleFavorite(fav.sessionId))}
                >
                  <span style={{ fg: sessionRowColor({ hasChildren: fav.hasChildren, rowStatus: fav.rowStatus, status: fav.status }) }}>● </span>
                  <span style={{ fg: "#e0af68" }}>
                    {truncate(fav.title || fav.sessionId.slice(0, 8), 40)}
                  </span>
                </text>
                <text
                  fg="#7aa2f7"
                  onMouseDown={(e) => handleMouseDown(e, () => props.onFocusSession(fav))}
                >
                  {' ↗ '}
                </text>
              </box>
            ));
          })()}
        </box>
      )}
      {props.connected && activeTab() === "all" && (
        <box flexDirection="column">
          {/* 有数据但全被过滤掉时给出明确提示（区别于「无任何 session」）。 */}
          {visibleProjects().length === 0 && props.projects.length > 0 && (
            <text fg="#a9b1d6">(no matching sessions)</text>
          )}
          {visibleProjects().map((p) => {
            const pid = p.projectId;
            return (
              <ProjectGroup
                project={p}
                isCollapsed={!!collapsed()[pid]}
                onToggle={() => toggleProject(pid)}
                expandedMap={expandedSessions}
                toggleSession={toggleSession}
                favorites={props.favorites}
                toggleFavorite={props.toggleFavorite}
                onRequestDelete={handleRequestDelete}
                onFocusSession={props.onFocusSession}
              />
            );
          })}
        </box>
      )}
      {/* 蒙版确认弹窗：在 sidebar 根 box 内用 absolute 拉伸覆盖整个面板（不用 Portal，
          因为 Portal 挂载到 renderer.root 会把蒙版定位到整个 TUI 的错误位置）。 */}
      {(() => {
        const state = confirmState();
        if (!state) return null;
        const node = state.node;
        return (
          <box
            position="absolute"
            top={0}
            left={0}
            right={0}
            bottom={0}
            zIndex={1000}
            backgroundColor="#1a1b2680"
            alignItems="center"
            justifyContent="center"
            onMouseDown={(e) => {
              e.stopPropagation();
              setConfirmState(null);
            }}
          >
            <box
              flexDirection="column"
              border={true}
              borderColor="#f7768e"
              backgroundColor="#1a1b26"
              paddingX={2}
              paddingY={1}
              onMouseDown={(e) => e.stopPropagation()}
            >
              <text fg="#c0caf5">{confirmMessage(node)}</text>
              <box flexDirection="row" paddingY={1}>
                <text
                  fg="#f7768e"
                  bold
                  onMouseDown={(e) =>
                    handleMouseDown(e, () => {
                      setConfirmState(null);
                      props.onConfirmDelete(node);
                    })
                  }
                >
                  [确认删除]
                </text>
                <text fg="#565f89">  </text>
                <text
                  fg="#565f89"
                  onMouseDown={(e) => handleMouseDown(e, () => setConfirmState(null))}
                >
                  [取消]
                </text>
              </box>
            </box>
          </box>
        );
      })()}
    </box>
  );
}

export default {
  id: "octl-sidebar",
  async tui(api: any) {
    log("tui() called");

    const [projects, setProjects] = createSignal<SidebarProject[]>([]);
    const [connected, setConnected] = createSignal(false);
    const [error, setError] = createSignal("");
    // 收藏状态：daemon ViewMsg.Favorites 为本端数据源，乐观更新后由下一次 ViewMsg 确认。
    const [favorites, setFavorites] = createSignal<Record<string, boolean>>({});
    // 协议版本不一致标志：为 true 时停止重连（重连也只会再次被 daemon 拒绝）。
    const [versionMismatch, setVersionMismatch] = createSignal(false);

    let socket: Socket | null = null;
    let disposed = false;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let readBuf = "";

    function scheduleRetry() {
      if (disposed || retryTimer) return;
      retryTimer = setTimeout(() => {
        retryTimer = null;
        connect();
      }, 2000);
    }

    function clearRetry() {
      if (retryTimer) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
    }

    function onSocketClose() {
      setConnected(false);
      socket = null;
      // 版本不一致时停止盲重连（重连只会再次被 daemon 拒绝，刷屏 12 万条日志）。
      if (!disposed && !versionMismatch()) scheduleRetry();
    }

    // 向 daemon 发送 action（favorite / unfavorite / delete 等），支持可选 projectId。
    function sendAction(actionType: string, sessionIDs: string[], projectId?: string) {
      if (!socket || socket.destroyed) {
        log("sendAction: socket not connected, action=" + actionType);
        setError(actionType + " 未执行：daemon 离线");
        return;
      }
      try {
        socket.write(JSON.stringify(buildActionPayload(actionType, sessionIDs, projectId)) + "\n");
        log("sendAction: " + actionType + " " + JSON.stringify(sessionIDs));
      } catch (e: any) {
        log("sendAction error: " + (e.message || String(e)));
        setError("发送失败: " + (e.message || String(e)));
      }
    }

    // 乐观移除本地 favorites 中已删除的 session（参考 toggleFavoriteAction 模式，不等 daemon push）。
    function removeLocalFavorites(ids: string[]) {
      setFavorites((prev) => removeIdsFromMap(ids, prev));
    }

    // 执行删除：session 删除收集自身+全部后代；project 删除收集全部 session + projectId。
    function executeDelete(node: SessionTreeNode | SidebarProject) {
      const action = confirmDeleteAction(node);
      if (!action) {
        log("executeDelete: confirmDeleteAction returned null, abort");
        return;
      }
      log("executeDelete: ids=" + JSON.stringify(action.ids) + " projectId=" + (action.projectId || ""));
      sendAction("delete", action.ids, action.projectId);
      removeLocalFavorites(action.ids);
    }

    // 切换收藏：发 daemon action + 乐观更新本地信号。
    function toggleFavoriteAction(sessionId: string) {
      const isFav = !!favorites()[sessionId];
      if (isFav) {
        sendAction("unfavorite", [sessionId]);
        setFavorites((prev) => {
          const next = { ...prev };
          delete next[sessionId];
          return next;
        });
      } else {
        sendAction("favorite", [sessionId]);
        setFavorites((prev) => ({ ...prev, [sessionId]: true }));
      }
    }

    function handleLine(line: string) {
      if (!line.trim()) return;
      try {
        const msg = JSON.parse(line);
        // 订阅确认：收到 subscribed 才算真正在线（socket connect 只是 TCP 建连）。
        if (msg.type === "subscribed") {
          setConnected(true);
          setVersionMismatch(false);
          setError("");
          log("subscribed: " + JSON.stringify(msg.channels || []));
          return;
        }
        // daemon 对 subscribe/request 的错误响应（含版本不一致）。
        if (msg.type === "response" && msg.ok === false) {
          const err = String(msg.error || "unknown error");
          if (isVersionMismatchError(err)) {
            // 版本不一致：显示明确错误并停止重连，避免 2 秒盲重连刷屏。
            setVersionMismatch(true);
            setConnected(false);
            setError("插件与 daemon 版本不一致，请运行 octl plugins 重新生成并重启 opencode");
            log("version mismatch from daemon: " + err);
            if (socket) {
              try {
                socket.end();
              } catch {}
            }
            return;
          }
          setError(err);
          return;
        }
        if (msg.type === "view" && Array.isArray(msg.projects)) {
          setProjects(msg.projects.map(normalizeProject));
          // 同步 daemon 收藏数据到本地 favorites 信号（daemon 为权威数据源）。
          if (Array.isArray(msg.favorites)) {
            const favMap: Record<string, boolean> = {};
            for (const f of msg.favorites) {
              if (f.sessionId) favMap[f.sessionId] = true;
            }
            setFavorites(favMap);
          } else {
            // 旧 daemon 不含 favorites 字段时优雅降级为空收藏。
            setFavorites({});
          }
          // 注意：这里不清 error。view 推送高频到达（每个事件都广播），若在此
          // 清除，跳转提示等操作反馈会被下一秒的推送冲掉；错误的清除交给
          // connect 成功与 result 成功两个明确的恢复点。
          return;
        }
        if (msg.type === "result") {
          // daemon action 执行结果：失败时显示可见错误，成功时清除错误。
          if (msg.error) {
            setError("操作失败: " + String(msg.error));
            log("result error for " + msg.action + ": " + msg.error);
          } else {
            setError("");
            log("result success: " + msg.action + " " + JSON.stringify(msg.summary || {}));
          }
          return;
        }
        if (msg.type === "progress") {
          // 加载态：当前仅记录日志，后续如需 UI 加载提示可在此扩展 busy 信号。
          log("progress: " + msg.action + " done=" + msg.done);
          return;
        }
      } catch (e: any) {
        // parse errors are logged only when debugging
      }
    }

    function onSocketData(data: Buffer) {
      readBuf += data.toString("utf8");
      let idx: number;
      while ((idx = readBuf.indexOf("\n")) >= 0) {
        const line = readBuf.slice(0, idx);
        readBuf = readBuf.slice(idx + 1);
        handleLine(line);
      }
    }

    function connect() {
      if (disposed || socket) return;
      clearRetry();
      readBuf = "";

      try {
        const sock = createConnection({ path: SOCKET_PATH }, () => {
          log("socket connected");
          setError("");

          // Register as subscriber to receive daemon-pushed ViewMsg updates.
          // 真正的在线状态在收到 "subscribed" 后由 handleLine 置 true（避免被拒仍显示在线）。
          sock.write(JSON.stringify({ type: "subscribe", channels: ["view"], version: OCTL_PROTOCOL_VERSION }) + "\n");
        });

        sock.on("data", onSocketData);
        sock.on("close", () => {
          onSocketClose();
        });
        sock.on("error", (err: Error) => {
          setError(err.message);
        });

        socket = sock;
      } catch (e: any) {
        setError(e.message);
        onSocketClose();
      }
    }

    connect();

    onCleanup(() => {
      log("tui cleanup");
      disposed = true;
      clearRetry();
      if (socket) {
        try {
          socket.end();
        } catch {}
        socket = null;
      }
    });

    // 跳转/打开指定 session：已附着 tmux 直接跳转 pane，未附着新建 tmux
    // session 并以 TUI 模式（opencode --session）打开。结果与失败都显示在
    // 顶部状态行（与 error 同一条通道、同一位置），不再静默吞掉。
    function focusSessionAction(node: {
      sessionId: string;
      title: string;
      directory?: string;
      pid: number;
      tmuxPane: string | null;
      tmuxSession: string | null;
    }) {
      // switch-client / select-pane 都要求当前进程是 tmux 客户端；sidebar 所在
      // 的 opencode 不在 tmux 内时（无 TMUX 环境变量）无法自动切换，只能创建
      // /复用目标 tmux session 并提示用户手动切换。
      const inTmux = !!process.env.TMUX;
      if (!inTmux && node.tmuxPane && node.tmuxSession) {
        setError(
          `目标正在 tmux ${node.tmuxSession} 的 pane ${node.tmuxPane} 运行，当前不在 tmux 内，无法自动跳转`
        );
        return;
      }
      const name = makeTmuxSessionName(node.title, node.sessionId);
      focusSession(
        node.sessionId,
        node.title,
        inTmux ? node.tmuxPane : null,
        inTmux ? node.tmuxSession : null,
        node.directory,
        undefined,
        { autoSwitch: inTmux }
      )
        .then(() => {
          if (!inTmux) {
            setError(
              `已创建/复用 tmux session「${name}」，当前不在 tmux 内，请手动切换`
            );
          }
        })
        .catch((err: any) => {
          setError("跳转失败: " + (err?.message || String(err)));
          log("onFocusSession error: " + (err?.message || String(err)));
        });
    }

    api.slots.register({
      slots: {
        sidebar_content: () => (
          <OctlSidebar
            projects={projects()}
            connected={connected()}
            error={error()}
            favorites={favorites}
            toggleFavorite={toggleFavoriteAction}
            onConfirmDelete={executeDelete}
            onFocusSession={focusSessionAction}
          />
        ),
      },
    });

    log("sidebar_content slot registered");
  },
};
