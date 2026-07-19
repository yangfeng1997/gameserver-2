package sdk

import (
	"encoding/binary"
	"fmt"
	"sync"

	"project/src/core/errcode"
	"project/src/core/ragent/wire"
	corerpc "project/src/core/rpc"
	"project/src/pkg/network"
)

// Client 通过本机 UDS 连接 RouterAgent 的业务侧客户端。
// 实现 rpc.Transport 接口，用于 RPC 引擎发送帧。
type Client struct {
	nodeID uint32
	sock   string
	poster corerpc.Poster
	core   *corerpc.Core

	cli    *network.Client
	conn   *network.Conn
	onRecv func(wire.Frame)

	handshakeCh chan error

	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

// NewClient 构造客户端。
//   - nodeID: 本进程节点 ID（来自 nodeid.Encode）
//   - sock: RouterAgent UDS 路径
//   - poster: 主循环投递器，用于将 onRecv 帧投递到主 goroutine
//   - onRecv: 收到 RPC 请求/通知时的回调（在 poster 所在 goroutine 中调用）
func NewClient(nodeID uint32, sock string, poster corerpc.Poster, onRecv func(wire.Frame)) *Client {
	return &Client{
		nodeID:      nodeID,
		sock:        sock,
		poster:      poster,
		onRecv:      onRecv,
		handshakeCh: make(chan error, 1),
		done:        make(chan struct{}),
	}
}

// SetCore 注入 RPC 引擎，用于 OnResponse/OnResponseWithRelease。
func (c *Client) SetCore(core *corerpc.Core) { c.core = core }

// Connect 拨号 UDS 并完成握手。
func (c *Client) Connect() error {
	if c.nodeID == 0 {
		return fmt.Errorf("ragent node_id is empty")
	}
	if c.sock == "" {
		return fmt.Errorf("routeragent sock path is empty")
	}

	// 用自研网络库拨号 UDS，单 loop
	c.cli = network.NewClient(network.WithNumEventLoop(1))
	c.cli.SetOnMessage(c.onMessage)

	conn, err := c.cli.Dial("unix", c.sock)
	if err != nil {
		return fmt.Errorf("dial routeragent %s: %w", c.sock, err)
	}
	c.conn = conn

	// 发送握手
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, c.nodeID)
	conn.AsyncSend(mustEncodeFrame(wire.FrameHandshake, body))

	// 等待握手响应
	if err := <-c.handshakeCh; err != nil {
		c.conn.Close()
		return err
	}
	return nil
}

// Close 关闭连接。
func (c *Client) Close() error {
	c.once.Do(func() {
		close(c.done)
		if c.conn != nil {
			c.conn.Close()
		}
		if c.cli != nil {
			c.cli.Stop()
		}
	})
	return nil
}

// SendFrame 实现 rpc.Transport，编码 RPC 头部并通过 UDS 发送。
func (c *Client) SendFrame(target corerpc.Target, header corerpc.Header, body []byte) error {
	rpcHead := wire.RPCWireHeader{
		SeqID:       header.SeqID,
		ServerType:  header.ServerType,
		RoutingMode: routingModeToWire(target.Mode),
		DeadlineMs:  header.DeadlineMs,
		WaiterID:    header.WaiterID,
		SrcNodeID:   header.SrcNodeID,
		DestNodeID:  header.DestNodeID,
		RoutingKey:  header.RoutingKey,
		Route:       header.Route,
	}
	if rpcHead.ServerType == 0 {
		rpcHead.ServerType = target.ServerType
	}
	if rpcHead.SrcNodeID == 0 {
		rpcHead.SrcNodeID = c.nodeID
	}
	if target.Mode == corerpc.RoutingDirect {
		rpcHead.DestNodeID = target.NodeID
		rpcHead.RoutingKey = fmt.Sprintf("%d", target.NodeID)
		if target.NodeID != 0 {
			st, err := corerpc.NormalizeRouteTarget(target.NodeID, rpcHead.ServerType)
			if err != nil {
				return err
			}
			rpcHead.ServerType = st
		}
	}

	frameType := wire.FrameRpcNotify
	if header.SeqID != 0 {
		frameType = wire.FrameRpcRequest
	}
	return c.SendRPCFrame(frameType, rpcHead, body)
}

