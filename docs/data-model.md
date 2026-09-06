# 数据模型

## 实体关系

```
┌───────────┐     ┌───────────┐     ┌───────────┐
│  Session  │1──n│  Message  │1──n│   Part    │
│           │     │           │     │           │
│ 会话      │     │ 消息      │     │ 消息片段  │
└───────────┘     └───────────┘     └───────────┘
      │
      │  belongs to project (project_id FK)
      ▼
┌───────────┐
│  Project  │
│           │
│ 项目      │
└───────────┘
```

## Project（项目）

来源：`opencode.db` 的 `project` 表，只读访问（删除操作除外）。

| Go 字段 | DB 列 | 类型（Go） | Nullable | 说明 |
|---------|-------|-----------|----------|------|
| ID | id | string | No | PK，"global" 或 git root commit hash |
| Worktree | worktree | string | No | 工作区绝对路径 |
| Vcs | vcs | string | **Yes** | "git" 或 "" |
| Name | name | string | **Yes** | 项目名称 |
| TimeCreated | time_created | int64 | No | unix ms |
| TimeUpdated | time_updated | int64 | No | unix ms |

octl 将 project 作为 session 的上层组织维度：
- `project.id = "global"`：不在 git 仓库中的会话
- `project.id = <git root commit>`：对应一个 git 仓库
- 从 project 节点删除时，会删除该 project 的所有子 session + project 记录本身，**不删除原始 git 文件和项目文件**

## Session（会话）

来源：`opencode.db` 的 `session` 表，只读访问。

| Go 字段 | DB 列 | 类型（Go） | Nullable | 说明 |
|---------|-------|-----------|----------|------|
| ID | id | string | No | PK，格式 `ses_<hex>` |
| ProjectID | project_id | string | No | 所属项目 ID，"global" 或 git hash |
| ParentID | parent_id | string | **Yes** | fork 来源 session ID |
| Slug | slug | string | No | 人类可读名称，如 "curious-cactus" |
| Directory | directory | string | No | 工作目录绝对路径 |
| Title | title | string | No | session 标题 |
| Version | version | string | No | opencode 版本号 |
| Agent | agent | string | **Yes** | 使用的 agent 类型 |
| Model | model | string | **Yes** | JSON 字符串（见下） |
| Cost | cost | float64 | No | 总费用（USD），default 0 |
| TokensInput | tokens_input | int64 | No | 输入 token 数，default 0 |
| TokensOutput | tokens_output | int64 | No | 输出 token 数，default 0 |
| TokensReasoning | tokens_reasoning | int64 | No | 推理 token 数，default 0 |
| TokensCacheRead | tokens_cache_read | int64 | No | 缓存读取 token 数，default 0 |
| TokensCacheWrite | tokens_cache_write | int64 | No | 缓存写入 token 数，default 0 |
| TimeCreated | time_created | int64 | No | 创建时间，unix ms |
| TimeUpdated | time_updated | int64 | No | 最后更新时间，unix ms |
| TimeCompacting | time_compacting | int64 | **Yes** | 压缩时间，unix ms；COALESCE 为 0 |
| TimeArchived | time_archived | int64 | **Yes** | 归档时间，unix ms；COALESCE 为 0 |
| Path | path | string | **Yes** | 路径（URL 风格，"home/user/proj"） |
| WorkspaceID | workspace_id | string | **Yes** | 工作区 ID |
| SummaryAdditions | summary_additions | int | **Yes** | 文件变更行数 |
| SummaryDeletions | summary_deletions | int | **Yes** | 文件删除行数 |
| SummaryFiles | summary_files | int | **Yes** | 文件变更数 |
| SummaryDiffs | summary_diffs | string | **Yes** | diff 摘要（JSON 文本） |
| MessageCount | — | int | **计算字段** | 通过 `SELECT COUNT(*) FROM message` 聚合，非 DB 列 |

### Model JSON 字段格式

```json
// 标准格式
{"id":"deepseek-v4-flash","providerID":"deepseek"}
// 带 variant
{"id":"deepseek-v4-pro","providerID":"deepseek","variant":"max"}
// opencode 内置模型
{"id":"big-pickle","providerID":"opencode"}
```

### 时间字段说明

所有 `time_*` 列在 DB 中存储为 **unix 毫秒整数**（如 `1779292141042`）。
Go 中映射为 `int64`。0 值表示"从未发生"（never archived / never compacted）。

### Nullable 列处理策略

