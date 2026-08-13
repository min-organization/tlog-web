#!/usr/bin/env bash
# 一键构建 tlog-web 单二进制（前端 embed 进 Go 二进制）。
# 用法: ./scripts/build.sh [GOOS] [GOARCH]
#   ./scripts/build.sh linux amd64     # 默认
#   ./scripts/build.sh linux arm64
#   ./scripts/build.sh darwin arm64
set -euo pipefail

GOOS="${1:-linux}"
GOARCH="${2:-amd64}"
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "==> frontend build"
cd frontend
npm ci --legacy-peer-deps
npm run build
cd "$ROOT"

echo "==> stage dist for embed"
mkdir -p backend/frontend
cp -r frontend/dist backend/frontend/dist

echo "==> go build ($GOOS/$GOARCH, version=$VERSION)"
cd backend
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -ldflags="-s -w -X main.version=${VERSION}" \
  -o "../tlog-web-${GOOS}-${GOARCH}" .
cd "$ROOT"

echo "==> done: tlog-web-${GOOS}-${GOARCH}"
