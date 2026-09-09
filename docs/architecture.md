# 架构设计与模块接口

## 模块依赖图

```
main.go
  │
  ├── internal/tui
  │    ├── app.go          主应用：视图切换、布局编排、消息路由、daemon 连接状态、favoritesMap 共享收藏集合
  │    ├── nav.go          导航栏：渲染 3 个视图入口 + 高亮当前选中
  │    ├── statusbar.go    状态栏：显示 session 总数/活跃数/总费用
  │    ├── keys.go         快捷键定义：Tab/Ctrl+x/1-3/Esc
  │    └── views/
  │         ├── dashboard  项目管理 + 会话树（含实时状态列）、管理操作请求、f 键收藏
  │         ├── favorites  收藏 session 列表（f 取消收藏 / d 删除）
  │         └── stats      用量统计
  │
  ├── internal/daemon      实时状态守护进程：Unix socket listener、event 处理、DB 同步、ViewMsg 构建/广播、action 执行
  │    └── client.go       SocketClient：TUI/sidebar 作为 Unix socket 客户端连接 daemon
  ├── internal/db          数据库：只读 SQLite 查询层 + Project 查询
  ├── internal/manage      管理操作：删除/导出/创建/fork/发送/project删除
  └── internal/types       纯数据结构（Project, Session, MessagePart 等）

internal/plugins/
  ├── gen.go                      go:embed + MD5 + Generate：生成含协议版本的插件文件
  ├── gen_test.go                 生成逻辑测试
  └── templates/
        ├── octl-hook.js          事件转发插件模板
        └── octl-sidebar.tsx      sidebar 插件模板

plugin/                          插件测试（Bun）
  ├── octl-hook/
  │     └── octl-hook.test.js     Server 插件测试
  └── octl-sidebar/
        └── octl-sidebar.test.ts  sidebar 测试
```

**依赖规则**：`main → tui → views → daemon → {db, manage, types}` + `main --daemon → daemon → {db, manage, types}`

TUI 不再直接依赖 `internal/db` 或 `internal/manage`。

## TUI 视图

当前有 3 个视图，通过数字键 1-3 切换：

| 键 | 视图 | 文件 |
|----|------|------|
| 1 | Manage — 项目+会话树（展开/收起/创建/删除/导出/fork/发送/收藏） | `views/dashboard.go` |
| 2 | Favorites — 收藏 session 列表 | `views/favorites.go` |
| 3 | Stats — 用量统计 | `views/stats.go` |

## 树形结构

```
📁 global (project)
  ├── ses_xxx... (root session)
  │   └── ses_yyy... (subagent session)
  └── ses_zzz... (root session)
📁 <git-hash> (project, git repo)
  └── ses_aaa... (root session)
```

Project 节点和 Session 节点支持不同的按键操作。

## daemon-driven 数据流

```
        opencode 实例
              │
              │ 11 类目标事件
              ▼
       internal/plugins/templates/octl-hook.js
       （运行时由 `octl plugins --output=<dir>` 生成到目标目录）
              │
              │ 裸事件 JSON
              ▼
       ┌──────────────────────────────────────────────────────┐
       │              octl --daemon                            │
       │  ┌───────────────────────────────────────────────┐   │
       │  │              StateManager                      │   │
       │  │  processEvent → stateMap                       │   │
       │  │  syncFromDB (30s)                              │   │
       │  │  buildView() → ViewMsg                         │   │
       │  │  handle action / request messages              │   │
       │  └───────────────────────────────────────────────┘   │
       │                          │                            │
       │          ┌───────────────┴───────────────┐            │
       │          ▼                               ▼            │
       │   event source                      subscriber        │
       │   (plugin)                          (TUI / sidebar)   │
       └───────────────────────────────────────────────────────┘
              │                                    │
              │ ViewMsg / progress / result        │ action / request
              ▼                                    ▼
       ┌──────────────────┐              ┌──────────────────┐
       │ TUI (app.go)     │              │ sidebar (tsx)    │
       │ 🟢 ONLINE / 🔴 OFFLINE          │                  │
       │ dashboard / favorites / stats   │                  │
       └──────────────────┘              └──────────────────┘
```

所有业务逻辑集中在 daemon：

