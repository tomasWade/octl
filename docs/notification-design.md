# octl 通知功能 — 完整设计方案

> **Status**: Implemented.
> 2025-06 更新：daemon 已改为独立进程（`octl --daemon`），TUI 通过 Unix socket 长连接订阅 snapshot。

## 一、整体架构

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  opencode 实例 A                    opencode 实例 B                          │
│  ┌────────────────────┐            ┌────────────────────┐                    │
│  │ octl-hook.js       │            │ octl-hook.js       │                    │
│  │ 500 条 FIFO 缓冲    │            │ 500 条 FIFO 缓冲    │                    │
│  │ 断线后 2s 重连      │            │ 断线后 2s 重连      │                    │
│  └────────┬───────────┘            └────────┬───────────┘                    │
│           │                                 │                               │
│           │   Unix socket                                                   │
│           ▼  (~/.local/share/octl/octl.sock)                            │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    octl daemon (独立进程)                             │   │
│  │  ┌─────────────────────┐  ┌──────────────────────┐  ┌──────────────┐  │   │
│  │  │ socket listener     │  │ eventCh (buf=100)    │  │ clients[]    │  │   │
│  │  │ accept unix conn    │  │ plugin events        │  │ subscribers  │  │   │
│  │  └─────────┬───────────┘  └──────────┬───────────┘  └──────┬───────┘  │   │
│  │            │                         │                      │         │   │
│  │            └─────────────┬───────────┴──────────────────────┘         │   │
│  │                          ▼                                            │   │
│  │            ┌───────────────────────────────┐                         │   │
│  │            │ stateManager (单 goroutine)   │                         │   │
│  │            │ select { event / ticker }     │                         │   │
│  │            │ stateMap[sessionID] → snapshot│                         │   │
│  │            └───────────────┬───────────────┘                         │   │
│  │                            │ broadcastSnapshot                       │   │
│  │                            ▼                                         │   │
│  │            ┌───────────────────────────────┐                         │   │
│  │            │ JSON Lines → subscribers      │                         │   │
│  │            └───────────────────────────────┘                         │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                      │                                       │
│                                      │ Unix socket                           │
│                                      ▼                                       │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         octl TUI                                     │   │
│  │  SocketClient → Msgs() → Update() → statusMap → dashboard           │   │
│  │  header right: 🟢 ONLINE / 🔴 OFFLINE                                │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 并发模型

- **socket listener goroutine**: 接受 Unix socket 连接，每个连接由独立 goroutine 服务
- **stateManager goroutine**: 唯一写 stateMap 的 goroutine，`select` 串行化事件和 DB 同步
- **TUI goroutine**: 通过 `SocketClient` 读 socket，持有自己的状态拷贝
- **clients 注册表** 受 `clientsMu` 保护；TUI 永不直接读 stateMap

---

## 二、三个组件职责

### 1. 插件 `octl-hook/octl-hook.js`

- 位置：`~/.config/opencode/plugins/octl-hook.js`（源码位于 `plugin/octl-hook/octl-hook.js`）
- 每个 opencode 实例启动时自动加载
- `init`: 通过 `Bun.connect({ unix: socketPath })` 直接连接 daemon socket
- `event`: 过滤 8 个目标事件，直接 `socket.write(JSON + "\n")`
- `dispose`: 关闭连接、清除重连定时器
- 自带 500 条 FIFO 缓冲：daemon 离线时缓存事件，连接成功后按先入先出顺序重放
- 断线后每 2 秒自动重连
- **插件不 subscribe**，只作为事件源；本版本不改造

### 2. octl daemon

- 独立进程：`octl --daemon`，前台运行
- 监听单一 Unix socket：`~/.local/share/octl/octl.sock`
- 新连接第一行决定角色：
  - `{"type":"subscribe"}` → subscriber，加入 `clients[]`，接收 snapshot 广播
  - 其他 → event source，写入 `eventCh(buf=100)`
- 30 秒定时器 → `db.ListSessions()` → 全量同步
- 单 goroutine `select { case event: ... case <-ticker: ... }` 串行化，独占 stateMap

### 3. TUI

- Status 列显示状态图标 + 颜色
- 通过 `internal/daemon.SocketClient` 连接 daemon
- 连接成功后发送 `subscribe`，之后接收主动推送的 snapshot
- daemon 离线时 TUI 不退出：header 右上角显示 `🔴 OFFLINE`，每 5 秒重连

