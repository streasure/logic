// logicserver 组合根（唯一装配点）。
//
// 装配顺序：flag → tlog → config.Load → NewService → handler 注册 → 观测端点
// → svc.Start → 信号 → drain（拒新业务、等在途排空）→ svc.Stop → 观测收尾。
// 边界：本文件只做装配与生命周期，不含业务；业务 handler 全部在 internal/handler，
// 经 SDK 公开 API 挂接（SDK 根包永不反向依赖 cmd/internal，方向单向：服务 → SDK）。
//
// 为什么不用 SDK 的 svc.Run()：Run 收到信号后直接 Stop，插入不了业务在途
// 宽限（drain）；SDK 停机顺序（dialer → registry → server → GracefulStop）
// 保持不动，drain 是 Stop 之前的额外一步，由本组合根负责。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/streasure/logic/internal/config"
	"github.com/streasure/logic/internal/handler"
	"github.com/streasure/logic/internal/metrics"
	"github.com/streasure/logic/internal/probe"

	logic "github.com/streasure/logic"
	"github.com/streasure/util/tlog"
)

// version 发布构建经 -ldflags "-X main.version=x.y.z" 覆盖（阶段 5 tag 治理接入）。
var version = "dev"

var (
	confFiles  = flag.String("conf", "config/logicserver.yaml", "config file path")
	loggerConf = flag.String("logger", "config/tlog.yaml", "log configuration file")
	showVer    = flag.Bool("version", false, "show version")
)

// drainPollInterval drain 等待的轮询间隔（在途归零无事件可订阅，轮询足够）。
const drainPollInterval = 50 * time.Millisecond

// metricsStopTimeout 观测端点优雅关闭的兜底超时。
const metricsStopTimeout = 3 * time.Second

func main() {
	flag.Parse()
	if *showVer {
		fmt.Printf("logicserver version: %s\n", version)
		return
	}

	// 日志最先初始化（进程生命周期最先，defer Close 收尾；全程 tlog）
	if err := tlog.Init(*loggerConf); err != nil {
		// tlog 未就绪时无法用 tlog 记录，只能写 stderr（sgate/loginserver 同款）
		fmt.Fprintf(os.Stderr, "failed to initialize tlog: %v\n", err)
		return
	}
	defer tlog.Close()

	// 配置显式传参（无包级全局态），校验失败即失败启动
	cfg, err := config.Load(*confFiles)
	if err != nil {
		tlog.Error(context.TODO(), "load config failed error=%v", err)
		return
	}

	// 服务装配：选项逐条显式列出，未列出的走 SDK defaultConfig
	opts := []logic.ServiceOption{
		logic.WithListenPort(strconv.Itoa(cfg.Ports.GrpcPort)),
		logic.WithServiceID(cfg.ServerID),
		logic.WithServerType(cfg.ServerType),
		logic.WithServiceName(cfg.ServiceName),
		logic.WithBelong(cfg.Belong),
		logic.WithZone(cfg.Zone),
		logic.WithEtcdEndpoints(cfg.Etcd.Endpoints...),
		logic.WithShardCount(cfg.ShardCount),
		logic.WithReliability(cfg.Reliability),
	}
	if len(cfg.Gateways) > 0 {
		opts = append(opts, logic.WithGateways(cfg.Gateways...))
	}
	svc := logic.NewService(opts...)

	// 业务注册：只经 SDK 公开 API，不伸手进 SDK 内部状态；返回 drain 状态持有者
	h := handler.New(svc, cfg)

	// 观测：readiness = 未 drain + etcd 注册完成 + flip 流 ≥1（三个只读观测 func 注入）
	checker := &probe.Checker{
		Registered: svc.Registered,
		Streams:    svc.Server().ActiveStreams,
		IsDraining: h.Draining,
	}
	mc := metrics.New(context.TODO(), cfg.Ports.MetricsAddr, svc, checker)

	// 信号通道先建：启动窗口内到来的信号不丢（channel 容量 1，信号不阻塞）
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	tlog.Info(context.TODO(), "logicserver starting version=%s serverId=%s belong=%s zone=%s grpcPort=%d metricsAddr=%s drainGrace=%s etcd=%v",
		version, cfg.ServerID, cfg.Belong, cfg.Zone, cfg.Ports.GrpcPort, cfg.Ports.MetricsAddr,
		cfg.DrainGrace, cfg.Etcd.Endpoints)

	if err := svc.Start(); err != nil {
		tlog.Error(context.TODO(), "logicserver start failed error=%v", err)
		return
	}
	// 观测端点启动失败即失败启动（起不来的服务不能假装可观测）
	if err := mc.Start(); err != nil {
		tlog.Error(context.TODO(), "metrics start failed error=%v", err)
		svc.Stop()
		return
	}

	<-sigCh
	tlog.Info(context.TODO(), "logicserver signal received, draining grace=%s", cfg.DrainGrace)

	// 优雅停机：先拒新业务（readiness 随即 503，摘流发生在停流之前），
	// 等在途 handler 与未确认可靠推送排空；超时带余量告警后强制继续（不挂死）。
	h.BeginDrain()
	drainWait(context.TODO(), svc, h, cfg.DrainGrace)

	svc.Stop()
	stopCtx, cancel := context.WithTimeout(context.Background(), metricsStopTimeout)
	mc.Stop(stopCtx)
	cancel()

	tlog.Info(context.TODO(), "logicserver stopped serverId=%s", cfg.ServerID)
}

// drainWait 等待业务在途归零（在途 handler + 未确认可靠推送），最多等 grace
// （0 = 不等待）。超时只告警不阻塞：挂死的停机比丢尾部消息更糟。
func drainWait(ctx context.Context, svc *logic.Service, h *handler.Handlers, grace time.Duration) {
	deadline := time.Now().Add(grace)
	for {
		inFlight := h.InFlight()
		pending := svc.Server().PendingReliable()
		if inFlight == 0 && pending == 0 {
			tlog.Info(ctx, "drain complete inFlight=0 pendingReliable=0 dropped=%d", h.Dropped())
			return
		}
		if time.Now().After(deadline) {
			tlog.Warn(ctx, "drain grace timeout, forcing stop inFlight=%d pendingReliable=%d dropped=%d grace=%s",
				inFlight, pending, h.Dropped(), grace)
			return
		}
		time.Sleep(drainPollInterval)
	}
}