- 读取 SQLite 数据库
- 维护实时状态 stateMap
- 构建 project/session 树并计算 `rowStatus`
- 计算全局统计 `ViewStats`
- 推送 `ViewMsg` 给所有 `view` 频道订阅者
- 接收并执行管理操作 action（含 `favorite` / `unfavorite` 收藏操作）
- 维护收藏集合（内存态）并注入 `ViewMsg.Favorites` / `isFavorite`
- 响应 `messages` 请求

TUI 和 sidebar 只做渲染和 UI 状态维护。

## 收藏数据流（daemon 权威驱动，Phase 2 完成）

收藏集合由 **daemon 统一维护**（内存态），权威数据源是 daemon 推送的 `ViewMsg.Favorites` 与每个 session 的 `isFavorite` 字段。前端（TUI / sidebar）只做渲染缓存 + 乐观更新，增删收藏一律通过 `favorite` / `unfavorite` action 发送到 daemon：

- **daemon 侧**：`StateManager` 在内存中维护有序收藏集合（`favorites []string` 保持插入顺序 + `favoritesSet map[string]bool`）。`handleActionMsg` 新增 `favorite` / `unfavorite` 两个 case（幂等批量处理：已收藏的 session 执行 favorite 为 no-op 仍返回成功，未收藏的 unfavorite 同理）。`buildView` 逐 session 注入 `IsFavorite`，并按 `favorites` 插入顺序组装 `Favorites` 列表（孤儿收藏——session 已不存在于当前视图——跳过）。
- **删除联动剪枝（三路径）**：
  1. `handleDeleteAction`：删除成功的 session 同步 `removeFavorite`；
  2. `session.deleted` 事件：hook 活跃时被删除的 session 实时移出收藏；
  3. `syncFromDB` tombstone 扫描：外部删除（如 `opencode` CLI）的 session 在 30s DB 同步周期内被检测到，对应收藏 ID 同步移除。
- **TUI 侧**：app 层收到 `daemonViewMsg` 后以 `ViewMsg.Favorites` 重建共享收藏集合 `favoritesMap`（`map[string]bool`，陈旧本地项被丢弃，旧 daemon 无该字段时优雅降级为空）。dashboard 按 `f` 发 `FavoritesToggleRequestMsg`（toggle 语义：已收藏→`unfavorite`、未收藏→`favorite`）、favorites 视图按 `f` 发 `FavoritesRemoveRequestMsg`（`unfavorite`），app 层乐观更新集合、广播 `FavoritesChangedMsg` 给两个子视图，同时向 daemon 发送对应 action；下一次 `ViewMsg` 到达后以 daemon 数据调和。收藏跨视图、跨 `ViewMsg` 刷新保持。
- **sidebar 侧**：每次收到 `view` 消息时将 `ViewMsg.Favorites` 同步到本地 `favorites` signal（daemon 为权威数据源，旧 daemon 无该字段时优雅降级为空）。点击 session 标题触发 `toggleFavoriteAction`：经 `sendAction` 发送 `favorite`/`unfavorite` action，并乐观更新本地 signal，由下一次 `ViewMsg` 确认调和。收藏列表在「收藏」tab 内显示（`★ Favorites`，空收藏时显示 `(no favorites)` 提示），条目从所有 project 的 sessions 反查标题/状态（状态点复用 `rowStatus` 聚合渲染父项、自身 `status` 渲染叶子，与「全部」树视图一致）。
- **两端经 daemon 保持一致**：TUI 与 sidebar 的收藏状态最终都由 daemon 的 `buildView` 输出统一，不再各自独立维护。收藏仍为**内存态**（daemon 重启即丢失，磁盘持久化留待后续）。

## 视图数据：ViewMsg

daemon 推送的 `ViewMsg` 是 TUI 和 sidebar 的唯一数据源：

```
{
  "type": "view",
  "projects": [
    {
      "projectId": "...",
      "name": "...",
      "worktree": "...",
      "timeUpdated": 1234567890,
      "rowStatus": "BUSY",
      "sessions": [
        {
          "sessionId": "...",
          "title": "...",
          "directory": "...",
          "agent": "...",
          "cost": 0.05,
          "timeUpdated": 1234567890,
          "timeArchived": 0,
          "status": "BUSY",
          "rowStatus": "BUSY",
          "parentId": "",
          "hasChildren": false,
          "depth": 1,
          "isFavorite": true
        }
      ]
    }
  ],
  "stats": {
    "totalSessions": 10,
    "activeSessions": 8,
    "totalCost": 1.23,
    "totalTokens": 45678
  },
  "favorites": [
    {
      "sessionId": "...",
      "title": "...",
      "status": "BUSY",
      "rowStatus": "BUSY",
      "isFavorite": true
    }
  ]
}
```

