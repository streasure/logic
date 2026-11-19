package handler

import (
	"context"
	"testing"

	"github.com/streasure/logic/internal/config"

	logic "github.com/streasure/logic"
	protocol "github.com/streasure/protocol/gateway"
	logicpb "github.com/streasure/protocol/logic"
	"google.golang.org/protobuf/proto"
)

// newTestHandler 构造未 Start 的服务 + handler（无网络、无注册，纯内存）。
func newTestHandler(t *testing.T) (*Handlers, *logic.Service) {
	t.Helper()
	svc := logic.NewService(logic.WithServiceID("handler-test"))
	return New(svc, &config.Config{ServerID: "handler-test"}), svc
}

// loginMsg 组装一条 LoginGate 上游消息（与网关转发形态一致）。
func loginMsg(sessionID string) *protocol.StreamData {
	body, err := proto.Marshal(&protocol.LoginGateReq{ServerId: "handler-test", UserId: "u1"})
	if err != nil {
		panic(err)
	}
	return &protocol.StreamData{Cmd: protocol.CmdLoginGate, SessionId: sessionID, UserKey: "u1", Data: body}
}

// logicLoginMsg 组装一条 CmdLogicLoginReq 上游消息（与网关转发形态一致：
// UserKey 是网关注入的 fullUUID `{serverId}:{userId}`）。
func logicLoginMsg(sessionID, userUUID string) *protocol.StreamData {
	body, err := proto.Marshal(&logicpb.LoginReq{UserId: "u1", LoginKey: "k1", IsReconnect: true})
	if err != nil {
		panic(err)
	}
	return &protocol.StreamData{Cmd: protocol.CmdLogicLoginReq, SessionId: sessionID, UserKey: userUUID, Data: body}
}

// dispatch 经 SDK 公开分发入口同步执行（respCmd=0 → 无应答）。
func dispatch(t *testing.T, svc *logic.Service, msg *protocol.StreamData) {
	t.Helper()
	if _, err := svc.Server().SendMessage(context.Background(), msg); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// TestLoginGateJoinGroup 正常路径：入组、计数归零、未丢弃。
func TestLoginGateJoinGroup(t *testing.T) {
	h, svc := newTestHandler(t)
	dispatch(t, svc, loginMsg("sess-1"))

	if got := svc.Server().GetGroupCount(defaultGroup); got != 1 {
		t.Fatalf("group members = %d, want 1", got)
	}
	if got := h.InFlight(); got != 0 {
		t.Errorf("InFlight() = %d, want 0", got)
	}
	if got := h.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
	if h.Draining() {
		t.Error("Draining() = true before BeginDrain")
	}
}

// TestDrainDropsNewRequests drain 后到达的请求：不入组、dropped++、在途归零。
func TestDrainDropsNewRequests(t *testing.T) {
	h, svc := newTestHandler(t)
	h.BeginDrain()

	dispatch(t, svc, loginMsg("sess-2"))

	if got := svc.Server().GetGroupCount(defaultGroup); got != 0 {
		t.Fatalf("draining 期间入组成员 = %d, want 0", got)
	}
	if got := h.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1", got)
	}
	if got := h.InFlight(); got != 0 {
		t.Errorf("InFlight() = %d, want 0", got)
	}
}

// TestLogicLoginAckKeepsGatewayUserKey 登录应答的 user_key 必须回显网关注入的
// fullUUID（`{serverId}:{userId}`），不能回裸 userId。
// 依据：SDK 应答分发见应答 user_key 非空即覆盖响应 StreamData.UserKey，
// 网关收到后执行 UpdateConnectionUserUUID 改绑连接身份——回裸键会让
// 按 fullUUID 查重的重复登录踢旧连接失效（login.go GetUserConnection），
// 且跨逻辑分片同 userId 的会话索引互撞。
func TestLogicLoginAckKeepsGatewayUserKey(t *testing.T) {
	_, svc := newTestHandler(t)
	const fullUUID = "handler-test:u1"

	resp, err := svc.Server().SendMessage(context.Background(), logicLoginMsg("sess-ll-1", fullUUID))
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp == nil {
		t.Fatal("resp = nil, want LoginAck 应答")
	}
	if resp.Cmd != protocol.CmdLogicLoginAck {
		t.Errorf("resp.Cmd = %d, want %d", resp.Cmd, protocol.CmdLogicLoginAck)
	}
	if resp.UserKey != fullUUID {
		t.Errorf("应答 UserKey = %q, want fullUUID %q（裸 userId 会触发网关身份改绑）", resp.UserKey, fullUUID)
	}
	ack := new(logicpb.LoginAck)
	if err := proto.Unmarshal(resp.Data, ack); err != nil {
		t.Fatalf("unmarshal LoginAck: %v", err)
	}
	if ack.UserKey != fullUUID {
		t.Errorf("LoginAck.user_key = %q, want %q", ack.UserKey, fullUUID)
	}
	if ack.ServerTime == 0 {
		t.Error("LoginAck.serverTime = 0, want 非 0")
	}
}

// TestWrapInFlightAccounting wrap 的在途计数：handler 执行期间为 1，返回后归零。
func TestWrapInFlightAccounting(t *testing.T) {
	h, _ := newTestHandler(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	wrapped := h.wrap(func(ctx *logic.Context, req proto.Message) proto.Message {
		close(entered)
		<-release
		return nil
	})

	done := make(chan struct{})
	go func() {
		wrapped(&logic.Context{}, &protocol.LoginGateReq{})
		close(done)
	}()
	<-entered
	if got := h.InFlight(); got != 1 {
		t.Fatalf("InFlight() = %d during handler, want 1", got)
	}
	close(release)
	<-done
	if got := h.InFlight(); got != 0 {
		t.Fatalf("InFlight() = %d after handler, want 0", got)
	}
}

// TestDrainKeepsInFlight 时序关键路径：请求已过 drain 门后才 BeginDrain，
// 业务照常完成（drain 只拒新、不杀在途），且在途归零发生在业务结束后。
func TestDrainKeepsInFlight(t *testing.T) {
	h, _ := newTestHandler(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	ran := make(chan struct{})
	wrapped := h.wrap(func(ctx *logic.Context, req proto.Message) proto.Message {
		close(entered)
		<-release
		close(ran)
		return nil
	})

	done := make(chan struct{})
	go func() {
		wrapped(&logic.Context{}, &protocol.LoginGateReq{})
		close(done)
	}()
	<-entered
	h.BeginDrain() // 在途请求已过门：不应被拒绝
	close(release)
	<-done

	select {
	case <-ran:
	default:
		t.Fatal("BeginDrain 中断了在途业务（drain 只应拒新）")
	}
	if got := h.InFlight(); got != 0 {
		t.Errorf("InFlight() = %d, want 0", got)
	}
	if got := h.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0（在途请求不计 dropped）", got)
	}
}
