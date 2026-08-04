#!/usr/bin/env bash
#
# 构建前后端及飞书网关 Docker 镜像，tag 使用当前 git commit 短 SHA。
# 自动更新 compose.yaml 中的镜像 tag，并重启容器。
#
# 用法:
#   ./scripts/build-docker.sh              # 构建 + 更新 compose + 重启
#   ./scripts/build-docker.sh --no-restart # 构建 + 更新 compose，不重启
#   ./scripts/build-docker.sh --clean      # 构建前先 docker builder prune
#
set -euo pipefail

# 启用 BuildKit，利用 --mount=type=cache 缓存 go mod / npm 下载
export DOCKER_BUILDKIT=1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-/volume4/docker/archive/compose.yaml}"

cd "$REPO_DIR"

# ── 获取当前 git commit 短 SHA ────────────────────────────────────────────────
SHA=$(git rev-parse --short HEAD)
if [ -z "$SHA" ]; then
  echo "❌ 无法获取 git commit SHA，请在仓库目录下运行"
  exit 1
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  构建 Docker 镜像"
echo "  commit SHA: $SHA"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# ── 可选: 清理构建缓存 ────────────────────────────────────────────────────────
if [[ "${1:-}" == "--clean" || "${2:-}" == "--clean" ]]; then
  echo "🧹 清理 Docker 构建缓存..."
  docker builder prune -f
fi

# ── 构建后端 ──────────────────────────────────────────────────────────────────
echo ""
echo "📦 [1/3] 构建后端镜像 welink-backend:$SHA"
echo "   Dockerfile: backend/Dockerfile"
echo ""
docker build --network=host \
  -f backend/Dockerfile \
  -t "welink-backend:$SHA" \
  backend/

echo ""
echo "✅ 后端镜像构建完成: welink-backend:$SHA"

# ── 构建前端 ──────────────────────────────────────────────────────────────────
echo ""
echo "📦 [2/3] 构建前端镜像 welink-frontend:$SHA"
echo "   Dockerfile: frontend/Dockerfile"
echo ""
docker build --network=host \
  -f frontend/Dockerfile \
  -t "welink-frontend:$SHA" \
  frontend/

echo ""
echo "✅ 前端镜像构建完成: welink-frontend:$SHA"

# ── 构建飞书网关 ──────────────────────────────────────────────────────────────
echo ""
echo "📦 [3/3] 构建飞书网关镜像 welink-feishu-bot:$SHA"
echo "   Dockerfile: feishu-bot/Dockerfile"
echo ""
docker build --network=host \
  -f feishu-bot/Dockerfile \
  -t "welink-feishu-bot:$SHA" \
  feishu-bot/

echo ""
echo "✅ 飞书网关镜像构建完成: welink-feishu-bot:$SHA"

# ── 更新 compose.yaml ────────────────────────────────────────────────────────
if [ -f "$COMPOSE_FILE" ]; then
  echo ""
  echo "📝 更新 $COMPOSE_FILE 中的镜像 tag..."
  sed -i.bak \
    -e "s|image: welink-backend:.*|image: welink-backend:$SHA|" \
    -e "s|image: welink-frontend:.*|image: welink-frontend:$SHA|" \
    -e "s|image: welink-feishu-bot:.*|image: welink-feishu-bot:$SHA|" \
    "$COMPOSE_FILE"
  rm -f "$COMPOSE_FILE.bak"
  echo "✅ compose.yaml 已更新"
else
  echo "⚠️  compose.yaml ($COMPOSE_FILE) 不存在，跳过更新"
fi

# ── 重启容器 ──────────────────────────────────────────────────────────────────
NO_RESTART=false
if [[ "${1:-}" == "--no-restart" || "${2:-}" == "--no-restart" ]]; then
  NO_RESTART=true
fi

if [ "$NO_RESTART" = true ]; then
  echo ""
  echo "⏭️  --no-restart 模式，跳过容器重启"
else
  if [ -f "$COMPOSE_FILE" ]; then
    echo ""
    echo "🔄 重启容器..."
    cd "$(dirname "$COMPOSE_FILE")"
    docker compose up -d
    echo ""
    echo "✅ 容器已重启"
  fi
fi

# ── 清理旧镜像 ────────────────────────────────────────────────────────────────
echo ""
echo "🧹 清理旧镜像 (保留当前 SHA: $SHA)..."
OLD_IMAGES=$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E 'welink-(backend|frontend|feishu-bot):' | grep -v ":$SHA" || true)
if [ -n "$OLD_IMAGES" ]; then
  echo "删除旧镜像:"
  echo "$OLD_IMAGES" | while read -r img; do
    echo "  - $img"
    docker rmi "$img" 2>/dev/null || true
  done
else
  echo "没有旧镜像需要清理"
fi

# ── 清理悬空镜像 (headless / dangling) ───────────────────────────────────────
echo ""
echo "🧹 清理悬空镜像 (dangling/headless)..."
DANGLING_COUNT=$(docker images -f "dangling=true" -q | wc -l)
if [ "$DANGLING_COUNT" -gt 0 ]; then
  echo "删除 $DANGLING_COUNT 个悬空镜像..."
  docker image prune -f
else
  echo "没有悬空镜像需要清理"
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  ✅ 全部完成!"
echo "  镜像: welink-backend:$SHA, welink-frontend:$SHA, welink-feishu-bot:$SHA"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
