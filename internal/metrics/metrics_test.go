package metrics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	logic "github.com/streasure/logic"
)

// fakeState 手动控制的 State（probe.Checker 的接口契约由 probe 包自己的
// 单测覆盖，这里只验证 metrics 对接口的消费）。
type fakeState struct {
	ready    bool
	reason   string
	draining bool
}

func (f *fakeState) Ready() (bool, string) { return f.ready, f.reason }
func (f *fakeState) Draining() bool        { return f.draining }

// newTestServer 起一个只挂路由的 httptest 服务：svc 仅 NewService 构造、
// 不 Start（无网络、无注册）。
func newTestServer(t *testing.T, state State) (*httptest.Server, *logic.Service) {
	t.Helper()
	svc := logic.NewService(logic.WithServiceID("logic-test"))
	s := New(context.Background(), "127.0.0.1:0", svc, state)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, svc
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestHealthzAlwaysOK(t *testing.T) {
	ts, _ := newTestServer(t, &fakeState{ready: false, reason: "no gateway stream"})
	status, body := get(t, ts.URL+"/healthz")
	if status != http.StatusOK || body != "ok\n" {
		t.Fatalf("GET /healthz = (%d, %q), want (200, \"ok\\n\")", status, body)
	}
}

func TestReadyz(t *testing.T) {
	cases := []struct {
		name       string
		state      *fakeState
		wantStatus int
		wantBody   string
	}{
		{"就绪", &fakeState{ready: true, reason: "ok"}, http.StatusOK, "ready: ok\n"},
		{"未就绪", &fakeState{ready: false, reason: "no gateway stream"},
			http.StatusServiceUnavailable, "not ready: no gateway stream\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newTestServer(t, tc.state)
			status, body := get(t, ts.URL+"/readyz")
			if status != tc.wantStatus || body != tc.wantBody {
				t.Fatalf("GET /readyz = (%d, %q), want (%d, %q)",
					status, body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestCounters(t *testing.T) {
	ts, _ := newTestServer(t, &fakeState{ready: false, reason: "draining", draining: true})
	status, body := get(t, ts.URL+"/counters")
	if status != http.StatusOK {
		t.Fatalf("GET /counters status = %d, want 200", status)
	}

	var got countersPayload
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode counters: %v\nbody=%s", err, body)
	}
	if got.Service.ServerID != "logic-test" {
		t.Errorf("service.serverId = %q, want logic-test", got.Service.ServerID)
	}
	if !got.Service.Draining {
		t.Errorf("service.draining = false, want true（State 注入未生效）")
	}
	if got.Service.Registered || got.Service.GatewayStreams != 0 ||
		got.Service.Sessions != 0 || got.Service.Groups != 0 || got.Service.PendingReliable != 0 {
		t.Errorf("未启动服务的计数应全为零: %+v", got.Service)
	}
	// push 指标键是对账契约（totalPushed ↔ 网关 /stats pushedToClient）
	if _, ok := got.Push["totalPushed"]; !ok {
		t.Errorf("push 缺少对账键 totalPushed: %v", got.Push)
	}
	if got.Process.Goroutines <= 0 {
		t.Errorf("process.goroutines = %d, want > 0", got.Process.Goroutines)
	}
}

func TestCountersRejectsNonGET(t *testing.T) {
	ts, _ := newTestServer(t, &fakeState{ready: true, reason: "ok"})
	resp, err := http.Post(ts.URL+"/counters", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /counters: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /counters status = %d, want 405", resp.StatusCode)
	}
}
