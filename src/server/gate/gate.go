// Package gate 网关服：客户端 TCP/WebSocket 接入，协议编解码，会话管理，RPC 转发。
package gate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"project/config/gen/server/common"
	"project/config/gen/server/gatesvr"
	"project/src/core/app"
	"project/src/core/codec"
	"project/src/core/config"
	"project/src/core/dispatcher"
	"project/src/core/errcode"
	"project/src/core/nodeid"
	"project/src/core/ragent/sdk"
	ragentwire "project/src/core/ragent/wire"
	corerpc "project/src/core/rpc"
	"project/src/core/session"
	genrpc "project/protocol/gen"
	"project/src/pkg/network"
)

const moduleName = "gate"

// Module 网关服模块。
type Module struct {
	app.DefaultModule
	ready          *app.Ready
	cfg            *gatesvr.GatesvrConfig
	commonCfg      *common.CommonConfig
	sessions       *session.SessionManager
	dispatch       *dispatcher.GateDispatcher
	remoteDispatch *corerpc.Dispatcher
	rpcCore        *corerpc.Core
	ragentClient   *sdk.Client
	server         *network.Server
	stopCh         chan struct{}
	stopOnce       sync.Once
}

// NewModule 创建网关模块。
func NewModule() *Module {
	return &Module{ready: app.NewReady(), stopCh: make(chan struct{})}
}

func (m *Module) Name() string { return moduleName }

// GateConfig 返回当前网关配置。
func GateConfig() *gatesvr.GatesvrConfig { return gatesvr.GatesvrConfigInstance() }

// CommonConfig 返回当前公共配置。
func CommonConfig() *common.CommonConfig { return common.CommonConfigInstance() }

// LoadConfigs 起服时加载所有配置。
func LoadConfigs() error {
	if err := common.LoadCommonConfig(); err != nil {
		return err
	}
	if err := gatesvr.LoadGatesvrConfig(); err != nil {
		return err
	}
	return nil
}

// ReloadConfigs 热更所有配置（通过 config.Manager）。
func ReloadConfigs() error {
	mgr := config.NewManager()
	mgr.Register(common.CommonConfigReloader)
	mgr.Register(gatesvr.GatesvrConfigReloader)
	return mgr.ReloadAll()
}

// Init 初始化：加载配置、创建 session、dispatcher、RPC、ragent client。
func (m *Module) Init() error {
	m.cfg = gatesvr.GatesvrConfigInstance()
	if m.cfg == nil {
		return fmt.Errorf("gatesvr config is nil")
	}
	m.commonCfg = common.CommonConfigInstance()

	nid, err := nodeid.Parse(m.App().NodeID())
	if err != nil {
		return fmt.Errorf("parse nodeid: %w", err)
	}

	m.sessions = session.NewSessionManager()
	m.remoteDispatch = corerpc.NewDispatcher()

	m.dispatch = dispatcher.NewGateDispatcher(1, m.sessions)
	m.dispatch.Use(dispatcher.RecoverMiddleware())
	m.dispatch.Use(dispatcher.AuthMiddleware(genrpc.AuthWhitelist))
	for cmdID, entry := range genrpc.RouteTable {
		m.dispatch.RegisterRoute(cmdID, dispatcher.RouteEntry{
			CmdID:      cmdID,
			ServerType: entry.ServerType,
			Route:      entry.Route,
			RspCmdID:   entry.RspCmdID,
		})
	}
	m.dispatch.SetHandshakeHandler(m.handleHandshake)
	m.dispatch.SetForward(m.forwardToBackend)

	m.ragentClient = sdk.NewClient(
		nid.Uint32(),
		m.cfg.RouteragentSockPath,
		m.App(),
		m.handleRagentFrame,
	)
	m.rpcCore = corerpc.New(m.ragentClient, corerpc.WithPoster(m.App()))
	m.ragentClient.SetCore(m.rpcCore)

	return nil
}

// AfterInit 连接 RouterAgent、启动 TCP acceptor。
func (m *Module) AfterInit() error {
	if err := m.ragentClient.Connect(); err != nil {
		m.ready.Fail(err)
		return err
	}

	srv, err := network.NewServer(
		network.WithNumEventLoop(0),
		network.WithMulticore(true),
	)
	if err != nil {
		m.ready.Fail(err)
		return err
	}
	m.server = srv

	srv.SetOnConnect(m.onConnect)
	srv.SetOnMessage(m.onMessage)
	srv.SetOnClose(m.onClose)

	if err := srv.AddAddress("tcp", m.cfg.ListenTcp); err != nil {
		m.ready.Fail(err)
		return err
	}

	go srv.Run()
	m.ready.Done()
	return nil
}

func (m *Module) WaitReady(ctx context.Context) error { return m.ready.WaitReady(ctx) }