---

## 三、启动流程

```
终端 1: octl --daemon
  ├─ 1. db.ListSessions() → 全量 populate stateMap
  │      每个 session: status = deriveFromDB()  // IDLE / ARCHIVED / UNKNOWN
  │                    source = DB
  │
  ├─ 2. 启动 Unix socket listener
  └─ 3. 启动 30s 定时器

终端 2: octl
  ├─ 1. 连接 ~/.local/share/octl/octl.sock
  ├─ 2. 发送 subscribe
  ├─ 3. 接收初始 snapshot
  └─ 4. 启动 Bubble Tea UI
```

**不调 opencode server API**。丢失的 transient 状态靠后续实时事件 + DB 定时同步修正。

---

## 四、Wire Protocol

同一 Unix socket 上同时承载两类流量：

### 1. 插件 → daemon（事件源）

插件不 subscribe，直接发送裸事件：

```json
{"type":"session.status","properties":{"sessionID":"...","status":{"type":"busy"}}}
```

### 2. TUI → daemon（subscriber）

订阅：

```json
{"type":"subscribe","channels":["snapshot"]}
```

心跳：

```json
{"type":"ping"}
```

显式请求：

```json
{"type":"request","method":"snapshot","id":"req-1"}
```

### 3. daemon → TUI

订阅确认：

```json
{"type":"subscribed","channels":["snapshot"]}
```

状态快照：

```json
{"type":"snapshot","states":[{"sessionId":"...","status":"BUSY", ...}]}
```

心跳响应：

```json
{"type":"pong"}
```

请求响应：

```json
{"type":"response","id":"req-1","states":[...]}
```

请求 sidebar 数据：

```json
{"type":"request","method":"listSessions","id":"req-1"}
```

响应：

```json
{"type":"response","id":"req-1","ok":true,"projects":[{"projectId":"global","name":"global","worktree":"/home/user","timeUpdated":1779292141042,"sessions":[{"sessionId":"ses_xxx","title":"my-session","timeUpdated":1779292141042,"statuses":{"BUSY":2,"IDLE":1},"rowStatus":"BUSY"}]}]}
```

---

## 五、DB 定时同步规则 (30s)

```go
for each dbSession in db.ListSessions():
    mem, exists := stateMap[dbSession.ID]

    if !exists:
        // A: DB有, 内存无 → 新建
        stateMap[id] = {status: deriveFromDB(), source: DB}

    else if mem.source == EVENT:
        if time.Since(mem.LastEventAt) < 60s:
            // 事件仍新鲜 → 信任事件态, 仅更新 title/projectID
        else:
            // 事件超时(>60s) → 事件流断了, DB 接管
            mem.status = deriveFromDB()
            mem.source = DB

    else: // source == DB
        mem.status = deriveFromDB()

// DB 无, 内存有 → 标记 tombstone, 2 个同步周期后真删
for mem in stateMap where mem.id not in dbSessionIDs:
    if mem.tombstone:
        delete(mem)
    else:
        mem.tombstone = true
```

### deriveFromDB

```go
func deriveFromDB(s types.Session, db *DB) SessionStatus {
    if s.TimeArchived != 0 {
        return StatusArchived
    }
    lastRole := db.GetLastMessageRole(s.ID)
    if lastRole == "assistant" {
        return StatusIdle  // AI 说完等人
    }
    return StatusUnknown
}
```

---

## 六、事件处理 (8 个)

### 1. `session.status`
- 入参：`{ sessionID, status: { type: "idle"|"busy"|"retry" } }`
- 动作：写 `status = BUSY|IDLE|RETRY, source = EVENT, LastEventAt = now`

### 2. `session.idle`
- 入参：`{ sessionID }`
- 动作：写 `status = IDLE, source = EVENT, LastEventAt = now`

### 3. `session.created`
- 入参：`{ info: { id, projectID, directory, title, time } }`
- 动作：新建 entry: `status = IDLE, source = EVENT`

### 4. `session.deleted`
- 入参：`{ info: { id } }`
- 动作：**立即从 stateMap 删除**（不等 tombstone）

### 5. `session.error`
- 入参：`{ sessionID?, error?: { name, data: { message } } }`
- 动作：如果 sessionID 非空 → 写 `status = ERROR, ErrMsg = message`

### 6. `permission.updated`
- 入参：`{ id, type, sessionID, title }`
- 动作：写 `status = PERMISSION, PermType = type, PermTitle = title`

