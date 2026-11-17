// logicserver 最小可运行骨架的组合根（唯一装配点）。
//
// 装配顺序：flag → tlog → config.Load → NewService → handler 注册 → Run。
// 边界：本文件只做装配与生命周期，不含业务；业务 handler 全部在 internal/handler，
// 经 SDK 公开 API 挂接（SDK 根包永不反向依赖 cmd/internal，方向单向：服务 → SDK）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/streasure/logic/internal/config"
	"github.com/streasure/logic/internal/handler"

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

	// 业务注册：只经 SDK 公开 API，不伸手进 SDK 内部状态
	handler.Register(svc, cfg)

	tlog.Info(context.TODO(), "logicserver starting version=%s serverId=%s belong=%s zone=%s grpcPort=%d etcd=%v",
		version, cfg.ServerID, cfg.Belong, cfg.Zone, cfg.Ports.GrpcPort, cfg.Etcd.Endpoints)

	// Run = Start + SIGINT/SIGTERM → 优雅停机（注销 etcd → 关存量流 → GracefulStop 兜底）
	if err := svc.Run(); err != nil {
		tlog.Error(context.TODO(), "logicserver run failed error=%v", err)
		return
	}

	tlog.Info(context.TODO(), "logicserver stopped serverId=%s", cfg.ServerID)
}
