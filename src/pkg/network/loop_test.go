//go:build linux

package network_test

import (
	"testing"
	"time"

	"project/src/pkg/network"
)

type loopState struct {
	n int
}

// TestEventLoopContext 验证 ②：per-loop 上下文读写。
func TestEventLoopContext(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	seen := make(chan struct{}, 1)
	srv.SetOnConnect(func(c *network.Conn) {
		L := c.EventLoop()
		if L.Context() == nil {
			L.SetContext(&loopState{})
		}
		st := L.Context().(*loopState)
		st.n++
		seen <- struct{}{}
	})

	cli := network.NewClient()
	defer cli.Stop()
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) { in.RetrieveAll() })
	conn := dialWithRetry(t, "tcp", addr, cli)
	conn.AsyncSend([]byte("hi"))

	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("OnConnect not fired")
	}
}

// TestClientStopClosesConn 验证 ①：Client.Stop 触发 loop 退出 teardown 业务 conn，OnClose 触发（否则 fd 泄漏）。
func TestClientStopClosesConn(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	cli := network.NewClient()
	closed := make(chan struct{}, 2)
	cli.SetOnClose(func(c *network.Conn) {
		select {
		case closed <- struct{}{}:
		default:
		}
	})
	received := make(chan struct{}, 1)
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		in.RetrieveAll()
		select {
		case received <- struct{}{}:
		default:
		}
	})
	conn := dialWithRetry(t, "tcp", addr, cli)
	if cli.Conn() != conn { // ⑤ Conn() 访问器
		t.Fatal("Client.Conn() != dial 返回的 conn")
	}
	conn.AsyncSend([]byte("ping"))
	<-received // 确保 conn 已在 loop 的 poller 注册

	cli.Stop() // → loops 退出 → teardown conn → OnClose
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Client.Stop 未触发 OnClose（fd 泄漏）")
	}
}

// TestDialOn 验证 ③：DialOn 把 conn 落到指定 loop，且能正常收发。
func TestDialOn(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	cli := network.NewClient(network.WithNumEventLoop(2))
	defer cli.Stop()
	cli.Start() // 先建 loop 池
	received := make(chan struct{}, 1)
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		in.RetrieveAll()
		select {
		case received <- struct{}{}:
		default:
		}
	})
	// 选 client 第一个 loop 做 DialOn 目标，重试至服务端就绪。
	loop := cli.Loops().Loops()[0]
	var conn *network.Conn
	for range 50 {
		c, err := cli.DialOn(loop, "tcp", addr)
		if err == nil {
			conn = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("dialOn retry failed")
	}
	if conn.EventLoop() != loop {
		t.Fatal("DialOn 的 conn 未落在指定 loop")
	}
	conn.AsyncSend([]byte("x"))
	<-received
}
