// Package handler logicserver 业务命令注册与业务在途（drain）状态。
//
// 边界（设计案 §3.2）：业务 handler 只经 SDK 公开 API（Service.RegisterProto /
// Service.Server()）挂接，不 import SDK 内部实现、不触碰 SDK 包状态；
// handler 与业务状态一律留在本包，永不进 SDK 根包。
package handler

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/streasure/logic/internal/config"

	logic "github.com/streasure/logic"
	"github.com/streasure/protocol/enums"
	protocol "github.com/streasure/protocol/gateway"
	logicpb "github.com/streasure/protocol/logic"
	"github.com/streasure/util/tlog"
	"google.golang.org/protobuf/proto"
)

// defaultGroup 登录用户进入的默认分组（阶段 2 起随业务配置扩展）。
const defaultGroup = "logicserver"

// Handlers 业务命令注册结果 + 优雅 drain 的在途状态持有者。
// 组合根持有它：注册（New）→ 观测（probe 注入 Draining）→ 停机（BeginDrain 等 InFlight 归零）。
type Handlers struct {
	svc *logic.Service
	cfg *config.Config

	draining atomic.Bool  // 置位后到达的业务请求直接拒绝（dropped++）
	inFlight atomic.Int64 // 在途业务请求数（drain 等待判据）
	dropped  atomic.Int64 // drain 期间被拒绝的请求数
}

// New 注册业务命令处理器并返回状态持有者（组合根在 NewService 之后调用一次）。
func New(svc *logic.Service, cfg *config.Config) *Handlers {
	h := &Handlers{svc: svc, cfg: cfg}
	// respCmd=0：LoginGateAck 由网关应答，逻辑服不回包（端到端不重复应答）。
	h.svc.RegisterProto(protocol.CmdLoginGate, &protocol.LoginGateReq{}, 0, h.wrap(h.loginGate()))
	h.svc.RegisterProto(protocol.CmdLogicLoginReq, &logicpb.LoginReq{}, protocol.CmdLogicLoginAck, h.wrap(h.logicLogin()))
	return h
}

// wrap 给业务 handler 套统一的 drain 门。顺序是关键：先登记在途、再判
// draining——BeginDrain 后到达的请求必然看到标志（先判后增会漏：标志
// 置位与请求进入可交错，drain 判空与新业务进入互不感知）；而 BeginDrain
// 之前已过门的请求持有 inFlight 直到业务完成，drain 等待判据不会提前归零。
func (h *Handlers) wrap(fn logic.ProtoHandler) logic.ProtoHandler {
	return func(ctx *logic.Context, req proto.Message) proto.Message {
		h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		if h.draining.Load() {
			h.dropped.Add(1)
			return nil
		}
		return fn(ctx, req)
	}
}

// BeginDrain 开始优雅停机第一步：拒绝新业务（readiness 随即失败，
// 摘流发生在停流之前）。
func (h *Handlers) BeginDrain() { h.draining.Store(true) }

// Draining 是否已开始 drain（probe / /counters 消费）。
func (h *Handlers) Draining() bool { return h.draining.Load() }

// InFlight 当前在途业务请求数（drain 等待归零判据）。
func (h *Handlers) InFlight() int64 { return h.inFlight.Load() }

// Dropped drain 期间被拒绝的请求数（停机对账：正常停机窗口外不应增长）。
func (h *Handlers) Dropped() int64 { return h.dropped.Load() }

// loginGate 登录转发：网关完成网关侧登录后把 LoginGate 转给逻辑服，入组并绑定用户。
func (h *Handlers) loginGate() logic.ProtoHandler {
	return func(ctx *logic.Context, req proto.Message) proto.Message {
		login := req.(*protocol.LoginGateReq)
		h.svc.Server().JoinGroup(defaultGroup, ctx.ConnectionID)
		if ctx.UserUUID != "" {
			if err := h.svc.Server().JoinGroupForUser(ctx.UserUUID, defaultGroup); err != nil {
				tlog.Warn(context.TODO(), "join group for user failed user=%s error=%v", ctx.UserUUID, err)
			}
		}
		// 欢迎推送（骨架示例）：协议既有下行 CmdPushNotify，用于验证登录后
		// 真实下行链路并支撑对账（本端 /counters push.totalPushed ↔
		// 网关 /stats pushedToClient）。网关 finishLoginGate 先回 LoginGateAck
		// 再转发本命令，推送不会插到 ack 前面。
		h.pushWelcome(ctx.ConnectionID)
		tlog.Info(context.TODO(), "login gate bound sessionID=%s user=%s target=%s serverId=%s members=%d",
			ctx.ConnectionID, ctx.UserUUID, login.GetServerId(), h.cfg.ServerID,
			h.svc.Server().GetGroupCount(defaultGroup))
		return nil
	}
}

// pushWelcome 登录欢迎推送：尽力而为，失败仅告警不影响登录链路
// （会话刚由上游消息绑定，失败只说明链路异常，由 /counters totalFailed 暴露）。
func (h *Handlers) pushWelcome(sessionID string) {
	body, err := proto.Marshal(&logicpb.PushNotify{
		Title:    "welcome",
		Content:  "login ok",
		PushType: enums.PushType_PUSH_TYPE_NOTIFY,
	})
	if err != nil {
		tlog.Warn(context.TODO(), "marshal push notify failed error=%v", err)
		return
	}
	// 命令码取协议权威枚举（enums.Cmd_CMD_PUSH_NOTIFY），不自造数值。
	if err := h.svc.Server().PushToConnection(sessionID, int32(enums.Cmd_CMD_PUSH_NOTIFY), body); err != nil {
		tlog.Warn(context.TODO(), "push welcome failed sessionID=%s error=%v", sessionID, err)
	}
}

// logicLogin 逻辑登录（骨架应答）：验证 SDK 应答分发链路（respCmd → 回调下发）。
// TODO(阶段 2+)：真实登录业务（校验/建号/快照）在此展开，届时替换本桩。
func (h *Handlers) logicLogin() logic.ProtoHandler {
	return func(ctx *logic.Context, req proto.Message) proto.Message {
		loginReq := req.(*logicpb.LoginReq)
		return &logicpb.LoginAck{
			ServerTime: time.Now().UnixMilli(),
			UserKey:    loginReq.GetUserId(),
		}
	}
}
