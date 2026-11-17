// Package handler logicserver 业务命令注册。
//
// 边界（设计案 §3.2）：业务 handler 只经 SDK 公开 API（Service.RegisterProto /
// Service.Server()）挂接，不 import SDK 内部实现、不触碰 SDK 包状态；
// handler 与业务状态一律留在本包，永不进 SDK 根包。
package handler

import (
	"context"
	"time"

	"github.com/streasure/logic/internal/config"

	logic "github.com/streasure/logic"
	protocol "github.com/streasure/protocol/gateway"
	logicpb "github.com/streasure/protocol/logic"
	"github.com/streasure/util/tlog"
	"google.golang.org/protobuf/proto"
)

// defaultGroup 登录用户进入的默认分组（阶段 2 起随业务配置扩展）。
const defaultGroup = "logicserver"

// Register 挂接业务命令处理器（模式照 sgate bench/logic2_ws）。
// 由组合根 cmd/logicserver 在 NewService 之后、Run 之前调用一次。
func Register(svc *logic.Service, cfg *config.Config) {
	// 登录转发：网关完成网关侧登录后把 LoginGate 转给逻辑服，入组并绑定用户。
	// respCmd=0：LoginGateAck 由网关应答，逻辑服不回包（端到端不重复应答）。
	svc.RegisterProto(protocol.CmdLoginGate, &protocol.LoginGateReq{}, 0,
		func(ctx *logic.Context, req proto.Message) proto.Message {
			login := req.(*protocol.LoginGateReq)
			svc.Server().JoinGroup(defaultGroup, ctx.ConnectionID)
			if ctx.UserUUID != "" {
				if err := svc.Server().JoinGroupForUser(ctx.UserUUID, defaultGroup); err != nil {
					tlog.Warn(context.TODO(), "join group for user failed user=%s error=%v", ctx.UserUUID, err)
				}
			}
			tlog.Info(context.TODO(), "login gate bound sessionID=%s user=%s target=%s serverId=%s members=%d",
				ctx.ConnectionID, ctx.UserUUID, login.GetServerId(), cfg.ServerID,
				svc.Server().GetGroupCount(defaultGroup))
			return nil
		})

	// 逻辑登录（骨架应答）：验证 SDK 应答分发链路（respCmd → 回调下发）。
	// TODO(阶段 2+)：真实登录业务（校验/建号/快照）在此展开，届时替换本桩。
	svc.RegisterProto(protocol.CmdLogicLoginReq, &logicpb.LoginReq{}, protocol.CmdLogicLoginAck,
		func(ctx *logic.Context, req proto.Message) proto.Message {
			loginReq := req.(*logicpb.LoginReq)
			return &logicpb.LoginAck{
				ServerTime: time.Now().UnixMilli(),
				UserKey:    loginReq.GetUserId(),
			}
		})
}
