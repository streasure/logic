#!/usr/bin/env bash
# logic SDK 导出面兼容性门禁（additive-only 纪律的自动化；sgate 设计案 §3.5 规则 4）。
#
# 方法：go-apidiff 比较两个 commit 的模块导出 API（internal/* 天然跳过）。
# 基准默认 = 最近的 tag，比较对象默认 = HEAD。
# 退出码：0=向后兼容 1=存在不兼容变更 2=环境/用法错误。
#
# go-apidiff 语义（勿绕过）：
#   - 要求工作区干净（未跟踪文件也算脏）——先提交再跑；
#   - 在本仓原地 checkout 两个 commit、结束自动还原 HEAD——运行期间勿操作本仓；
#   - 连带清理 untracked 内容（含 gitignored 的 bin/、logs/ 构建产物与运行日志，
#     实测会被删）——均可重建，跑前知悉；
#   - 原地执行，go.mod 的 replace => ../protocol 等同级布局照常解析
#     （sgate 同级 checkout 约束，设计案 §3.5）。
#
# 安装（Windows 必须 @main：v0.8.3 及以下缺 //go:build，Go 1.26 编译不过；
# Linux/macOS 可用 @latest）：
#   go install github.com/joelanford/go-apidiff@main
#
# 用法：
#   bash scripts/apidiff.sh              # 最近 tag -> HEAD
#   bash scripts/apidiff.sh <ref>        # 指定基准 ref -> HEAD
#   bash scripts/apidiff.sh <old> <new>  # 任意两个 ref（排障）
set -uo pipefail
cd "$(dirname "$0")/.."

old="${1:-}"
new="${2:-HEAD}"

if [ -z "$old" ]; then
    old="$(git describe --tags --abbrev=0 2>/dev/null || true)"
    if [ -z "$old" ]; then
        echo "apidiff: 尚无基准 tag（首个 tag 前先提交治理件并 git tag v0.1.0，或显式传 ref）" >&2
        exit 2
    fi
fi

if ! command -v go-apidiff >/dev/null 2>&1; then
    echo "apidiff: 未安装 go-apidiff：go install github.com/joelanford/go-apidiff@main" >&2
    exit 2
fi

if [ -n "$(git status --porcelain)" ]; then
    echo "apidiff: 工作区不干净（go-apidiff 要求干净树），先提交后重跑：" >&2
    git status --short >&2
    exit 2
fi

echo "apidiff: $old -> $new"
go-apidiff --repo-path . --print-compatible "$old" "$new"
rc=$?
case "$rc" in
    0) echo "apidiff: PASS（导出面向后兼容：无变化或仅 additive）" ;;
    1)
        echo "apidiff: FAIL（存在不兼容变更）。破坏性变更须走 sgate 设计案 §3.5 规则 4：" >&2
        echo "  sgate bench/logic_* 同轮修复编译 + 提交标注「SDK 破坏性变更」+ docs/logic-guide.md 同步" >&2
        ;;
    *) echo "apidiff: ERROR（go-apidiff 退出码 $rc）" >&2 ;;
esac
exit "$rc"
