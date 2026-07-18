//go:build linux

package network_test

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"project/src/pkg/network"
)

// waitReadyAddr 轮询拨号直到地址就绪或超时。
func waitReadyAddr(t testing.TB, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server on %s 未就绪", addr)
}

// TestServerStopAccepting 验证 StopAccepting 是优雅停机原语：
//   - 停 accept 后新连接被拒（listen fd 关闭，dial 失败）；
//   - 但现有连接未被触及，仍可正常收发（继续在 subLoop 上服务）。
//
// 这是"停 accept 不杀 conn"的传输原语——优雅停机靠它 + 业务侧 drain 编排实现。
func TestServerStopAccepting(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	srv, err := network.NewServer(network.WithNumEventLoop(2))
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
	go srv.Run()
	defer srv.Stop()

	waitReadyAddr(t, addr)

	// 建一条连接，做一次 echo 往返确认存活。
	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial existing: %v", err)
	}
	defer c1.Close()
	payload := []byte("hello")
	buf := make([]byte, len(payload))
	if _, err := c1.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.ReadFull(c1, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	// 停 accept：不再接受新连接，但不关现有 conn。
	srv.StopAccepting()

	// 新连接应被拒。
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Fatal("StopAccepting 后新 dial 应失败，但成功了")
	}

	// 现有连接仍可收发——证明未被 StopAccepting 触及。
	if _, err := c1.Write(payload); err != nil {
		t.Fatalf("existing write after StopAccepting: %v", err)
	}
	if _, err := io.ReadFull(c1, buf); err != nil {
		t.Fatalf("existing read after StopAccepting: %v", err)
	}
}

// TestServerStopAcceptingIdempotent 验证 StopAccepting 幂等：
// 多次调用不应 panic，且 Stop 之后调 StopAccepting 也是无副作用。
func TestServerStopAcceptingIdempotent(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, err := network.NewServer(network.WithNumEventLoop(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.AddAddress("tcp", addr); err != nil {
		t.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) { in.RetrieveAll() })
	go srv.Run()

	// 等就绪后停。
	waitReadyAddr(t, addr)
	srv.StopAccepting()
	srv.StopAccepting() // 重复调用应幂等无 panic
	srv.Stop()
	srv.StopAccepting() // Stop 后再调也应幂等无 panic
}
