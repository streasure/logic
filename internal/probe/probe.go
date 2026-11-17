// Package probe readiness 判定：组合根注入三个只读观测 func（etcd 注册、
// flip 流数、drain 状态），判定逻辑与 SDK 解耦、可单测。
//
// 判定顺序即停机语义：draining 一律未就绪（先摘流再停，避免 drain 期间
// 网关仍把新会话路由进来），其次注册完成，最后至少 1 条网关 flip 流。
package probe

// Checker readiness 判定器。三个 func 必须非 nil（组合根 cmd/logicserver 注入）。
type Checker struct {
	Registered func() bool // etcd 注册是否已完成
	Streams    func() int  // 当前活跃 flip 流数
	IsDraining func() bool // 是否已开始优雅 drain
}

// Ready 返回就绪判定与原因（/readyz 消费）。
func (c *Checker) Ready() (bool, string) {
	if c.IsDraining() {
		return false, "draining"
	}
	if !c.Registered() {
		return false, "not registered"
	}
	if c.Streams() < 1 {
		return false, "no gateway stream"
	}
	return true, "ok"
}

// Draining 暴露 drain 状态给 /counters（与 Ready 共用同一注入源，
// 由 metrics.State 接口消费）。
func (c *Checker) Draining() bool { return c.IsDraining() }
