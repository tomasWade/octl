#!/bin/bash
# publish-github.sh —— 把 master 工作区的公开内容同步到 main 分支并推送
#
# 模型：单仓双分支双 remote
#   master  私有全量历史（推 origin=Gitea，永不推 GitHub）
#   main    公开快照历史（orphan，推 github=GitHub + origin=Gitea）
# 两条历史永不 merge；"发布" = 按排除清单把 master 工作区文件同步到
# main 的临时 worktree 里做快照 commit。
#
# 用法：
#   ./scripts/publish-github.sh [--push] [--dry-run]
#
# 首次使用（已执行过一次可跳过）：
#   git remote add github git@github.com:tomasWade/octl.git
#   git fetch github main --tags && git branch main github/main
#   cp scripts/hooks/pre-push .git/hooks/pre-push && chmod +x .git/hooks/pre-push
#
# 环境变量：
#   PUBLISH_NAME   提交作者名（默认 haotianyu）
#   PUBLISH_EMAIL  提交作者邮箱（默认 haotianyu1@163.com）

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
PUBLISH_NAME="${PUBLISH_NAME:-haotianyu}"
PUBLISH_EMAIL="${PUBLISH_EMAIL:-haotianyu1@163.com}"

PUSH=0
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --push)    PUSH=1 ;;
    --dry-run) DRY_RUN=1 ;;
    *) echo "未知参数: $arg" >&2; exit 2 ;;
  esac
done

EXCLUDE_FILE="$PROJECT_DIR/scripts/publish-exclude.txt"
[ -f "$EXCLUDE_FILE" ] || { echo "错误：排除清单不存在: $EXCLUDE_FILE" >&2; exit 1; }

log() { echo "[publish] $*"; }

cd "$PROJECT_DIR"

# ---------- 1. 计算发布文件列表（master 的 git ls-files 减去排除清单） ----------
LIST="$(mktemp)"
trap 'rm -f "$LIST" "$LIST.tmp" "$LIST.main"' EXIT

git ls-files > "$LIST"
EXCLUDED=0
while IFS= read -r pat; do
  [ -z "$pat" ] && continue
  case "$pat" in \#*) continue ;; esac
  before=$(wc -l < "$LIST")
  if [[ "$pat" == */ ]]; then
    # 目录前缀：字面匹配，剔除该目录下所有文件
    awk -v p="$pat" 'index($0, p) != 1' "$LIST" > "$LIST.tmp" && mv "$LIST.tmp" "$LIST"
  else
    awk -v p="$pat" '$0 != p' "$LIST" > "$LIST.tmp" && mv "$LIST.tmp" "$LIST"
  fi
  after=$(wc -l < "$LIST")
  EXCLUDED=$((EXCLUDED + before - after))
done < "$EXCLUDE_FILE"

log "发布文件 $(wc -l < "$LIST") 个，排除 $EXCLUDED 个"
if [ "$DRY_RUN" = 1 ]; then
  log "--- dry-run：以下文件将被发布 ---"
  cat "$LIST"
  exit 0
fi

# ---------- 2. 临时 worktree 检出 main ----------
WT="$(mktemp -d /tmp/octl-publish.XXXXXX)"
cleanup() { git worktree remove --force "$WT" 2>/dev/null || rm -rf "$WT"; }
trap cleanup EXIT

git worktree add "$WT" main >/dev/null

# ---------- 3. 同步文件 ----------
(cd "$PROJECT_DIR" && rsync -a --files-from="$LIST" ./ "$WT/")
# 清掉 main 里有、但已不在发布清单里的文件（如文件被移进私有区）
git ls-tree -r --name-only main > "$LIST.main"
# awk 差集而非 grep -v：grep 零匹配时退出码 1 会触发 set -e
awk 'NR==FNR{want[$0]=1; next} !($0 in want)' "$LIST" "$LIST.main" | while IFS= read -r f; do
  [ -z "$f" ] && continue
  rm -f "$WT/$f"
done

# ---------- 4. 快照 commit ----------
cd "$WT"
git add -A
if git diff --cached --quiet; then
  log "无变更，跳过提交"
else
  git -c user.name="$PUBLISH_NAME" -c user.email="$PUBLISH_EMAIL" \
    commit -m "publish: snapshot $(date +%Y-%m-%d_%H:%M)" >/dev/null
  log "已提交: $(git log --oneline -1 --format='%h %s [%ae]')"
fi

# ---------- 5. 推送 ----------
if [ "$PUSH" = 1 ]; then
  log "推送 main → github (GitHub) 与 origin (Gitea)"
  git push github main
  git push origin main
else
  log "未推送。确认后执行: ./scripts/publish-github.sh --push"
fi
