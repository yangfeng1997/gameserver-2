//go:build linux

package network

import (
	"encoding/binary"
)

// MessageHandler 单帧载荷到达回调。payload 为该帧载荷的拷贝，回调返回后仍可安全持有。
type MessageHandler func(c *Conn, payload []byte)

// LengthHeaderCodec muduo LengthHeaderCodec 对偶：4 字节 big-endian 长度前缀分帧。
//
// 帧格式：[uint32 BE length][length 字节 payload]。
//
// 用法：
//
//	codec := NewLengthHeaderCodec(onFrame)
//	srv.SetOnMessage(codec.OnMessage)   // 替换裸 OnMessage
//	...
//	codec.Send(conn, []byte(payload))   // 发送自动加长度头
//
// 设计取舍：载荷拷贝一次（防业务持有切片后被下次读覆盖），发送按帧单次分配
// （零拷贝化需暴露 Conn 出站 buffer 注入接口，留作后续打磨）。
type LengthHeaderCodec struct {
	onMessage MessageHandler
	maxLen    int // 单帧载荷上限，防错帧/恶意大帧耗尽内存
}

const defaultMaxFrameLen = 64 * 1024 * 1024 // 64MiB

// NewLengthHeaderCodec 构造分帧 codec，onMessage 为单帧载荷回调。
// 默认单帧上限 64MiB，用 SetMaxFrameSize 调整。
func NewLengthHeaderCodec(onMessage MessageHandler) *LengthHeaderCodec {
	return &LengthHeaderCodec{onMessage: onMessage, maxLen: defaultMaxFrameLen}
}

// SetMaxFrameSize 设置单帧载荷上限。超限帧视为协议错误，强制关闭连接。
func (cc *LengthHeaderCodec) SetMaxFrameSize(n int) { cc.maxLen = n }

// OnMessage 适配 MessageCallback：循环从入站缓冲切分完整帧并派发。
// 不完整帧保留到下次可读，长度超限强制关闭。须在 loop 线程（即作为 OnMessage 回调）。
func (cc *LengthHeaderCodec) OnMessage(c *Conn, in *Buffer) {
	for {
		if in.ReadableBytes() < 4 {
			return // 长度头未到齐。
		}
		length, _ := in.PeekUint32BE()
		if int(length) > cc.maxLen {
			c.Logger().Errorf("codec: frame length %d > max %d, close %s", length, cc.maxLen, c.Name())
			c.ForceClose()
			return
		}
		if in.ReadableBytes() < 4+int(length) {
			return // 帧体未到齐，等下次可读。
		}
		in.Retrieve(4) // 消费长度头。
		payload := in.Next(int(length))
		// 拷贝载荷：业务回调可能持有切片超出本次 OnMessage 范围，
		// 而 Next 返回的是缓冲内部切片，下次读会被覆盖。
		p := make([]byte, len(payload))
		copy(p, payload)
		cc.onMessage(c, p)
	}
}

// Send 给 payload 加 4 字节长度头后发送（线程安全，经 AsyncSend 投递到 loop）。
// loop 线程内调用会多一次队列投递开销；若已知在 loop 线程，用 SendInLoop 走快路径。
func (cc *LengthHeaderCodec) Send(c *Conn, payload []byte) {
	if len(payload) == 0 {
		return
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	c.AsyncSend(frame)
}

// SendInLoop 给 payload 加长度头后在 loop 线程直接发送（零投递开销）。
// 仅在 OnMessage/OnConnect 等 loop 线程回调内调用。
func (cc *LengthHeaderCodec) SendInLoop(c *Conn, payload []byte) {
	if len(payload) == 0 {
		return
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	c.Send(frame)
}
