# Changelog

logic SDK（根包 `github.com/streasure/logic`）与 logicserver 服务的版本记录。
版本规则、tag 流程与 `replace => require` 时机见 [README「版本治理」](README.md#版本治理)；
导出面兼容性由 `scripts/apidiff.sh` 对最近 tag 自动检查（纪律 = additive-only，
`internal/*` 不在导出面内）。

## [v0.1.0] - 2026-11-19

首个基线 tag：SDK 导出面 + logicserver 骨架（sgate `docs/logicserver-design.md` 阶段 1~3）冻结。

### Added

- SDK 自 sgate `internal/logic` 迁出独立成仓（`994735b`）：
  - 装配与生命周期：`NewService` + `Service.Start|Run|Stop`、`NewServer`、`NewDialer`、
    `ServiceOption`（`WithEtcd` / `WithGateways` / `WithListenPort` / `WithServiceID` /
    `WithServiceName` / `WithServerType` / `WithZone` / `WithShardCount` / `WithReliability`）、
    `ServerOption`（`WithServerID` / `WithStreamChSize` / `WithStreamSendTimeout` / `WithReliableConfig`）
  - 上行：`RegisterProto` + `ProtoHandler` + `logic.Context`；用户/分组
    （`RegisterUser` / `JoinGroup` / `LeaveGroup` / `SendToUser` / `SendToGroup` /
    `Broadcast` / `Kick` / `Offline` 等）
  - 下行与可靠推送：`PushToConnection`、`PushReliable` + `ReliabilityConfig` +
    `PendingReliable`、`PushMetrics.GetSnapshot`
- `ServiceOption` 增 `WithBelong` 与 `WithEtcdEndpoints`（`8ea8750`：logicserver 显式
  belong + 多 etcd 端点，注册与发现同源）
- logicserver 服务 `cmd/logicserver`：
  - 阶段 1（`8ea8750`）：最小可运行骨架——`internal/config`（显式传参、无包级状态）、
    `internal/handler`（LoginGate 入组 + LoginReq/LoginAck 应答）、`config/*.yaml`
  - 阶段 2（`68d6b59`）：配置 4 件套（常规/压测 × logicserver/tlog）、`scripts/`
    （start.bat / start-linux.sh / signal-helper.ps1）、`docs/architecture.md`、README 运行说明
  - 阶段 3（`7050dc1`）：`internal/metrics`（`/healthz` `/readyz` `/counters`）、
    `internal/probe` readiness 三态、handler drain 门、组合根信号/drainWait 优雅停机、
    `ports.metricsAddr` / `drainGrace` 配置
- 发版治理（本次）：`CHANGELOG.md`（本文件）、`scripts/apidiff.sh` 导出面兼容性门禁、
  README「版本治理」段