### 7. `permission.replied`
- 入参：`{ sessionID, permissionID, response }`
- 动作：清 `PermInfo, status → BUSY`

### 8. `question.asked`
- 入参：`{ id, sessionID, ... }`
- 动作：写 `status = PERMISSION`（与权限确认同义复用，UI 统一显示 🟡 ASK）
- 说明：opencode question 工具（agent 提问并阻塞等待回答），该等待态**不会**体现在 `session.status`（只有 idle/retry/busy），只能靠本事件感知

### 9. `question.replied` / `question.rejected`
- 入参：`{ sessionID, requestID }`
- 动作：`status → BUSY`（agent 继续生成；若转入空闲由后续 `session.idle` 修正）

### 10. `session.compacted` (可选)
- 入参：`{ sessionID }`
- 动作：如果 entry 存在 → mark `status = BUSY`

---

## 七、会话状态机

```
CREATED → IDLE
          │
          │ user sends message (session.status: busy)
          ▼
         BUSY ──→ RETRY (retry) → BUSY
          │
           │ permission.updated / question.asked
           ▼
      PERMISSION
           │
           │ permission.replied (allow) → BUSY
           │ permission.replied (deny)  → IDLE
           │ question.replied / question.rejected → BUSY
           ▼
      BUSY / IDLE
          │
          │ session.idle
          ▼
         IDLE
          │
          │ session.error
          ▼
         ERROR
```

**UI 状态优先级**：ERROR > PERMISSION > RETRY > BUSY > IDLE > UNKNOWN > ARCHIVED

---

## 八、数据类型

```go
type SessionStatus string
const (
    StatusIdle       SessionStatus = "IDLE"
    StatusBusy       SessionStatus = "BUSY"
    StatusPermission SessionStatus = "PERMISSION"
    StatusRetry      SessionStatus = "RETRY"
    StatusError      SessionStatus = "ERROR"
    StatusUnknown    SessionStatus = "UNKNOWN"
    StatusArchived   SessionStatus = "ARCHIVED"
)

type StateSource string
const (
    SourceEvent StateSource = "EVENT"
    SourceDB    StateSource = "DB"
)

type SessionState struct {
    SessionID    string
    Status       SessionStatus
    Source       StateSource
    LastEventAt  time.Time
    LastSyncAt   time.Time
    Title        string
    ProjectID    string
    ErrorMsg     string
    PermType     string    // "bash" | "file_write" | ...
    PermTitle    string    // "Run bash: npm test"
    Tombstone    bool
}
```

---

## 九、文件改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `plugin/octl-hook/octl-hook.js` | **不变** | 继续直接连接 daemon socket 发送事件 |
| `internal/daemon/daemon.go` | **修改** | socket listener + subscriber registry + broadcastSnapshot |
| `internal/daemon/types.go` | **修改** | 新增 wire message 类型 |
| `internal/daemon/client.go` | **新增** | TUI 侧 Unix socket 客户端 |
| `internal/tui/app.go` | **修改** | 使用 SocketClient；header 显示 online/offline；断线重连 |
| `main.go` | **修改** | `--daemon` / `--tui` / `--socket` 命令行分支 |
| `README.md` | **更新** | 新的启动方式说明 |
| `docs/architecture.md` | **更新** | 新的模块依赖与数据流图 |

---

## 十、边界场景

| 场景 | 处理 |
|---|---|
| opencode 先启动, octl 后启动 | DB 全量同步 + 后续事件补 transient 状态；插件在 daemon 离线期间缓冲事件 |
| octl daemon 重启 | TUI 自动重连并重新订阅，恢复 ONLINE |
| 插件连不上 octl daemon | 500 条 FIFO 缓冲，每 2s 重试；超出的旧事件被覆盖，靠 30s DB 同步修正 |
| 多个 opencode 实例 | 每实例各自加载插件，各自连接 daemon socket |
| opencode 实例退出 | 插件 dispose 关闭连接和定时器 |
| TUI 启动时 daemon 未运行 | TUI 正常启动，显示 `🔴 OFFLINE`，每 5s 重连 |
| daemon 运行中崩溃 | TUI 不退出，显示 `🔴 OFFLINE`，保留最后状态，重连后恢复 |
| DB 同步进行中收到事件 | 事件存在 eventCh 里，syncFromDB 返回后立即 drain |
