// Package agent RouterAgent 服务端运行时。
//
// 负责：
//   - UDS 监听：接受本地业务进程（gate/lobby/match 等）连接
//   - 握手：校验 SDK 客户端身份
//   - 帧路由：将 RPC 帧转发到目标连接
//   - TCP peer：跨机器 RA 互联（待补）
//   - 服务发现：etcd 注册/发现（待补）
package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	"project/src/core/nodeid"
	"project/src/core/ragent/wire"
	"project/src/pkg/logger"
	"project/src/pkg/network"
)

const defaultSockPath = "/run/routeragent/ra.sock"

// Runtime RouterAgent 服务端运行时。
type Runtime struct {
	ready   *Ready
	nodeID  uint32
	sockPath  string

	udsServer *network.Server
	mu        sync.Mutex
	conns     map[uint32]*network.Conn // nodeID → UDS conn
	connsByFD map[int]*network.Conn   // fd → UDS conn
}

// NewRuntime 创建运行时。
func NewRuntime() *Runtime {
	return &Runtime{
		ready:   NewReady(),
		conns:   make(map[uint32]*network.Conn),
		connsByFD: make(map[int]*network.Conn),
	}
}

// SetNodeID 设置本节点 ID。
func (rt *Runtime) SetNodeID(nodeID uint32) {
	rt.nodeID = nodeID
}

// ApplyConfig 应用配置。
func (rt *Runtime) ApplyConfig(sockPath string) {
	if sockPath == "" {
		sockPath = defaultSockPath
	}
	rt.sockPath = sockPath
}

// Init 初始化。
func (rt *Runtime) Init() error {
	logger.Info("routeragent init", logger.String("sock_path", rt.sockPath))
	if rt.sockPath == "" {
		return fmt.Errorf("routeragent sock_path is required")
	}
	return nil
}

// AfterInit 启动 UDS 监听。
func (rt *Runtime) AfterInit() error {
	srv, err := network.NewServer(
		network.WithNumEventLoop(1),
	)
	if err != nil {
		rt.ready.Fail(err)
		return fmt.Errorf("routeragent uds server: %w", err)
	}
	rt.udsServer = srv

	srv.SetOnConnect(rt.onConnect)
	srv.SetOnMessage(rt.onMessage)
	srv.SetOnClose(rt.onClose)

	if err := srv.AddAddress("unix", rt.sockPath); err != nil {
		rt.ready.Fail(err)
		return fmt.Errorf("routeragent add unix addr: %w", err)
	}

	go srv.Run()
	rt.ready.Done()
	return nil
}

// WaitReady 等待就绪。
func (rt *Runtime) WaitReady(ctx context.Context) error {
	return rt.ready.WaitReady(ctx)
}

// BeforeShutdown 停止接入。
func (rt *Runtime) BeforeShutdown() {
	if rt.udsServer != nil {
		rt.udsServer.Stop()
	}
}

// Shutdown 清理。
func (rt *Runtime) Shutdown() {}

// ---- UDS 连接处理 ----

func (rt *Runtime) onConnect(conn *network.Conn) {
	rt.mu.Lock()
	rt.connsByFD[conn.Fd()] = conn
	rt.mu.Unlock()
	logger.Info("routeragent uds connect", logger.String("peer", conn.PeerAddr().String()))
}

func (rt *Runtime) onClose(conn *network.Conn) {
	rt.mu.Lock()
	delete(rt.connsByFD, conn.Fd())
	for nid, c := range rt.conns {
		if c == conn {
			delete(rt.conns, nid)
			break
		}
	}
	rt.mu.Unlock()
	logger.Info("routeragent uds disconnect", logger.Int("fd", conn.Fd()))
}

func (rt *Runtime) onMessage(conn *network.Conn, in *network.Buffer) {
	// 帧解析：4B length → 1B type → 2B headerLen → header → body
	for {
		view := in.Peek()
		if len(view) < 4 {
			return
		}
		length := int(binary.BigEndian.Uint32(view[:4]))
		if length < 3 || len(view) < 4+length {
			return
		}

		frame, err := wire.DecodeFrame(view[:4+length])
		if err != nil {
			logger.Warn("routeragent decode frame", logger.Err(err))
			in.RetrieveAll()
			return
		}
		in.Retrieve(4 + length)

		rt.dispatchFrame(conn, frame)
	}
}

func (rt *Runtime) dispatchFrame(conn *network.Conn, frame wire.Frame) {
	switch frame.Type {
	case wire.FrameHandshake:
		rt.handleHandshake(conn, frame)
	case wire.FrameRpcRequest, wire.FrameRpcResponse, wire.FrameRpcNotify:
		rt.routeFrame(conn, frame)
	case wire.FrameHeartbeat:
		rt.sendFrame(conn, wire.Frame{Type: wire.FrameHeartbeat})
	default:
		logger.Warn("routeragent unknown frame type", logger.Int("type", int(frame.Type)))
	}
}

// ---- 握手 ----

func (rt *Runtime) handleHandshake(conn *network.Conn, frame wire.Frame) {
	if len(frame.Body) < 4 {
		logger.Warn("routeragent handshake body too short")
		return
	}
	nodeID := binary.BigEndian.Uint32(frame.Body[:4])

	rt.mu.Lock()
	rt.conns[nodeID] = conn
	rt.mu.Unlock()

	ack := wire.Frame{Type: wire.FrameHandshakeAck, Body: []byte{1}}
	rt.sendFrame(conn, ack)
	logger.Info("routeragent handshake ok", logger.Uint32("node_id", nodeID), logger.String("peer", conn.PeerAddr().String()))
}

// ---- 路由 ----

func (rt *Runtime) routeFrame(_ *network.Conn, frame wire.Frame) {
	head, err := wire.DecodeRPCWireHeader(frame.Header)
	if err != nil {
		logger.Warn("routeragent decode rpc header", logger.Err(err))
		return
	}

	targetID := head.DestNodeID
	if targetID == 0 {
		// Try to find a local target of the given ServerType
		rt.mu.Lock()
		for nid := range rt.conns {
			_, st, _ := nodeid.Decode(nid)
			if uint32(st) == head.ServerType {
				targetID = nid
				break
			}
		}
		rt.mu.Unlock()
	}

	if targetID == 0 {
		logger.Warn("routeragent no route",
			logger.String("route", head.Route),
			logger.Uint32("server_type", head.ServerType))
		return
	}

	rt.mu.Lock()
	targetConn := rt.conns[targetID]
	rt.mu.Unlock()

	if targetConn != nil {
		rt.sendFrame(targetConn, frame)
	} else {
		// Future: forward to remote peer via TCP
		logger.Warn("routeragent no local conn for target",
			logger.Uint32("target_id", targetID),
			logger.String("route", head.Route))
	}
}

func (rt *Runtime) sendFrame(conn *network.Conn, frame wire.Frame) {
	data, err := wire.EncodeFrame(frame)
	if err != nil {
		logger.Warn("routeragent encode frame", logger.Err(err))
		return
	}
	conn.AsyncSend(data)
}

// ---- helpers ----

// Ready 一次性就绪原语。
type Ready struct {
	once sync.Once
	ch   chan struct{}
	err  error
}

func NewReady() *Ready { return &Ready{ch: make(chan struct{})} }

func (r *Ready) Done() { r.once.Do(func() { close(r.ch) }) }

func (r *Ready) Fail(err error) {
	r.once.Do(func() {
		r.err = err
		close(r.ch)
	})
}

func (r *Ready) WaitReady(ctx context.Context) error {
	select {
	case <-r.ch:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
