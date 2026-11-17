# 架构设计 —— logicserver（独立逻辑服进程）

核心原则：**网关是哑管道，业务状态只在逻辑服**。sgate 只管连接/鉴权/分流/帧，
logicserver 管业务/分组/端到端 ACK；两者只经 gRPC flip 流 + etcd 握手，
代码上零依赖（sgate 产品代码对 `github.com/streasure/logic` 的 import 恒为 0）。
本仓 = SDK（根包 `logic`）+ 服务（`cmd/logicserver` + `internal/*`），服务是 SDK 的第一个真实消费者。

## 1. 定位与边界

```
E:\logic\
├── cmd/logicserver/main.go     # 组合根（唯一装配点）：flag → tlog → config → NewService → 注册 → Run
├── internal/
│   ├── config/                 # Config + Load + validate（显式传参，零包级全局态）
│   └── handler/                # 业务 cmd handler（经 RegisterProto 挂接）
├── config/                     # 4 件套：logicserver{,_stress}.yaml + tlog{,_stress}.yaml
├── scripts/                    # start.bat / start-linux.sh（压测配置可传参）
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
  → logic.NewService(选项逐条显式列出) → handler.Register(svc, cfg) → svc.Run()
```

停机（`Run` 收 SIGINT/SIGTERM → `Service.Stop()`，顺序不可换）：

1. `dialer.Stop()` — 停 flip 拨入与网关发现
2. `registry.Destroy()` — 注销 etcd 注册键
3. `server.Stop()` — **先关全部存量流**（否则 `GracefulStop` 会永远等长活双向流，SIGINT 挂死）
4. `grpcServer.GracefulStop()`（5s 超时兜底强杀）
5. `listener.Close()`

缺口：**业务在途宽限**尚未做（收到信号后先拒新业务、等 `PushReliable` 队列与在途
handler 排空，再走上述顺序）——阶段 3 与 `/counters` 一并落地。

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
| 观测 | `PushMetricsSnapshot()`（内存计数，阶段 3 暴露成 `/counters`） |

行为契约（分组语义、断线清窗、心跳保活、推送 API 细节）的权威在 sgate 仓
`docs/logic-guide.md`，本仓只链接不复制。

## 6. 配置

- **显式传参、零包级全局态**：`config.Load(path)` 返回值由 main 持有并注入
  （`NewService` 选项 + `handler.Register`），不提供 `GetConfig`。
- `uconfig` 承载默认值（`default` 标签）与严格解码（未消费的 yaml 键报错），
  `validate` 必填/范围校验失败即失败启动。
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
| 业务在途宽限的优雅 drain | 缺口，阶段 3（SDK 停机顺序已定，勿改） |
| `/counters` + `/healthz` `/readyz` 探针 | 缺口，阶段 3（`PushMetricsSnapshot` 已就绪） |
| logic 实例故障契约（重启后会话重路由/状态重同步） | 设计空白，待补一节 |
| 下行 coalescing 无界背压 | 在 **sgate** 侧（`dataplane/connection.go AppendCoalesced`），见 sgate `todo.md`，非本仓 |
| pprof / 指标端口 | 未接（阶段 3 随 `/counters` 一并） |
| 配置暴露 `grpcWindowSize` 等 SDK 传输参数 | 暂不暴露（默认 512KB 与 bench 口径一致）；确需对称网关 64MB 窗口时补 additive 选项 |
