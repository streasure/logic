# 架构设计 —— logicserver（独立逻辑服进程）

核心原则：**网关是哑管道，业务状态只在逻辑服**。sgate 只管连接/鉴权/分流/帧，
logicserver 管业务/分组/端到端 ACK；两者只经 gRPC flip 流 + etcd 握手，
代码上零依赖（sgate 产品代码对 `github.com/streasure/logic` 的 import 恒为 0）。
本仓 = SDK（根包 `logic`）+ 服务（`cmd/logicserver` + `internal/*`），服务是 SDK 的第一个真实消费者。

## 1. 定位与边界

```
E:\logic\
├── cmd/logicserver/main.go     # 组合根（唯一装配点）：flag → tlog → config → NewService → 注册 → 观测 → Start → 信号 → drain → Stop
├── internal/
│   ├── config/                 # Config + Load + validate（显式传参，零包级全局态）
│   ├── handler/                # 业务 cmd handler（经 RegisterProto 挂接）+ drain 在途状态
│   ├── probe/                  # readiness 判定（三个只读观测 func 注入，判定可单测）
│   └── metrics/                # /healthz /readyz /counters 观测端点（只读导出）
├── config/                     # 4 件套：logicserver{,_stress}.yaml + tlog{,_stress}.yaml
├── scripts/                    # start.bat / start-linux.sh（压测配置可传参）+ signal-helper.ps1（Windows 优雅信号）
├── docs/architecture.md        # 本文件
└── bin/                        # 构建产物（gitignore）
```

**边界规则**（比编译器更严，靠 `go list` 机检）：

- SDK 根包 `logic` 永不 import `cmd/`、`internal/`——方向单向：服务 → SDK；
  判据 `go list -json .` 的 Imports 中不出现 `streasure/logic/(internal|cmd)`。
- 业务 handler 只能经 `RegisterProto` / `svc.Server()` 公开 API 挂接，不伸手进 SDK 内部状态。
- 无运行时可变包状态（init 期注册允许）；配置由 main 加载后逐层注入。

## 2. 启动装配与生命周期

装配顺序（`cmd/logicserver/main.go`，唯装配点）：

```
flag(-conf/-logger/-version) → tlog.Init → config.Load(validate 失败即失败启动)
  → logic.NewService(选项逐条显式列出) → handler.New(svc, cfg)（业务注册 + drain 状态）
  → probe.Checker（三个只读观测 func 注入）→ metrics.New
  → svc.Start() → metrics.Start()（bind 失败即失败启动）→ 信号循环
```

组合根用 `Start()` + 自有信号循环替代 SDK 的 `Run()`：`Run` 收到信号直接 `Stop()`，
插入不了业务在途宽限；SDK 停机顺序不动，drain 是 `Stop()` 之前的额外一步。

停机（信号到达后，顺序即语义）：

1. `handler.BeginDrain()` — 置 draining 标志：新到业务请求直接拒绝（dropped++），
   `/readyz` 随即 503（**摘流发生在停流之前**，网关不再把新会话路由进来）
2. `drainWait` — 轮询等 `InFlight()==0 && PendingReliable()==0`，最多 `drainGrace`
   （默认 10s，0 = 不等待）；超时 WARN 带余量后强制继续（挂死的停机比丢尾部消息更糟）
3. `Service.Stop()` — SDK 原有顺序不可换：
   1. `dialer.Stop()` — 停 flip 拨入与网关发现
   2. `registry.Destroy()` — 注销 etcd 注册键
   3. `server.Stop()` — **先关全部存量流**（否则 `GracefulStop` 会永远等长活双向流，SIGINT 挂死）
   4. `grpcServer.GracefulStop()`（5s 超时兜底强杀）
   5. `listener.Close()`
4. `metrics.Stop()` — Shutdown 等在途 scrape 完成（3s 兜底强关）

## 3. 服务发现与 flip（主动拨入网关）

| 项 | 值（代码事实） |
| --- | --- |
| etcd 注册键 | `/services/{belong}/SERVER_TYPE_LOGICSERVER:{zone}/{instanceId}`，value = `AdvertiseAddr`（`OutboundIPv4:gRPC 端口`），LeaseTTL 默认 10s |
| 网关发现 | `{belong}/SERVER_TYPE_SGATE:{zone}`；无 etcd 时仅静态 `gateways` 并告警 |
| flip 握手元数据 | `LogicID = ServiceID`、`Zone`——网关按 `LogicID` 建 logic client，**服务实例 ID 必须与网关路由的 serverId 一致** |
| 分片/连接组 | `shardCount` 0 → `NumCPU*8`；`connGroupCount` 0 → 4 条独立 TCP 连接组 |
| gRPC 服务端 | 窗口 512KB、单消息 4MB、keepalive `MinTime=5s` `PermitWithoutStream=true` |
| gRPC 客户端 | 窗口 512KB、单消息 4MB、活跃 ping（防 NAT/空闲断链） |

