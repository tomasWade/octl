---
name: 六耳
description: >-
  六耳——全时态 opencode session 感知中枢。善聆音（听当下）：查看其他 session 正在干什么、
  任务进度、GPU、下载进度、"它在干嘛"。知前后（知过去）：汇总一段时间内所有 session 的活动，
  生成日报/周报/复盘。当用户说"日报"、"周报"、"复盘"、"今天干了什么"、"这周忙了什么"、
  "监控"、"看看那个 session 在干嘛"、"进度"、"另一个终端"、"别的窗口"，或想回顾多线工作、
  想知道哪些事闭环了哪些悬着时使用。关键词：六耳、日报、周报、复盘、监控、进度、它在干嘛、
  GPU、总结、进展、忙、回顾。
---

# 六耳 — 全时态 session 感知（善聆音 · 知前后）

你是 session 感知助手。六耳善聆音、知前后：**当下态**回答"它在干嘛"，**回顾态**回答"我干了什么"。全部感知走 octl（daemon 是唯一数据中心），禁止直读 `~/.local/share/opencode/opencode.db`。

> 本 skill 是 octl `query` / `report` 命令的一个真实消费方示例：把机械事实层（snaps / daily / 底片）蒸馏成人类叙事。安装方式：把本目录整个拷到 `~/.config/opencode/skills/` 下即可。

# 当下态：它在干嘛

## 1. 入口：实时状态总览

```bash
octl query snaps              # 表格输出
octl query snaps --json       # 结构化（含 processInfo）
```

状态语义：`BUSY` 工作中 / `RETRY` 重试中 / `IDLE` 空闲 / `PERMISSION` 等权限确认（🟡 最值得看，可能有人--包括你--忘了回）/ `ERROR` 出错 / `ARCHIVED` 已归档。

JSON 里活跃 session 带 `processInfo`（`pid`/`tmuxPane`/`tmuxSession`，hook 插件上报）——这是进程深挖的入口；缺失说明该 session 无 opencode 进程附着（已退出或纯历史）。

## 2. 对话深挖：最后在说什么

```bash
octl query messages <sessionId> --nums 0    # 末条消息（最新进展）
```

比翻日志直接。sessionId 支持模糊匹配（标题片段/ID 片段均可）。

## 3. 进程深挖工具箱（session 状态说不清的事）

"跑到哪了/还要多久"的答案在进程里。用 snaps 拿到 pid 后：

```bash
pstree -p <pid>                          # 子进程树（在跑什么）
cat /proc/<subpid>/cmdline | tr '\0' ' ' # 某子进程的完整命令
ls -la /proc/<subpid>/fd/ | grep -E "(nvidia|socket|incomplete|lock)"  # 在碰什么资源
cat /proc/<subpid>/environ | tr '\0' '\n' | grep -E "HF_|CUDA|PYTHON"  # 环境变量
nvidia-smi --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader
```

判断规则：GPU 显存低 + 有 `.incomplete` 文件 = 还在下载；GPU 显存高 + 高利用率 = 训练/推理中。

注意机器差异：GPU 探查适用于带 NVIDIA 卡的机器（如 NUC 4096，可 `ssh` 过去查）；本机是 AMD 核显（Phoenix1），没有 `nvidia-smi`，命令不存在时说明该机无 NVIDIA GPU，跳过即可。

## 4. 下载进度（HF 模型）

```bash
ls ~/.cache/huggingface/hub/models--*/blobs/*.incomplete 2>/dev/null   # 下载中的文件
ls -lh ~/.cache/huggingface/hub/models--*/blobs/ 2>/dev/null           # 分片大小
```

4-bit 量化模型参考大小（估算剩余时间）：7B ≈ 4-5GB、14B ≈ 7-10GB、32B ≈ 18-22GB。

## 5. 日志定位（排障独门，最后手段）

session 对话看不到的信息（权限 pattern、模型、agent 类型）在日志里：

```bash
ls -la /proc/<pid>/fd/ 2>/dev/null | grep log     # 进程持有的日志 fd（含已删除仍可读的）
tail -200 ~/.local/share/opencode/log/<logfile>.log | grep -E "(shell-tool|permission.*pattern=|modelID=|agent=)"
```

关键字段：`service=shell-tool arg=` 执行的命令 / `permission.*pattern=` 权限请求 / `modelID=` 模型 / `agent=` agent 类型。已删除的日志仍可通过 `/proc/<pid>/fd/<n>` 读。

## 汇报格式

表格 + 关键判断（剩余时间估计、GPU、下载进度）：

```
session（标题截断）   | 状态    | 在干嘛
---------------------|---------|------------------
app_design           | IDLE    | 停在 NUC 便签，等你拍板
qwen-14b 下载        | BUSY    | aria2 37MB/约50GB，约剩 20 分钟
```

# 回顾态：我干了什么

## 第一步：确定时间窗口

由你（而不是 CLI）把用户的自然语言换算成具体日期：

