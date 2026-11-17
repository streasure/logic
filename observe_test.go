package logic

import "testing"

// TestRegisteredSkipEtcd 未配置注册（ServiceID/EtcdEndpoint 为空）时，
// initRegistry 视为注册完成：没有可等待的注册动作，readiness
// 不应因"本就没配 etcd"而永远失败；配置了但注册失败则保持未就绪。
func TestRegisteredSkipEtcd(t *testing.T) {
	cases := []struct {
		name string
		opts []ServiceOption
	}{
		{"无 ServiceID", nil},
		{"无 etcd 端点", []ServiceOption{WithServiceID("t1"), WithEtcd("")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(tc.opts...)
			if svc.Registered() {
				t.Fatal("Registered() = true before initRegistry")
			}
			svc.initRegistry()
			if !svc.Registered() {
				t.Fatal("Registered() = false after initRegistry with registration unconfigured")
			}
		})
	}
}

// TestObservationCountersZeroState 新建服务的观测快照零状态：
// 无网关流、无分组、无未确认可靠推送（/counters 与 drain 的取数入口）。
func TestObservationCountersZeroState(t *testing.T) {
	s := NewServer(WithServerID("obs-test"))
	if got := s.ActiveStreams(); got != 0 {
		t.Errorf("ActiveStreams() = %d, want 0", got)
	}
	if got := s.GetGroupTotal(); got != 0 {
		t.Errorf("GetGroupTotal() = %d, want 0", got)
	}
	if got := s.PendingReliable(); got != 0 {
		t.Errorf("PendingReliable() = %d, want 0", got)
	}
	if got := s.GetServerID(); got != "obs-test" {
		t.Errorf("GetServerID() = %q, want obs-test", got)
	}
}
