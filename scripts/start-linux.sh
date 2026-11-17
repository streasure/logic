#!/usr/bin/env bash
# logicserver 启动脚本（Linux/macOS）。
# 用法：
#   ./scripts/start-linux.sh                                    常规配置
#   ./scripts/start-linux.sh ./config/logicserver_stress.yaml ./config/tlog_stress.yaml   压测配置
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p bin
go build -o ./bin/logicserver ./cmd/logicserver

CONF="${1:-./config/logicserver.yaml}"
LOGGER="${2:-./config/tlog.yaml}"
exec ./bin/logicserver -conf "$CONF" -logger "$LOGGER"
