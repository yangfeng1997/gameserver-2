//go:build linux

package network_test

import (
	"fmt"
	"testing"
	"time"

	"project/src/pkg/network"
)

func TestBufferFramingAPI(t *testing.T) {
	b := network.NewBuffer(64)

	// PeekN：不消费看前 n 字节。
	b.Append([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05})
	if got := b.PeekN(4); len(got) != 4 || got[3] != 0x03 {
		t.Fatalf("PeekN(4) = %v", got)
	}
	if b.ReadableBytes() != 6 {
		t.Fatalf("PeekN 不应消费，剩 %d", b.ReadableBytes())
	}
	if b.PeekN(100) != nil {
		t.Fatal("PeekN 超出可读应返回 nil")
	}

	// PeekUint32BE / ReadUint32BE。
	if v, ok := b.PeekUint32BE(); !ok || v != 0x00010203 {
		t.Fatalf("PeekUint32BE = %v %v", v, ok)
	}
	if b.ReadableBytes() != 6 {
		t.Fatal("PeekUint32BE 不应消费")
	}
	if v, ok := b.ReadUint32BE(); !ok || v != 0x00010203 {
		t.Fatalf("ReadUint32BE = %v %v", v, ok)
	}
	if b.ReadableBytes() != 2 {
		t.Fatalf("ReadUint32BE 后应剩 2，剩 %d", b.ReadableBytes())
	}

	// ReadUint16BE。
	if v, ok := b.ReadUint16BE(); !ok || v != 0x0405 {
		t.Fatalf("ReadUint16BE = %v %v", v, ok)
	}
	if _, ok := b.ReadUint16BE(); ok {
		t.Fatal("可读不足应返回 false")
	}

	// PrependUint32BE：帧头回填。
	b.RetrieveAll()
	b.AppendString("payload")
	if !b.PrependUint32BE(7) {
		t.Fatal("PrependUint32BE 失败")
	}
	v, _ := b.PeekUint32BE()
	if v != 7 {
		t.Fatalf("PrependUint32BE 后 PeekUint32BE = %d", v)
	}
	b.Retrieve(4)
	if got := b.Peek(); string(got) != "payload" {
		t.Fatalf("Prepend 后载荷错 = %q", got)
	}
}

// TestLengthHeaderCodec 分帧往返：客户端发多帧粘包，服务端 codec 切分派发。
func TestLengthHeaderCodec(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	// 用 codec 替换服务端 OnMessage：回显原载荷。
	var codec *network.LengthHeaderCodec
	codec = network.NewLengthHeaderCodec(func(c *network.Conn, payload []byte) {
		codec.Send(c, payload) // 回显（自动加长度头）。
	})
	srv.SetOnMessage(codec.OnMessage)

	cli := network.NewClient()
	defer cli.Stop()

	// 客户端也用 codec 切收到的帧。
	got := make(chan []byte, 4)
	ccodec := network.NewLengthHeaderCodec(func(c *network.Conn, payload []byte) {
		select {
		case got <- payload:
		default:
		}
	})
	cli.SetOnMessage(ccodec.OnMessage)
	conn := dialWithRetry(t, "tcp", addr, cli)

	// 连发三帧（粘包），codec 须切分成三段。
	ccodec.Send(conn, []byte("hello"))
	ccodec.Send(conn, []byte("world"))
	ccodec.Send(conn, []byte("!"))
	want := [][]byte{[]byte("hello"), []byte("world"), []byte("!")}
	seen := map[string]bool{}
	for i := range 3 {
		select {
		case p := <-got:
			seen[string(p)] = true
		case <-time.After(time.Second):
			t.Fatalf("第 %d 帧超时", i)
		}
	}
	for _, w := range want {
		if !seen[string(w)] {
			t.Fatalf("未收到帧 %q", w)
		}
	}
}

// TestHighWaterMark 大载荷 + 对端不读 → 出站累积越阈值触发高水位回调。
func TestHighWaterMark(t *testing.T) {
	// 服务端建好后不读（OnMessage 空），让客户端 output 堆积。
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, err := network.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.AddAddress("tcp", addr); err != nil {
		t.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) { /* 不消费，模拟慢/不读 */ })
	go srv.Run()
	defer srv.Stop()

	cli := network.NewClient()
	defer cli.Stop()
	fired := make(chan int, 4)
	cli.SetOnConnect(func(c *network.Conn) {
		// 极小阈值，确保一发送就越线。
		c.SetHighWaterMark(1, func(cc *network.Conn, buffered int) {
			select {
			case fired <- buffered:
			default:
			}
		})
	})
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) { in.RetrieveAll() })
	conn := dialWithRetry(t, "tcp", addr, cli)

	// 等 OnConnect（设高水位）就绪。
	time.Sleep(100 * time.Millisecond)

	// 连发大载荷，对端不读 → output 累积越阈值。
	payload := make([]byte, 1<<20) // 1MiB
	for range 8 {
		conn.AsyncSend(payload)
	}
	select {
	case buffered := <-fired:
		if buffered < 1 {
			t.Fatalf("高水位回调 buffered=%d 应 >=1", buffered)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("高水位回调未触发（出站未堆积越阈值）")
	}
	if conn.OutboundBuffered() < 0 {
		t.Fatal("OutboundBuffered 不应为负")
	}
}