| 列 | Go 扫描方式 | Null 时取值 |
|----|-----------|------------|
| parent_id | COALESCE → "" | 空字符串 |
| agent | COALESCE → "" | 空字符串 |
| model | COALESCE → "" | 空字符串 |
| time_compacting | COALESCE → 0 | 0（1970-01-01） |
| time_archived | COALESCE → 0 | 0（1970-01-01） |
| path | COALESCE → "" | 空字符串 |
| workspace_id | COALESCE → "" | 空字符串 |
| summary_* | COALESCE → 0 | 0 |

## Message（消息）

| Go 字段 | DB 列 | 类型 | Nullable | 说明 |
|---------|-------|------|----------|------|
| ID | id | string | No | PK，格式 `msg_<hex>` |
| SessionID | session_id | string | No | FK → session.id |
| TimeCreated | time_created | int64 | No | unix ms |
| TimeUpdated | time_updated | int64 | No | unix ms |
| Data | data | string | No | JSON（见下） |

### Message.Data JSON 结构

```json
{
  "role": "user" | "assistant",
  "time": {
    "created": 1779244748381,
    "completed": 1779244812345
  },
  "agent": "chat" | "architect" | "coder" | ...,
  "model": {
    "providerID": "deepseek",
    "modelID": "deepseek-v4-pro"
  },
  "summary": {
    "diffs": []
  }
}
```

**`time.completed` 字段**（unix 毫秒）：

- 消息生成完毕后由 opencode 写入；进行中的消息（如 assistant 正在执行长
  shell 命令）只有 `created`，没有 `completed`。
- daemon 的 `deriveFromDB` 依赖该字段区分"assistant 消息已完成 → IDLE"与
  "assistant 消息生成中 → BUSY"，避免 30 秒 DB 同步周期把 BUSY 状态错误
  覆盖成 IDLE。查询时用 `COALESCE(json_extract(data, '$.time.completed'), 0)`。

## Part（消息片段）

| Go 字段 | DB 列 | 类型 | Nullable | 说明 |
|---------|-------|------|----------|------|
| ID | id | string | No | PK |
| MessageID | message_id | string | No | FK → message.id |
| SessionID | session_id | string | No | 反范式 FK（便于直接按 session 查询，避免多一层 JOIN） |
| TimeCreated | time_created | int64 | No | unix ms |
| TimeUpdated | time_updated | int64 | No | unix ms |
| Data | data | string | No | JSON |

### Part 表的作用

**Part 是 opencode 数据库设计中的关键表**。一条 message 可以包含多个 part，
每个 part 是消息的一个"片段"。这支持了消息的多模态结构：

```
一条 message（用户提问）
  ├── part type="text"      → "帮我写个程序"
  ├── part type="file"      → 上传的文件内容
  └── part type="step-start"→ 步骤开始标记

一条 message（AI 回复）
  ├── part type="text"      → "好的，我来实现"
  ├── part type="code"      → ```python ... ```
  └── part type="step-end"  → 步骤结束标记
```

**对本工具的影响**：
- **导出/归档**：导出 JSON 时需要 `part JOIN message` 获取完整消息内容；project 级的批量导出/归档会遍历该 project 下所有 session
- 所有 role/agent 信息在 message 表，所有文本内容在 part 表——查询时必须 JOIN 两个表

### Part.Data JSON 结构

```json
// 文本内容（消息查看器/导出时使用）
{"type": "text", "text": "对话的实际文本内容..."}

// 步骤开始
{"type": "step-start", "text": ""}
```

## 关键 SQL 查询及其设计思路

### 列表查询（含消息数）

**用途**：仪表盘视图加载所有 session 列表时使用。

```sql
SELECT s.*, (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id) as msg_count
FROM session s ORDER BY s.time_created DESC
```

**设计思路**：
- `MessageCount` 不是 session 表的列，而是聚合计算值
- 用子查询 (SELECT COUNT(*)...) 而不是在 Go 代码中循环统计，减少应用层开销
- 31MB 数据库 + 几十条 session，子查询性能足够（实测 < 5ms）

### 模型用量统计

**用途**：统计视图展示各模型的 token 和费用分布。

```sql
SELECT json_extract(model, '$.id') as model_id,
       json_extract(model, '$.providerID') as provider_id,
       SUM(tokens_input + tokens_output) as token_count,
       SUM(cost) as cost
FROM session WHERE model IS NOT NULL AND model != ''
GROUP BY model_id, provider_id
```

**设计思路**：
- `model` 列存的是 JSON 字符串 `{"id":"deepseek-v4-flash","providerID":"deepseek","variant":"default"}`
- modernc.org/sqlite 内置 SQLite JSON1 扩展，支持 `json_extract()` 函数，不需要外部依赖
- 过滤 `model IS NOT NULL AND model != ''` 排除没有模型信息的空 session