// SendRPCFrame 发送带有显式 RPC 头部的帧。
func (c *Client) SendRPCFrame(frameType wire.FrameType, rpcHead wire.RPCWireHeader, body []byte) error {
	return c.sendFrame(frameType, rpcHead, body)
}

// Send 发送通用帧（RPC 头部由调用方自行编码到 Header 字段）。
func (c *Client) Send(frame wire.Frame) error {
	if frame.Type == wire.FrameRpcRequest || frame.Type == wire.FrameRpcResponse || frame.Type == wire.FrameRpcNotify {
		return fmt.Errorf("ragent sdk: use SendRPCFrame for RPC frame types")
	}
	return c.sendRaw(frame)
}

func (c *Client) sendFrame(frameType wire.FrameType, rpcHead wire.RPCWireHeader, body []byte) error {
	if c.conn == nil {
		return fmt.Errorf("ragent sdk: not connected")
	}
	headBuf, err := wire.AppendRPCWireHeader(nil, rpcHead)
	if err != nil {
		return err
	}
	f := wire.Frame{Type: frameType, Header: headBuf, Body: body}
	data, err := wire.EncodeFrame(f)
	if err != nil {
		return err
	}
	c.conn.AsyncSend(data)
	return nil
}

func (c *Client) sendRaw(frame wire.Frame) error {
	if c.conn == nil {
		return fmt.Errorf("ragent sdk: not connected")
	}
	data, err := wire.EncodeFrame(frame)
	if err != nil {
		return err
	}
	c.conn.AsyncSend(data)
	return nil
}

// onMessage 是网络库收到数据时的回调（在 loop 线程中执行）。
func (c *Client) onMessage(conn *network.Conn, in *network.Buffer) {
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
			in.RetrieveAll()
			_ = c.Close()
			return
		}
		in.Retrieve(4 + length)

		c.handleFrame(frame)
	}
}

func (c *Client) handleFrame(frame wire.Frame) {
	// Debug: log frame types for tracing
	switch frame.Type {
	case wire.FrameHandshakeAck:
		// 握手响应
		if len(frame.Body) == 0 || frame.Body[0] == 0 {
			c.handshakeCh <- fmt.Errorf("routeragent handshake rejected")
		} else {
			c.handshakeCh <- nil
		}

	case wire.FrameRpcResponse:
		head, err := wire.DecodeRPCWireHeader(frame.Header)
		if err != nil || c.core == nil {
			return
		}
		c.core.OnResponse(head.SeqID, frame.Body, errcode.ErrCode(head.ErrCode))

	case wire.FrameRpcRequest, wire.FrameRpcNotify:
		if c.onRecv == nil {
			return
		}
		if c.poster == nil {
			c.onRecv(frame)
			return
		}
		// copy frame data since it references the network buffer which will be reused
		f := wire.Frame{
			Type: frame.Type,
			Header: append([]byte(nil), frame.Header...),
			Body:   append([]byte(nil), frame.Body...),
		}
		c.poster.Post(func() {
			c.onRecv(f)
		})

	case wire.FrameHeartbeat:
		// echo heartbeat back
		_ = c.sendRaw(wire.Frame{Type: wire.FrameHeartbeat})
	}
}

func routingModeToWire(mode corerpc.RoutingMode) uint8 {
	switch mode {
	case corerpc.RoutingDirect:
		return uint8(wire.RoutingModeDirect)
	case corerpc.RoutingConsistentHash:
		return uint8(wire.RoutingModeHash)
	case corerpc.RoutingBroadcast:
		return uint8(wire.RoutingModeBroadcast)
	default:
		return uint8(wire.RoutingModeAny)
	}
}

func mustEncodeFrame(typ wire.FrameType, body []byte) []byte {
	data, err := wire.EncodeFrame(wire.Frame{Type: typ, Body: body})
	if err != nil {
		panic(err)
	}
	return data
}
