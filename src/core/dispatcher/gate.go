package dispatcher

import (
	"project/src/core/codec"
	"project/src/core/errcode"
	"project/src/core/session"
)

// Conn gate 层需要的连接接口：只读地址 + 心跳更新。
type Conn interface {
	RemoteAddr() string
	TouchRecv()
}

// GateDispatcher 客户端入口流量分发器。
type GateDispatcher struct {
	*Dispatcher
	sessions         *session.SessionManager
	handshakeHandler func(Conn, []byte) bool
}

// NewGateDispatcher 创建 gate 分发器。
func NewGateDispatcher(selfServerType uint32, sessions *session.SessionManager) *GateDispatcher {
	return &GateDispatcher{
		Dispatcher: New(selfServerType),
		sessions:   sessions,
	}
}

// SetHandshakeHandler 设置握手回调。
func (g *GateDispatcher) SetHandshakeHandler(fn func(Conn, []byte) bool) {
	g.handshakeHandler = fn
}

// HandlePacket 处理来自连接的原始 packet。
func (g *GateDispatcher) HandlePacket(c Conn, pkt *codec.Packet) error {
	if pkt == nil {
		return errcode.New(errcode.ERR_UNMARSHAL, "nil packet")
	}
	if pkt.Type == codec.PacketData {
		sess := g.sessions.GetByConnID(c.RemoteAddr())
		return g.HandleSessionPacket(sess, c, *pkt)
	}
	return g.HandleSessionPacket(nil, c, *pkt)
}

// HandleSessionPacket 处理已解析 session 的 packet，避免热路径重复计算连接地址。
func (g *GateDispatcher) HandleSessionPacket(sess *session.Session, c Conn, pkt codec.Packet) error {
	switch pkt.Type {
	case codec.PacketHandshake:
		if g.handshakeHandler != nil {
			g.handshakeHandler(c, pkt.Body)
		}
		return nil
	case codec.PacketHandshakeAck:
		return nil
	case codec.PacketHeartbeat:
		c.TouchRecv()
		return nil
	case codec.PacketData:
		msg, err := codec.DecodeMessage(pkt.Body)
		if err != nil {
			return errcode.New(errcode.ERR_UNMARSHAL, err.Error())
		}
		if sess == nil {
			return errcode.New(errcode.ERR_UNAUTHED, "session not found")
		}
		return g.Dispatcher.Dispatch(sess, &msg)
	default:
		return nil
	}
}