连接责任在 logic 侧（flip）：重连、抖动、NAT 穿透都由持有业务状态的一方承担，
网关只被动接受拨入。

## 4. 上行分发与应答

- `RegisterProto(cmd, reqProto, respCmd, handler)` 按命令码注册，请求体按 `reqType` 解码（对象池复用）。
- 分发顺序：已注册命令 → 内置命令（`heartbeat` 空处理、`useroffline` 清会话/分组/用户映射、`msgack` 移出未确认窗口）→ 未注册则 `WARN` 丢弃。
- 应答回调：`handler` 返回 `nil` **或** `respCmd == 0` 都不回包；否则按 `respCmd` 编码下发，
  `userKey` 优先取 `resp.GetUserKey()`（非空则覆盖），`SeqId` 沿用请求。

## 5. 会话 / 分组 / 推送（单写者）

每份状态只有一个 owner：**连接归网关、会话索引归 loginserver、分组与 ACK 归 logic**。
本仓持有的状态与 API（`svc.Server()`）：

| 类别 | API |
| --- | --- |
| 用户/会话 | `RegisterUser` `UnregisterUser` `GetConnectionIDByUser` `Offline` `GetConnectionCount` |
| 分组 | `JoinGroup` `LeaveGroup` `JoinGroupForUser` `LeaveGroupForUser` `GetGroupMembers` `GetGroupCount` |
| 下行 | `PushToConnection`（尽力）`SendToUser` `SendToGroup` `Broadcast` `SendToGroupWithRetry` `Kick` |
| 可靠推送 | `PushReliable`：端到端 ACK + 指数退避重传 + **每会话窗口背压**（默认窗口 64、扫描 200ms、退避 300ms→5s、重试 5 次，`reliability.*` 可配） |
| 观测 | `PushMetricsSnapshot()`（内存计数）+ `Registered()` `ActiveStreams()` `GetGroupTotal()` `PendingReliable()` 只读快照——经 `internal/metrics` 暴露成 `/counters` |

行为契约（分组语义、断线清窗、心跳保活、推送 API 细节）的权威在 sgate 仓
`docs/logic-guide.md`，本仓只链接不复制。

## 6. 配置

- **显式传参、零包级全局态**：`config.Load(path)` 返回值由 main 持有并注入
  （`NewService` 选项 + `handler.Register`），不提供 `GetConfig`。
- `uconfig` 承载默认值（`default` 标签）与严格解码（未消费的 yaml 键报错），
  `validate` 必填/范围校验失败即失败启动。
- 环境项：`ports.metricsAddr`（观测端点，默认仅本机回环 `127.0.0.1:6061`，
  `host:port` + 端口范围校验）与 `drainGrace`（默认 10s，显式 0 = 不等待，负值非法）。
- 4 件套：常规 `logicserver.yaml` + `tlog.yaml`，压测 `logicserver_stress.yaml` + `tlog_stress.yaml`
  （压测仅日志降噪为 error 级——本服务无限流/准入/熔断等防护可关）。

## 7. 与 sgate / loginserver 的协作

| 方向 | 机制 | 说明 |
| --- | --- | --- |
| logicserver → sgate | flip gRPC 双向流（etcd 发现后主动拨入） | 上行命令由网关转发、下行推送由本服务下发；`LogicID` 标识实例 |
| sgate → logicserver | 同一条 flip 流 | 网关按 `serverId` 路由会话到对应逻辑服实例 |
| loginserver ↔ logicserver | **无直连** | 登录态索引在 loginserver（对 sgate 校验），业务态在 logicserver，二者不经手对方 |

依赖方向（零反向）：本仓只依赖 `protocol` + `util` + gRPC/protobuf；
不 import sgate、不 import loginserver。

## 8. 已知取舍与缺口

| 项 | 状态 |
| --- | --- |
| 业务在途宽限的优雅 drain | ✅ 阶段 3（组合根 `BeginDrain` → `drainWait` → `Stop`；SDK 停机顺序未动） |
| `/counters` + `/healthz` `/readyz` 探针 | ✅ 阶段 3（`127.0.0.1:6061`，readiness = 未 drain + 已注册 + flip 流 ≥1） |
| logic 实例故障契约（重启后会话重路由/状态重同步） | 设计空白，待补一节 |
| 下行 coalescing 无界背压 | 在 **sgate** 侧（`dataplane/connection.go AppendCoalesced`），见 sgate `todo.md`，非本仓 |
| pprof / 指标端点 | pprof 未接（阶段 3 只落 `/counters` 探针三件套，pprof 按需再开） |
| 配置暴露 `grpcWindowSize` 等 SDK 传输参数 | 暂不暴露（默认 512KB 与 bench 口径一致）；确需对称网关 64MB 窗口时补 additive 选项 |