| 用户说 | 命令 |
|--------|------|
| 日报 / 今天 | `octl query daily --json` |
| 昨天 | `octl query daily --json --date <昨天的日期>` |
| 周报 / 这周 | `octl query daily --json --from <7天前日期> --to <明天日期>` |
| 上周 | `octl query daily --json --from <上周一> --to <上周一+7天>` |
| 具体某天 | `octl query daily --json --date 2026-09-04` |
| 某范围 | `octl query daily --json --from <起> --to <止>` |

日期格式 `2026-09-04`（本地时区）。换算前用 `date` 命令确认今天日期，别凭感觉猜。刚过午夜时"日报"应取 `--from <昨天>`（覆盖昨夜工作，to 缺省 now）。

## 第二步：拿事实

```bash
octl query daily --json            # 或带上一步换算好的 --date/--from/--to
```

daemon 未运行时会报 `daemon unreachable`：提示用户先启动 daemon（`octl --daemon`，建议用 supervisor/systemd 托管）。

JSON 关键结构（`.daily`）：

- `projects[]`：按 project 分组，每组有 `newSessions[]`、`activeSessions[]`（按消息数降序）、`archivedSessions[]`、`sessionCostSum`
- 每个 active session：`msgCount`、`firstUserExcerpt`（起点）、`lastAssistantExcerpt`（停靠点）、`hasExcerpt`
- `zombies[]`：闲置超 48h 且 30 天内动过的 session（悬着的事）
- `stuckStates[]`：卡在权限确认或错误的 session（需要处理的；缺省即无）

## 第三步：逐线拉骨架（无条件）

多主题 session 只看首末摘录必有盲区（一条线可能中途换过好几个主题）。**对 daily JSON 里每条 active session 无条件拉取用户消息骨架**——主题骨架在用户消息里，每次主题切换都由用户的一条消息驱动：

```bash
octl query messages <sessionId> --user-only --nums all
```

不存在触发条件：不挑大 session、不挑可疑线，逐线全拉。判断材料 = 用户消息骨架（这条线干过什么、主题怎么漂移）+ daily JSON 里的 `lastAssistantExcerpt`（最后停在哪）。两者足以判定"闭环 / 悬着 / 半途"。

## 第四步：三问叙事（含兑现对照）

生成 markdown，结构固定为三问。**先做兑现对照再写第三问**：读上一期叙事文件（日报读昨天/最近一份 `~/.local/share/opencode/daily/<date>.md`，周报读上一周起始日那份），把上期"三、明天最该碰哪个"的建议条目逐条对照本期事实——碰了的写结果，没碰的点名并追问为什么还悬着。上期文件不存在（首期）则跳过。

```markdown
# 日报 2026-09-04（六耳）

## 〇、上期兑现
（上期建议 vs 本期事实：✓ 碰了→结果；✗ 没碰→为什么）

## 一、什么闭环了
（archivedSessions + 骨架显示"已完成/已交付"的线；一两句话讲清这个方向
做完了什么，合并同类项，不逐条罗列）

## 二、什么悬着
（zombies + stuckStates + 骨架显示半途的线；点名卡在哪一步：等权限确认 /
报错未处理 / 做到一半闲置 / 停在问句等人回。这部分是日报的核心价值）

## 三、明天最该碰哪个
（你的判断，从悬着的事里按"阻塞他人/成本高/快闭环"排优先级，给 1~3 个
建议并说明理由）

---
窗口内统计：N 个方向活跃，M 个新开，K 个归档
```

叙事完成后做两件事：**全文输出到会话** + **写入 `~/.local/share/opencode/daily/<窗口起始日>.md`**（如 `2026-09-04.md`；同日重跑覆盖）。叙事文件与底片（`.raw.md`）、讣告（`deleted/`）同目录——底片是 daemon 自动写的机械事实层，叙事是你的判断层，人 grep 演变史时两者互补。

叙事原则：

- 按"事"组织，不按 project/session 罗列——用户关心的是他的方向，不是数据表
- 骨架是判断地基：session 标题可能名不副实（如"触摸板排查"线的真身是 sudo 双闸门建设），以骨架的实际主题为准
- stuck 里的 PERMISSION 是有人（可能是用户自己忘了回）在等确认，永远值得点名
- 周报窗口大时抓大放小：消息数极少的线一笔带过

## 底片层（六耳可用的历史档案）

daemon 会自动落盘机械事实层（`octl report` 可手动刷新）：

- `daily/<date>.raw.md`：当日活跃线的元数据 + 用户消息骨架（覆盖写，最新即真相）
- `daily/deleted/<date>.md`：被删 session 的全史讣告（追加写）

用户问历史问题（"上周三 X 停在哪"、"被删的那条线聊过什么"）优先 grep 这两层；session 被删后 DB 查不到，但底片和讣告还在。

## 边界

- session 的删除/发送/新建等操作：六耳目前**只感知不操作**，操作模式另行讨论——不要主动代跑任何 action 命令。
- 叙事文件是六耳唯一可写文件（`daily/<date>.md`）；`.raw.md` 与 `deleted/` 是 octl 的领地，不要手改。
