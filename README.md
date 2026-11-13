# logic

逻辑服侧 SDK（Go 模块 `github.com/streasure/logic`，包名 `logic`）。

从 `github.com/streasure/sgate` 独立出来的逻辑服嵌入库：封装网关↔逻辑服
gRPC 双向流服务端、etcd 服务注册、flip 模式拨入网关（logic 主动连 sgate）、
上行命令分发（RegisterProto）、会话/用户/分组管理，以及可靠推送
（PushReliable：端到端 ACK + 重传 + 窗口背压）。

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

## 开发

- 日志统一 `github.com/streasure/util/tlog`，禁止 `fmt.Print*` / `log.*`
- 门禁：`gofmt -l .` / `go build ./...` / `go vet ./...` /
  `go test ./... -count=1` / `staticcheck ./...`
- 跨仓工具链：`update-gomod.bat`（GONOSUMDB 跳过 sumdb 校验）