- `status`：单个 session 的实时状态
- `rowStatus`：root session 及其所有 subsession 聚合后的优先级最高状态
- `parentId`：父 session ID，空字符串表示 root session
- `hasChildren`：该 session 是否拥有 subsession
- `depth`：session 在树中的层级（root=1，逐层 +1），用于渲染缩进
- `isFavorite`：该 session 是否在 daemon 收藏集合中（每个 session 行都带）
- `favorites`：按 daemon 插入顺序预组装的收藏 session 列表（完整 `ViewSession`，含 `isFavorite`；孤儿收藏被跳过，daemon 重启前为内存态）
- `stats`：全局聚合，直接渲染 Stats 视图

## 管理操作协议

TUI 不再直接调用 `manage.Manager`，而是通过 socket 向 daemon 发送 action 请求：

| action | 含义 | 关键字段 |
|--------|------|----------|
| `delete` | 批量删除 session | `sessionIds`, `projectId` |
| `export` | 批量导出 JSON | `sessionIds` |
| `create` | 新建 session | `directory`, `message` |
| `fork` | fork 子 session | `sessionId`, `directory`, `message` |
| `send` | 向已有 session 发送消息 | `sessionId`, `directory`, `message` |
| `favorite` | 批量收藏 session（幂等：已收藏为 no-op 仍成功） | `sessionIds` |
| `unfavorite` | 批量取消收藏 session（幂等：未收藏为 no-op 仍成功） | `sessionIds` |

daemon 执行流程：

1. 返回 `progress` 消息
2. 调用 `internal/manage` 执行具体操作
3. 返回 `result` 消息（含 `summary`）
4. 重新 `buildView()` 并 `pushView()`，所有订阅者自动刷新

## 消息查看协议

在 session 节点按 `m` 查看对话历史时，TUI 向 daemon 发送 `request/messages`：

```json
{"type":"request","method":"messages","id":"...","sessionId":"..."}
```

daemon 返回 `response` 消息，包含该 session 的所有 `MessagePart`（role + text + timeCreated）。TUI 渲染对话查看器。

## 实时状态守护进程

### StateManager

位置：`internal/daemon/daemon.go`

daemon 是独立进程（`octl --daemon`），核心组件通过 `select` 串行化事件处理：

| 组件 | 说明 |
|------|------|
| **socket listener** | 接受 Unix socket 连接，根据第一行 JSON 区分为 event source 或 subscriber |
| **eventCh (buf=100)** | 缓冲来自 plugin 的原始 JSON 事件 |
| **clients[]** | subscriber 连接注册表，按 `channels` 字段广播 |
| **processEvent** | 解析 11 类事件并更新 stateMap |
| **syncFromDB (30s)** | 定时全量同步 DB，处理事件超时（60s）后回退到 DB 状态、tombstone 清理 |
| **buildView** | 读取 DB，结合 stateMap 构建 ViewMsg（注入 `isFavorite` 并组装 `Favorites`） |
| **pushView** | 广播 ViewMsg 给所有订阅了 `view` 频道的客户端 |
| **favorites 收藏存储** | `favorites []string`（插入序）+ `favoritesSet map[string]bool`（O(1) 成员检查），内存态；`favorite`/`unfavorite` action 幂等写、删除三路径自动剪枝 |
| **handleActionMsg** | 执行 TUI 发来的管理操作（delete/export/create/fork/send/favorite/unfavorite） |
| **handleRequestMsg** | 处理 snapshot / listSessions / messages / daily 请求（未知 method 返回错误 response 而非静默） |

**并发模型**：Event channel 和 clients 注册表提供 goroutine 隔离。stateMap 使用 `sync.RWMutex` 保护（stateManager goroutine 独占写）。

### SocketClient

位置：`internal/daemon/client.go`

TUI 和 sidebar 通过 `SocketClient` 以 Unix socket 连接 daemon：

1. `Connect()` 连接 `~/.local/share/octl/octl.sock`
2. `SubscribeView()` 发送 `{"type":"subscribe","channels":["view"]}`
3. 后台 goroutine 读取 JSON Lines，解析为 `ViewMsg` / `ProgressMsg` / `ResultMsg` / `ResponseMsg` / `SubscribedMsg` / `PongMsg`
4. 连接断开时 `Msgs()` channel 关闭，TUI 进入 `🔴 OFFLINE` 状态并定时重连

