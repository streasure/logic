package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// baseYAML 最小合法配置（其余字段走 default 标签）。
const baseYAML = `
belong: "default"
zone: "default"
serverId: "t1"
ports:
  grpcPort: 50053
etcd:
  endpoints: ["http://127.0.0.1:2379"]
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "logicserver.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// TestLoadDefaults 新增字段的默认值：观测端点仅本机回环、drain 10s。
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, baseYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ports.MetricsAddr != "127.0.0.1:6061" {
		t.Errorf("metricsAddr = %q, want 127.0.0.1:6061", cfg.Ports.MetricsAddr)
	}
	if cfg.DrainGrace != 10*time.Second {
		t.Errorf("drainGrace = %s, want 10s", cfg.DrainGrace)
	}
}

// TestLoadExplicitZeroDrainGrace 显式 0 保留（= 停机不等待），不被 default 覆盖。
func TestLoadExplicitZeroDrainGrace(t *testing.T) {
	cfg, err := Load(writeTemp(t, baseYAML+"drainGrace: 0s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DrainGrace != 0 {
		t.Errorf("drainGrace = %s, want 0（显式 0 应保留）", cfg.DrainGrace)
	}
}

// TestLoadInvalidMetricsAddr 端点格式与端口范围校验失败即失败启动。
func TestLoadInvalidMetricsAddr(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"非 host:port", "nope"},
		{"端口超界", "127.0.0.1:70000"},
		{"端口非数字", "127.0.0.1:http"},
		{"显式空串", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaml := strings.Replace(baseYAML, "  grpcPort: 50053",
				"  grpcPort: 50053\n  metricsAddr: \""+tc.addr+"\"", 1)
			_, err := Load(writeTemp(t, yaml))
			if err == nil {
				t.Fatalf("metricsAddr %q 应校验失败", tc.addr)
			}
		})
	}
}

// TestLoadNegativeDrainGrace 负值非法（0 才是"不等待"）。
func TestLoadNegativeDrainGrace(t *testing.T) {
	_, err := Load(writeTemp(t, baseYAML+"drainGrace: -1s\n"))
	if err == nil {
		t.Fatal("drainGrace: -1s 应校验失败")
	}
}
