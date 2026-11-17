// Package config logicserver 服务配置：结构体 + Load + validate。
//
// 铁律：显式传参、零包级可变状态——不提供 GetConfig，配置由 main 加载后
// 逐层注入（config → handler / NewService 选项），可测试、无初始化顺序耦合。
package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	logic "github.com/streasure/logic"
	"github.com/streasure/util/uconfig"
)

// Config logicserver 顶层配置（config/logicserver.yaml）。
// 「基本不改」的数值（分片数、可靠推送参数）留零值走 SDK 默认，本文件只写环境项。
type Config struct {
	Belong      string                  `yaml:"belong" default:"default"`    // 所属应用/团队：etcd 注册键 {belong}/SERVER_TYPE_LOGICSERVER:{zone}
	Zone        string                  `yaml:"zone" default:"default"`      // 可用区：与网关同 zone 才会互拨
	ServerID    string                  `yaml:"serverId"`                    // 实例 ID：etcd instanceID + flip LogicID，须与网关路由的 serverId 一致
	ServerType  string                  `yaml:"serverType" default:"Logic"`  // 服务类型
	ServiceName string                  `yaml:"serviceName" default:"logic"` // 服务名称
	Ports       Ports                   `yaml:"ports"`                       // 端口组
	Etcd        Etcd                    `yaml:"etcd"`                        // etcd 注册与发现
	ShardCount  int                     `yaml:"shardCount"`                  // 拨入每个网关的流分片数（0 = NumCPU*8）
	Gateways    []string                `yaml:"gateways"`                    // 静态网关地址（etcd 不可用时的兜底）
	Reliability logic.ReliabilityConfig `yaml:"reliability"`                 // 可靠推送（端到端 ACK）参数，零值走 SDK 默认
	DrainGrace  time.Duration           `yaml:"drainGrace" default:"10s"`    // 停机 drain 最长等待：等在途 handler 与未确认可靠推送排空（0 = 不等待；负值非法）
}

// Ports 服务端口。
type Ports struct {
	GrpcPort    int    `yaml:"grpcPort" default:"50053"`             // gRPC 监听端口（flip 流入口）
	MetricsAddr string `yaml:"metricsAddr" default:"127.0.0.1:6061"` // 观测端点 host:port（/healthz /readyz /counters），默认仅本机回环
}

// Etcd etcd 端点；前缀/租约 TTL 走 SDK 默认（/services、10s），阶段 2 配置件套再暴露。
type Etcd struct {
	Endpoints []string `yaml:"endpoints"` // 端点列表，如 ["http://127.0.0.1:2379"]
}

// Load 读取并校验配置。显式传参：结果由调用方持有并继续传递，不做包级缓存。
func Load(path string) (*Config, error) {
	cfg, err := uconfig.Load[Config](path)
	if err != nil {
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return cfg, nil
}

// validate 必填项与取值范围校验，失败即失败启动（不留到运行期）。
func validate(cfg *Config) error {
	if cfg.Belong == "" {
		return errors.New("belong is required")
	}
	if cfg.Zone == "" {
		return errors.New("zone is required")
	}
	if cfg.ServerID == "" {
		return errors.New("serverId is required")
	}
	if cfg.ServerType == "" {
		return errors.New("serverType is required")
	}
	if cfg.Ports.GrpcPort <= 0 || cfg.Ports.GrpcPort > 65535 {
		return fmt.Errorf("ports.grpcPort must be in [1,65535], got %d", cfg.Ports.GrpcPort)
	}
	if len(cfg.Etcd.Endpoints) == 0 || cfg.Etcd.Endpoints[0] == "" {
		return errors.New("etcd.endpoints is required")
	}
	// 观测端点必填（设计案 §3.4：探针与 /counters 是上线最低观测能力），
	// 形如 host:port；显式空串会绕过 default 标签，由 SplitHostPort 拒绝。
	_, port, err := net.SplitHostPort(cfg.Ports.MetricsAddr)
	if err != nil {
		return fmt.Errorf("ports.metricsAddr must be host:port (e.g. 127.0.0.1:6061), got %q: %w",
			cfg.Ports.MetricsAddr, err)
	}
	if p, convErr := strconv.Atoi(port); convErr != nil || p < 1 || p > 65535 {
		return fmt.Errorf("ports.metricsAddr port must be in [1,65535], got %q", port)
	}
	if cfg.DrainGrace < 0 {
		return fmt.Errorf("drainGrace must be >= 0 (0 = no wait), got %s", cfg.DrainGrace)
	}
	return nil
}
