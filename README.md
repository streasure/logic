# logic

逻辑服侧 SDK（Go 模块 `github.com/streasure/logic`，包名 `logic`）。

从 `github.com/streasure/sgate` 独立出来的逻辑服嵌入库：封装网关↔逻辑服
gRPC 双向流服务端、etcd 服务注册、flip 模式拨入网关（logic 主动连 sgate）、
上行命令分发（RegisterProto）、会话/用户/分组管理，以及可靠推送
（PushReliable：端到端 ACK + 重传 + 窗口背压）。

本仓同时包含**可执行服务** `cmd/logicserver`（骨架与边界见
[`docs/architecture.md`](docs/architecture.md)）：SDK 的第一个真实消费者。

## 依赖

- `github.com/streasure/protocol` — wire 契约（命令码、StreamData、
  flip 握手元数据）
- `github.com/streasure/util` — tlog / panicutil / uetcd / udiscovery 等
- `google.golang.org/grpc`、`google.golang.org/protobuf`

本仓 `go.mod` 使用本地 `replace` 指向同级 `../protocol`、`../util`、
`../treasure-slog`（与 sgate 同款开发期布局）；对外发布走版本号消费。

## 快速开始

```go
import logic "github.com/streasure/logic"

svc := logic.NewService(
    logic.WithServiceID("logic1"),
    logic.WithZone("default"),
    logic.WithEtcd("http://127.0.0.1:2379"),
)
// ... 注册处理器、推送 ...
svc.Run()
```

完整接入手册（配置项、推送 API、可靠推送语义、联调清单）见 sgate 仓
`docs/logic-guide.md`。

## 运行 logicserver

装配、停机顺序、flip 握手与包边界规则见 [`docs/architecture.md`](docs/architecture.md)。

```bash
# 常规（默认 config/logicserver.yaml + config/tlog.yaml）
scripts\start.bat                       # Windows
./scripts/start-linux.sh                # Linux/macOS

# 压测（配置传参；压测日志仅 error 级）
scripts\start.bat config\logicserver_stress.yaml config\tlog_stress.yaml
./scripts/start-linux.sh ./config/logicserver_stress.yaml ./config/tlog_stress.yaml

# 等价单起
go run ./cmd/logicserver -conf config/logicserver.yaml -logger config/tlog.yaml
```

配置 4 件套（`config/`）：

| 文件 | 用途 |
| --- | --- |
| `logicserver.yaml` | 常规：身份（belong/zone/serverId）、gRPC 端口、etcd 端点 |
| `tlog.yaml` | 常规日志（info 级，写 `logs/logicserver.log`） |
| `logicserver_stress.yaml` | 压测：与常规同构（本服务无限流/准入等防护可关） |
| `tlog_stress.yaml` | 压测日志（仅 error 级，降噪） |

**`serverId` 必须与网关路由的 `serverId` 一致**（它是 etcd instanceID 也是 flip
握手的 `LogicID`），本机验收与 `bench1_ws -server-id logic1-ws` 对齐。

端到端验收口径（阶段 1 已过）：起服后 etcd 存在
`/services/{belong}/SERVER_TYPE_LOGICSERVER:{zone}/{serverId}` → 网关 flip 拨入
（日志 `gateway dial session established`）→ `bench1_ws` 登录全通、
logicserver 侧 `login gate bound` 条数 = 登录数、无 `unregistered cmd`。

## 观测与优雅停机（阶段 3）

观测端点 `ports.metricsAddr`（默认 `127.0.0.1:6061`，仅本机回环）：

| 路径 | 语义 |
| --- | --- |
| `GET /healthz` | 存活：进程活着即 200 `ok` |
| `GET /readyz` | 就绪：200 `ready: ok`；否则 503 + 原因（`draining` / `not registered` / `no gateway stream`），按状态码摘挂 |
| `GET /counters` | 计数 JSON：`service`（registered/gatewayStreams/sessions/groups/pendingReliable/draining）、`push`（totalPushed 等，与网关 `/stats` 对账）、`process` |

```bash
curl http://127.0.0.1:6061/healthz     # 200 ok
curl http://127.0.0.1:6061/readyz      # 未接网关流时 503 no gateway stream
curl http://127.0.0.1:6061/counters    # 对账：push.totalPushed ↔ 网关 /stats pushedToClient
```

优雅停机（SIGINT/SIGTERM，`drainGrace` 默认 10s）：置 draining（`/readyz` 随即
503 摘流）→ 等在途 handler 与未确认可靠推送归零 → SDK 停机（停拨入 → 注销 etcd →
关流 → GracefulStop）→ 关观测端点。超时仅 WARN 强制继续，不挂死。

Windows 控制台投递信号（`taskkill` 不带 `/F` 对控制台进程无效）：

```powershell
. .\scripts\signal-helper.ps1   # 同一会话内：启动与投递必须同一条 shell 命令
$pid = Start-InGroup -Exe "bin\logicserver.exe" -Args "-conf config\logicserver.yaml -logger config\tlog.yaml" -Cwd (Get-Location)
Send-Break -TargetPid $pid      # CTRL_BREAK → Go 运行时映射 SIGINT
```

## 版本治理

- **基线**：`v0.1.0`（记录见 [`CHANGELOG.md`](CHANGELOG.md)）。SDK 导出面纪律 =
  **additive-only**（只增不改不删，`internal/*` 不算导出面）；破坏性变更须同轮修复
  sgate `bench/logic_*` 全部编译通过、提交信息标注「SDK 破坏性变更」、sgate
  `docs/logic-guide.md` 同步更新（sgate `docs/logicserver-design.md` §3.5 规则 4）。
- **兼容性门禁**：`bash scripts/apidiff.sh`（基准 = 最近 tag → HEAD，退出码
  0=兼容 / 1=不兼容 / 2=环境错误）。依赖 `go install github.com/joelanford/go-apidiff@main`
  （Windows 必须 `@main`：v0.8.3 及以下缺 `//go:build`，Go 1.26 编译不过）。要求干净
  工作区——go-apidiff 原地 checkout 两个 commit、结束自动还原 HEAD，运行期间勿操作本仓；
  连带会清掉 untracked 内容（含 gitignored 的 `bin/`、`logs/`，实测，均可重建）。
- **bump 规则（0.x）**：纯修复/内部重构（导出面不变）→ 可不发版，发则 patch；
  向后兼容新增 → minor（v0.2.0 起）；破坏性变更 → minor + CHANGELOG 显式标注 breaking
  （升 1.0.0 后破坏性 = major，且需模块路径 `/vN`）。
- **tag 流程**：五门禁绿的提交上打 annotated tag，`CHANGELOG.md` 同轮更新；tag 随
  提交一并推送（推送经用户确认）。
- **`replace => ../logic` 切 `require` 的时机**：① tag 已推送远端；② 出现本地平级
  布局之外的第二个消费者。届时 sgate 改 `require github.com/streasure/logic vX.Y.Z`
  （`protocol` / `util` / `treasure-slog` 的 replace 保留）。

## 开发

- 日志统一 `github.com/streasure/util/tlog`，禁止 `fmt.Print*` / `log.*`
- 门禁：`gofmt -l .` / `go build ./...` / `go vet ./...` /
  `go test ./... -count=1` / `staticcheck ./...`
- SDK 变更另跑：`bash scripts/apidiff.sh`（导出面兼容性，见上「版本治理」）
- 跨仓工具链：`update-gomod.bat`（GONOSUMDB 跳过 sumdb 校验）