### Session 状态类型

```
     CREATED (event) → UNKNOWN
              │
              │ session.status: idle/busy/permission/retry/error
              ▼
     IDLE / BUSY / PERMISSION / RETRY / ERROR
              │
              │ session.idle
              ▼
            IDLE
              │
              │ session.status: busy
              ▼
            BUSY ── session.status: retry ──→ RETRY ──→ BUSY
              │
              │ session.compacted
              │   (GC compaction 后重置)
              ▼
            BUSY
              │
              │ permission.updated / permission.asked / question.asked
              ▼
         PERMISSION
              │
              │ permission.replied (allow) → BUSY
              │ permission.replied (deny)  → IDLE
              │ question.replied / question.rejected → BUSY
              │
              ▼
        BUSY / IDLE
              │
              │ session.error
              ▼
            ERROR
```

| 状态常量 | Glyph() 输出 | lipgloss 颜色 | 含义 |
|----------|-------------|---------------|------|
| ERROR | 🔴 | 196 (red) | 错误状态 |
| PERMISSION | 🟡 | 220 (yellow) | 等待处理：工具权限确认或 agent 提问（Icon 标签 🟡 ASK） |
| RETRY | 🟠 | 208 (orange) | 错误重试中 |
| BUSY | 🔵 | 45 (cyan) | AI 处理中 |
| IDLE | 🟢 | 120 (green) | 等待用户输入 |
| UNKNOWN | ❔ | 240 (gray) | 状态不确定 |
| ARCHIVED | 📦 | 243 (dark gray) | 已归档 |

TUI 第一列展示 `Glyph()` 纯图标（无文字标签）；已收藏的 session 在图标后追加 ` ⭐`（状态图标与 ⭐ 之间一个空格，如 `🟢 ⭐`）。所有 glyph 均为 Emoji_Presentation 彩色 emoji，终端渲染宽度固定 2 列，与 go-runewidth 计数一致，保证列对齐。

### Plugin: octl-hook.js

模板位置：`internal/plugins/templates/octl-hook.js`，运行时通过 `octl plugins --output=<dir>` 生成。

Bun 插件，每个 opencode 实例启动时加载。负责过滤 11 类目标事件并通过 Unix socket 推送给 octl daemon。

| 特性 | 说明 |
|------|------|
| 连接方式 | `Bun.connect({ unix: socketPath })` 直接连接 daemon |
| 目标事件 | session.status/idle/created/deleted/error, permission.asked/replied, question.asked/replied/rejected, session.compacted |
| 缓冲区 | 500 条 FIFO，daemon 离线时缓存，连接后重放 |
| 重连 | 连接断开后每 2s 自动重试 |
| 生命周期 | `dispose` 时关闭连接、清除定时器 |

### Plugin: octl-sidebar.tsx

模板位置：`internal/plugins/templates/octl-sidebar.tsx`，运行时通过 `octl plugins --output=<dir>` 生成。

SolidJS/TSX 插件，在 OpenCode TUI 中注册 `sidebar_content` slot。它：

1. 通过 Unix socket 连接到 daemon
2. 发送 `{"type":"subscribe","channels":["view"]}`
3. 解析 daemon 推送的 `ViewMsg`，`normalizeProject` 对每个 session 透传 `parentId` / `hasChildren` / `depth` / `isFavorite`
4. 用 `buildSessionTree` 将扁平 sessions 按 `parentId` 重建为多级树（悬空 parentId / 自引用 / depth≤0 按 root 处理）
5. 直接渲染 `Session Status` 面板，支持 project 级与 session 级两级折叠（▶/▼ 图标，左键点击行首图标切换，非左键不响应）：
   - project 标题行折叠/展开其 session 列表，标题显示 session 总数 (N)，项目名前用 `statusColors` 渲染 `rowStatus` 聚合状态点
   - session 行拆分为独立 text 子元素：行首展开图标 text（▶/▼，仅绑展开折叠）+ 标题 text（点击切换收藏），固定 1 空格 gap
   - 有子节点的 session 默认折叠，仅显示 root 行，折叠标题显示直接 subsession 计数（"标题 (N)"）；展开后 subsession 按 depth 缩进渲染并显示自身状态
   - session 折叠状态以 sessionId 为 key 用 `createSignal` 保持，`ViewMsg` 刷新后不丢失
   - 父节点状态标记用 daemon 推送的 `rowStatus` 渲染（含全部后代最高优先级状态），折叠与展开均可见
   - 标题 hover 高亮（`hoveredId` signal，变色 `#c0caf5`）
   - 顶部为「全部」/「收藏」tab 切换栏：`activeTab` signal（默认 `"all"`），手写 text + `onMouseDown` 实现（不用 @opentui 的 `tab_select` 组件——其无鼠标交互）；选中 tab 用 `tabTitleFg` 高亮 `#c0caf5`、未选中 `#565f89`，左键点击经 `switchTab` 纯函数切换（无效 tab 保持原值）