func (m *Module) BeforeShutdown() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	if m.server != nil {
		m.server.Stop()
	}
	if m.rpcCore != nil {
		m.rpcCore.Close()
	}
	if m.ragentClient != nil {
		_ = m.ragentClient.Close()
	}
}

func (m *Module) Shutdown() {}

// ---- 网络事件 ----

func (m *Module) onConnect(conn *network.Conn) {
	m.sessions.OnConnect(&gateConn{conn: conn})
}

func (m *Module) onMessage(conn *network.Conn, in *network.Buffer) {
	for {
		view := in.Peek()
		if len(view) < 4 {
			return
		}
		bodyLen := int(uint32(view[1])<<16 | uint32(view[2])<<8 | uint32(view[3]))
		total := 4 + bodyLen
		if len(view) < total {
			return
		}
		data := make([]byte, total)
		copy(data, view[:total])
		in.Retrieve(total)

		pkt, err := codec.DecodePacket(data)
		if err != nil {
			continue
		}

		gconn := &gateConn{conn: conn}
		_ = m.dispatch.HandlePacket(gconn, &pkt)
	}
}

func (m *Module) onClose(conn *network.Conn) {
	m.sessions.OnDisconnect(&gateConn{conn: conn})
}

func (m *Module) handleHandshake(c dispatcher.Conn, body []byte) bool {
	// 握手时创建 session
	m.sessions.OnConnect(c)
	pkt, _ := codec.EncodePacket(codec.Packet{
		Type: codec.PacketHandshakeAck,
		Body: []byte{1},
	})
	if gc, ok := c.(*gateConn); ok {
		gc.conn.AsyncSend(pkt)
	}
	return true
}

// ---- 转发到后端 ----

func (m *Module) forwardToBackend(sess *session.Session, msg *codec.Message, entry dispatcher.RouteEntry) error {
	if sess == nil || msg == nil {
		return errcode.New(errcode.ERR_UNMARSHAL, "nil session or message")
	}
	target := corerpc.Target{ServerType: entry.ServerType}.ByHash(sess.ConnID)
	if nodeID := sess.BoundNodes[entry.ServerType]; nodeID != 0 {
		target = target.At(nodeID)
	}
	nid, _ := nodeid.Parse(m.App().NodeID())
	ctx := corerpc.Background().WithFromNode(nid.Uint32())
	sessID := sess.ID
	sessConnID := sess.ConnID

	switch msg.Type {
	case codec.MessageRequest:
		m.rpcCore.Call(target, entry.Route, msg.Body, ctx, func(payload []byte, code errcode.ErrCode) {
			current := m.sessions.GetByConnID(sessConnID)
			if current == nil || current.ID != sessID || current.Conn == nil {
				return
			}
			pkt, encErr := codec.EncodeDataMessagePacket(codec.Message{
				Type:    codec.MessageResponse,
				SeqID:   msg.SeqID,
				CmdID:   entry.RspCmdID,
				ErrCode: code,
				Body:    payload,
			})
			if encErr != nil {
				return
			}
			if gc, ok := current.Conn.(*gateConn); ok {
				gc.conn.AsyncSend(pkt)
			}
		})
	case codec.MessageNotify:
		m.rpcCore.Send(target, entry.Route, msg.Body, ctx)
	}
	return nil
}

// ---- ragent 帧处理 ----

func (m *Module) handleRagentFrame(frame ragentwire.Frame) {
	switch frame.Type {
	case ragentwire.FrameRpcRequest, ragentwire.FrameRpcNotify:
		m.handleRemote(frame)
	}
}

func (m *Module) handleRemote(frame ragentwire.Frame) {
	head, err := ragentwire.DecodeRPCWireHeader(frame.Header)
	if err != nil {
		return
	}
	ctx := corerpc.Background().
		WithFromNode(head.SrcNodeID).
		WithDeadline(time.Duration(head.DeadlineMs) * time.Millisecond)
	code := errcode.CodeOf(m.remoteDispatch.Dispatch(head.Route, ctx, frame.Body, nil))
	if frame.Type == ragentwire.FrameRpcRequest && head.SeqID != 0 {
		rspHead := head
		nid, _ := nodeid.Parse(m.App().NodeID())
		if rspHead.DestNodeID != 0 {
			rspHead.SrcNodeID = rspHead.DestNodeID
		} else if nid.Uint32() != 0 {
			rspHead.SrcNodeID = nid.Uint32()
		}
		rspHead.DestNodeID = head.SrcNodeID
		_, st, _ := nodeid.Decode(rspHead.DestNodeID)
		rspHead.ServerType = st
		rspHead.ErrCode = uint32(code)
		_ = m.ragentClient.SendRPCFrame(ragentwire.FrameRpcResponse, rspHead, nil)
	}
}

// ---- gateConn 适配 dispatcher.Conn + session.Connection ----

type gateConn struct {
	conn *network.Conn
}

func (c *gateConn) RemoteAddr() string { return c.conn.PeerAddr().String() }
func (c *gateConn) TouchRecv()         {}
