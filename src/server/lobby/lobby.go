// Package lobby 大厅服：处理客户端通过 RouterAgent 转发的业务请求。
package lobby

import (
	"context"
	"fmt"
	"time"

	"project/src/pkg/logger"

	"project/config/gen/server/common"
	"project/config/gen/server/lobbysvr"
	"project/src/core/app"
	"project/src/core/config"
	"project/src/core/errcode"
	"project/src/core/nodeid"
	"project/src/core/ragent/sdk"
	ragentwire "project/src/core/ragent/wire"
	corerpc "project/src/core/rpc"
)

const moduleName = "lobby"

// Module 大厅服模块。
type Module struct {
	app.DefaultModule
	ready    *app.Ready
	cfg      *lobbysvr.LobbysvrConfig
	client   *sdk.Client
	rpcCore  *corerpc.Core
	dispatch *corerpc.Dispatcher
}

// NewModule 创建大厅模块。
func NewModule() *Module {
	return &Module{ready: app.NewReady()}
}

func (m *Module) Name() string { return moduleName }

func (m *Module) Init() error {
	m.cfg = lobbysvr.LobbysvrConfigInstance()
	if m.cfg == nil {
		return fmt.Errorf("lobbysvr config is nil")
	}

	nid, err := nodeid.Parse(m.App().NodeID())
	if err != nil {
		return fmt.Errorf("parse nodeid: %w", err)
	}

	m.dispatch = corerpc.NewDispatcher()

	// 注册 Ping handler（测试用）
	m.dispatch.MustRegister("LobbyHandler/Ping",
		corerpc.RecoverRoute("LobbyHandler/Ping",
			func(ctx corerpc.Ctx, body []byte, reply func([]byte, error)) error {
				logger.Info("[lobby] Ping received", logger.String("body", string(body)))
				reply([]byte("pong"), nil)
				logger.Info("[lobby] Ping reply sent")
				return nil
			}))

	m.client = sdk.NewClient(
		nid.Uint32(),
		m.cfg.RouteragentSockPath,
		m.App(),
		m.handleRagentFrame,
	)
	m.rpcCore = corerpc.New(m.client, corerpc.WithPoster(m.App()))
	m.client.SetCore(m.rpcCore)

	return nil
}

func (m *Module) AfterInit() error {
	if err := m.client.Connect(); err != nil {
		m.ready.Fail(err)
		return err
	}
	m.ready.Done()
	return nil
}

func (m *Module) WaitReady(ctx context.Context) error { return m.ready.WaitReady(ctx) }

func (m *Module) BeforeShutdown() {
	if m.rpcCore != nil {
		m.rpcCore.Close()
	}
	if m.client != nil {
		_ = m.client.Close()
	}
}

func (m *Module) Shutdown() {}

func (m *Module) handleRagentFrame(frame ragentwire.Frame) {
	switch frame.Type {
	case ragentwire.FrameRpcRequest:
		head, err := ragentwire.DecodeRPCWireHeader(frame.Header)
		logger.Info("[lobby] RpcRequest", logger.String("route", head.Route))
		if err != nil {
			return
		}
		ctx := corerpc.Background().
			WithFromNode(head.SrcNodeID).
			WithDeadline(time.Duration(head.DeadlineMs) * time.Millisecond)

		nid, _ := nodeid.Parse(m.App().NodeID())
		selfNode := nid.Uint32()

		// Reply 回调：将 handler 返回结果编码为 RPC Response 帧发回
		err = m.dispatch.Dispatch(head.Route, ctx, frame.Body, func(payload []byte, err error) {
			rspHead := head
			rspHead.SrcNodeID = selfNode
			rspHead.DestNodeID = head.SrcNodeID
			rspHead.ErrCode = uint32(errcode.CodeOf(err))
			_, st, _ := nodeid.Decode(rspHead.DestNodeID)
			rspHead.ServerType = st
			_ = m.client.SendRPCFrame(ragentwire.FrameRpcResponse, rspHead, payload)
		})
		if err != nil && head.SeqID != 0 {
			// Dispatch 本身失败（如路由未找到），发送错误响应
			rspHead := head
			rspHead.SrcNodeID = selfNode
			rspHead.DestNodeID = head.SrcNodeID
			rspHead.ErrCode = uint32(errcode.CodeOf(err))
			_, st, _ := nodeid.Decode(rspHead.DestNodeID)
			rspHead.ServerType = st
			_ = m.client.SendRPCFrame(ragentwire.FrameRpcResponse, rspHead, nil)
		}

	case ragentwire.FrameRpcNotify:
		head, err := ragentwire.DecodeRPCWireHeader(frame.Header)
		if err != nil {
			return
		}
		ctx := corerpc.Background().
			WithFromNode(head.SrcNodeID).
			WithDeadline(time.Duration(head.DeadlineMs) * time.Millisecond)
		_ = m.dispatch.Dispatch(head.Route, ctx, frame.Body, nil)
	}
}

// ---- 配置 ----

func LobbyConfig() *lobbysvr.LobbysvrConfig { return lobbysvr.LobbysvrConfigInstance() }

func LoadConfigs() error {
	if err := common.LoadCommonConfig(); err != nil {
		return err
	}
	if err := lobbysvr.LoadLobbysvrConfig(); err != nil {
		return err
	}
	return nil
}

func ReloadConfigs() error {
	mgr := config.NewManager()
	mgr.Register(common.CommonConfigReloader)
	mgr.Register(lobbysvr.LobbysvrConfigReloader)
	return mgr.ReloadAll()
}