6. **收藏（daemon 权威驱动）**：收藏的权威数据源是 daemon 推送的 `ViewMsg.Favorites` 与每个 session 的 `isFavorite`——每次收到 `view` 消息时同步到本地 `favorites` signal（旧 daemon 无该字段时优雅降级为空收藏）。点击 session 标题触发 `toggleFavoriteAction`（左键 + stopPropagation）：经 `sendAction` 向 daemon 发送 `favorite`/`unfavorite` action，并乐观更新本地 signal，由下一次 `ViewMsg` 确认调和。已收藏标题带 `★` 前缀并以 `#e0af68` 高亮；`★ Favorites` 收藏列表在「收藏」tab 内显示（空收藏显示 `(no favorites)` 提示），条目从所有 project 的 sessions 反查标题/状态（状态点复用 `rowStatus` 聚合渲染父项、自身 `status` 渲染叶子，与「全部」树视图一致），已删除 session 自动跳过，点击条目可取消收藏
7. **删除（X 按钮）**：session 行尾 `★` 之后为 X 删除按钮（`#f7768e`，左键触发），project 标题行尾同样有 X。点击 X 打开 Portal 确认浮层（全屏遮罩阻止事件穿透，点击遮罩或 `[取消]` 关闭，`[确认删除]` 左键执行，提示文案含目标计数）；确认后经 `sendAction` 发送 `delete` action——session 删除收集自身+全部后代 sessionId，project 删除收集其全部 session 并附带 `projectId`；global project（哨兵值 `"global"`）不显示 X、不可删除。删除后乐观清理本地 favorites（不等 daemon push），daemon 的 `result` 消息携带执行结果（失败显示可见错误、成功清除错误），`progress` 消息仅记录日志

sidebar 不再发送 `listSessions` 请求，也不再维护 `pendingRequests` 映射。

### DB 同步规则 (30s)

| 场景 | 规则 |
|------|------|
| DB 有，内存无 | 新建 entry，status=deriveFromDB()，source=DB |
| 事件态 < 60s | 信任事件态，仅更新 title/projectID |
| 事件态 >= 60s | DB 接管：status=deriveFromDB()，source=DB；PERMISSION / ERROR 豁免（二者均为 opencode 内存态，不落库，保持到解除事件——`syncFromDB` 与显示路径 `statusForSession` 一致豁免） |
| DB 态 | 刷新 status=deriveFromDB() |
| DB 无，内存有 | 标记 tombstone；下一周期仍无则删除 |

`deriveFromDB` 逻辑：
1. `TimeArchived > 0` → ARCHIVED
2. 最后一条消息 role 为 "assistant"：`time.completed` 非零 → IDLE（AI 刚回复完）；缺失/为零 → BUSY（消息仍在生成，如长 shell 命令执行中）
3. 否则 → UNKNOWN

## 视图切换

视图通过 Tab/Shift+Tab 或数字键 1-3 切换，切换时不销毁子 Model。

## 消息查看器

在 session 节点按 `m` 进入对话查看器：

| 按键 | 作用 |
|------|------|
| `j` / `↓` | 向下滚动 1 行 |
| `k` / `↑` | 向上滚动 1 行 |
| `h` / `←` | 上一条消息 |
| `l` / `→` | 下一条消息 |
| `Esc` / `Ctrl+x` / `m` | 关闭查看器 |

- assistant 消息渲染 Markdown（代码块高亮、列表等）
- 底部有滚动条显示当前浏览位置
- 到顶/到底分别显示 "At top" / "At bottom"

## 发送消息

在 session 节点按 `s` 进入发送界面，输入消息后按 Enter，TUI 将 `send` action 发送给 daemon，由 daemon 调用 `opencode run -s <id> <message>` 执行。
