//go:build linux

package network_test

import (
	"fmt"
	"testing"
	"time"

	"project/src/pkg/network"
)

// TestServerWithLoops 验证 server 侧 loop 共用对偶：
//   - NewServerWithLoops 注入外部 EventLoopGroup，Run 不阻塞（注入模式）；
//   - Server.Loops() 暴露同一 group，NewClientWithLoops 注入后端 client 与玩家 server 共用 loop；
//   - 后端 conn 落在共享 group 的某条 loop 上；
//   - Server.Stop 只关 acceptor/连接 fd，不停共享 loop（由所有者停）。
func TestServerWithLoops(t *testing.T) {
	// 手建并启动一套共享 loop 池：base + 2 sub。
	opts := []network.Option{network.WithNumEventLoop(2)}
	o := network.Apply(opts)
	base, err := network.NewEventLoop(o)
	if err != nil {
		t.Fatal(err)
	}
	group := network.NewEventLoopGroup(base, o)
	group.Start()
	go base.Loop() // base 由所有者跑
	defer func() {
		base.Quit()
		group.Stop()
	}()

	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// 注入式 server：Run 不启 loop、不阻塞。
	srv, err := network.NewServerWithLoops(group, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.AddAddress("tcp", addr); err != nil {
		t.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		c.Send(in.Peek())
		in.RetrieveAll()
	})
	runDone := make(chan struct{})
	go func() {
		srv.Run() // 注入模式应立即返回
		close(runDone)
	}()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("注入式 Server.Run 不应阻塞")
	}

	// 后端 client 注入同一 group：玩家与后端共用 loop。
	cli := network.NewClientWithLoops(srv.Loops(), opts...)
	// 注意：OnMessage 须在 Dial 前设置——buildConn 会把当前回调固化进 conn。
	got := make(chan struct{}, 1)
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		in.RetrieveAll()
		select {
		case got <- struct{}{}:
		default:
		}
	})
	conn := dialWithRetry(t, "tcp", addr, cli)

	// 后端 conn 必落在共享 group 的某条 loop 上（共用成立的硬证据）。
	connLoop := conn.EventLoop()
	loops := group.Loops()
	inGroup := false
	for _, l := range loops {
		if l == connLoop {
			inGroup = true
			break
		}
	}
	if !inGroup {
		t.Fatal("client conn 未落在共享 group 的 loop 上")
	}

	// echo 验证：经共用 loop 的后端 conn 收发正常。
	conn.AsyncSend([]byte("hello"))
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("echo 超时")
	}

	// Server.Stop 不停共享 loop（base 仍由所有者持有）；defer 会再 Quit/Stop，幂等。
	srv.Stop()
}
