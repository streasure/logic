// Package metrics logicserver 观测端点：/healthz（存活）+ /readyz（就绪）
// + /counters（计数快照，与网关 /stats 对账）。
//
// 边界：只读导出，不改任何业务状态；状态来源全部由组合根注入
// （*logic.Service 的公开只读快照 + 消费方定义的 State 接口），
// 本包不伸手进 SDK 内部实现。
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"

	logic "github.com/streasure/logic"
	"github.com/streasure/util/panicutil"
	"github.com/streasure/util/tlog"
)

// readHeaderTimeout 防慢速头部攻击（连接建立后不发完请求头就耗尽资源）。
const readHeaderTimeout = 5 * time.Second

// State 探针与 /counters 运行期状态的消费方定义接口，
// 由 internal/probe.Checker 实现（组合根注入三个只读观测 func）。
type State interface {
	// Ready 返回就绪判定与原因（/readyz 消费）。
	Ready() (bool, string)
	// Draining 返回是否已开始优雅 drain（/counters 消费；
	// readiness 侧由 Ready 一并覆盖）。
	Draining() bool
}

// Server 观测 HTTP 端点（只读，进程内与业务同生命周期）。
type Server struct {
	ctx   context.Context
	addr  string
	svc   *logic.Service
	state State
	http  *http.Server
	ln    net.Listener
}

// New 创建观测端点。addr 形如 "127.0.0.1:6061"（配置默认仅本机回环）；
// 端口绑定在 Start 同步进行，New 不产生任何副作用，可先于业务启动构造。
func New(ctx context.Context, addr string, svc *logic.Service, state State) *Server {
	s := &Server{ctx: ctx, addr: addr, svc: svc, state: state}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/counters", s.handleCounters)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	return s
}

// Handler 返回路由（测试用 httptest.NewServer(s.Handler())）。
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Start 同步绑定端口（失败立即返回错误，组合根据此失败启动——
// 起不来的服务不能假装可观测），Serve 交 panicutil.Go 异步运行。
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("metrics listen on %s: %w", s.addr, err)
	}
	s.ln = ln
	panicutil.Go(s.ctx, "logicserver.metricsServe", func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			tlog.Error(s.ctx, "metrics server stopped error=%v", err)
		}
	})
	tlog.Info(s.ctx, "metrics endpoint started addr=%s", s.addr)
	return nil
}

// Stop 优雅关闭：先 Shutdown 等在途 scrape 完成，超时由调用方 ctx 兜底强关。
func (s *Server) Stop(ctx context.Context) {
	if s.ln == nil {
		return
	}
	if err := s.http.Shutdown(ctx); err != nil {
		tlog.Warn(s.ctx, "metrics shutdown not clean error=%v", err)
		_ = s.http.Close()
	}
	tlog.Info(s.ctx, "metrics endpoint stopped addr=%s", s.addr)
}

// handleHealthz 存活探针：进程活着即 200（就绪与否交给 /readyz）。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	writeText(w, http.StatusOK, "ok")
}

// handleReadyz 就绪探针：200/503 + 原因（draining / not registered /
// no gateway stream），K8s/负载均衡按状态码摘挂。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	ready, reason := s.state.Ready()
	if ready {
		writeText(w, http.StatusOK, "ready: "+reason)
		return
	}
	writeText(w, http.StatusServiceUnavailable, "not ready: "+reason)
}

// handleCounters 计数快照（JSON）：字段名是对账脚本的消费契约，改动即破坏
// sgate /stats ↔ 本端 /counters 的对账口径。
func (s *Server) handleCounters(w http.ResponseWriter, r *http.Request) {
	if !requireGET(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(s.snapshot()); err != nil {
		// 头已写出，只能记日志（对账侧按解码失败处理）。
		tlog.Warn(s.ctx, "metrics encode failed error=%v", err)
	}
}

// countersPayload /counters 响应体。
type countersPayload struct {
	Service serviceCounters  `json:"service"`
	Push    map[string]int64 `json:"push"`
	Process processCounters  `json:"process"`
}

// serviceCounters 服务状态：registered/draining 来自注入的 State，
// 其余为 SDK 公开只读快照。
type serviceCounters struct {
	ServerID        string `json:"serverId"`
	Registered      bool   `json:"registered"`
	GatewayStreams  int    `json:"gatewayStreams"`
	Sessions        int    `json:"sessions"`
	Groups          int    `json:"groups"`
	PendingReliable int    `json:"pendingReliable"`
	Draining        bool   `json:"draining"`
}

// processCounters 进程运行时（heap* 单位字节，gcPauseNs 最近一次 GC 暂停）。
type processCounters struct {
	Goroutines int    `json:"goroutines"`
	HeapAlloc  uint64 `json:"heapAlloc"`
	HeapSys    uint64 `json:"heapSys"`
	HeapInuse  uint64 `json:"heapInuse"`
	NumGC      uint32 `json:"numGC"`
	GCPauseNs  uint64 `json:"gcPauseNs"`
}

// snapshot 组装一次计数快照。ReadMemStats 会短暂 STW，按需 scrape 可接受。
func (s *Server) snapshot() countersPayload {
	srv := s.svc.Server()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	var lastPause uint64
	if ms.NumGC > 0 {
		lastPause = ms.PauseNs[(ms.NumGC+255)%256]
	}
	return countersPayload{
		Service: serviceCounters{
			ServerID:        srv.GetServerID(),
			Registered:      s.svc.Registered(),
			GatewayStreams:  srv.ActiveStreams(),
			Sessions:        srv.GetConnectionCount(),
			Groups:          srv.GetGroupTotal(),
			PendingReliable: srv.PendingReliable(),
			Draining:        s.state.Draining(),
		},
		Push: srv.PushMetricsSnapshot(),
		Process: processCounters{
			Goroutines: runtime.NumGoroutine(),
			HeapAlloc:  ms.HeapAlloc,
			HeapSys:    ms.HeapSys,
			HeapInuse:  ms.HeapInuse,
			NumGC:      ms.NumGC,
			GCPauseNs:  lastPause,
		},
	}
}

// requireGET 只读端点仅接受 GET，其余 405。
func requireGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeText(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n")
}
