//go:build linux

package network_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panjf2000/gnet/v2"

	"project/src/pkg/network"
)

// 三方服务端都是 dumb byte echo（muduo server.cc 即 conn->send(buf)），
// 故客户端发任意字节、读回即可，协议无关、三方公平。

const muduoBin = "/root/gopoll/third_repo/muduo/build/bin/pingpong_server"

var qpsPayload = []byte("ping") // 4 字节

// qpsConfig 一个压测配置。
type qpsConfig struct {
	loops int
	conns int
}

var qpsConfigs = []qpsConfig{
	{1, 1},
	{4, 256},
	{8, 1024},
	{16, 4096},
}

// readFull 读满 buf，失败返回 false。
func readFull(c net.Conn, buf []byte) bool {
	if _, err := io.ReadFull(c, buf); err != nil {
		return false
	}
	return true
}

// benchClientQPS 用 conns 个 stdlib 客户端同步 ping-pong dur 秒，返回 QPS。
func benchClientQPS(b *testing.B, port, conns int, dur time.Duration) float64 {
	b.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	sz := len(qpsPayload)

	clients := make([]net.Conn, conns)
	for i := range conns {
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			b.Fatalf("dial %d/%d: %v", i, conns, err)
		}
		clients[i] = c
	}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()

	var ops atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(conns)
	for i := range conns {
		go func(c net.Conn) {
			defer wg.Done()
			buf := make([]byte, sz)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := c.Write(qpsPayload); err != nil {
					return
				}
				if !readFull(c, buf) {
					return
				}
				ops.Add(1)
			}
		}(clients[i])
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	return float64(ops.Load()) / dur.Seconds()
}

// startOurEcho 启动本库 dumb-echo 服务（loops 个 sub loop），返回停止函数。
func startOurEcho(b *testing.B, loops, port int) func() {
	return startOurEchoOpts(b, loops, port)
}

// startOurEchoOpts 启动本库 dumb-echo，带额外选项（用于压测不同 buffer 配置）。
func startOurEchoOpts(b *testing.B, loops, port int, opts ...network.Option) func() {
	b.Helper()
	base := []network.Option{network.WithNumEventLoop(loops)}
	srv, err := network.NewServer(append(base, opts...)...)
	if err != nil {
		b.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := srv.AddAddress("tcp", addr); err != nil {
		b.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		c.Send(in.Peek())
		in.RetrieveAll()
	})
	go srv.Run()
	waitDial(b, port)
	return func() { srv.Stop() }
}

// gnetQpsEcho gnet v2 echo handler。
type gnetQpsEcho struct {
	*gnet.BuiltinEventEngine
	eng   gnet.Engine
	ready chan struct{}
}

func (e *gnetQpsEcho) OnBoot(eng gnet.Engine) gnet.Action {
	e.eng = eng
	close(e.ready)
	return gnet.None
}

func (e *gnetQpsEcho) OnTraffic(c gnet.Conn) gnet.Action {
	buf, _ := c.Next(-1)
	_, _ = c.Write(buf)
	return gnet.None
}

// startGnetEcho 启动 gnet dumb-echo 服务（loops 个 event loop），返回停止函数。
func startGnetEcho(b *testing.B, loops, port int) func() {
	b.Helper()
	e := &gnetQpsEcho{ready: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- gnet.Run(e, fmt.Sprintf("tcp://127.0.0.1:%d", port),
			gnet.WithNumEventLoop(loops), gnet.WithMulticore(false))
	}()
	select {
	case <-e.ready:
	case err := <-errCh:
		b.Fatalf("gnet run: %v", err)
	case <-time.After(3 * time.Second):
		b.Fatal("gnet boot timeout")
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.eng.Stop(ctx)
		<-errCh
	}
}

// startMuduoEcho 启动 muduo pingpong_server（threads 个线程），返回停止函数。无二进制则跳过。
func startMuduoEcho(b *testing.B, threads, port int) func() {
	b.Helper()
	cmd := exec.Command(muduoBin, "127.0.0.1", strconv.Itoa(port), strconv.Itoa(threads))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		b.Skipf("muduo binary not runnable at %s: %v", muduoBin, err)
	}
	waitDial(b, port)
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

// waitDial 轮询拨号直到服务端就绪。
func waitDial(b *testing.B, port int) {
	b.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Fatalf("server on port %d not ready", port)
}

func freePortB(b *testing.B) int {
	b.Helper()
	return freePort(b)
}

// runQPSMeasure 启动指定服务端，跑 conns 客户端 dur 秒，返回 qps。
func runQPSMeasure(b *testing.B, start func(b *testing.B, loops, port int) func(), loops, conns int, dur time.Duration) float64 {
	b.Helper()
	port := freePortB(b)
	stop := start(b, loops, port)
	defer stop()
	return benchClientQPS(b, port, conns, dur)
}

const qpsDur = 2 * time.Second

// BenchmarkQPS_Our 本库 dumb-echo 多连接 QPS。
func BenchmarkQPS_Our(b *testing.B) {
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := runQPSMeasure(b, startOurEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			b.ReportMetric(qps/float64(cfg.conns), "qps/conn")
			fmt.Printf("our    L=%-2d C=%-4d  qps=%12.0f  qps/conn=%9.0f\n", cfg.loops, cfg.conns, qps, qps/float64(cfg.conns))
		})
	}
}

// BenchmarkQPS_Gnet gnet dumb-echo 多连接 QPS。
func BenchmarkQPS_Gnet(b *testing.B) {
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := runQPSMeasure(b, startGnetEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			b.ReportMetric(qps/float64(cfg.conns), "qps/conn")
			fmt.Printf("gnet   L=%-2d C=%-4d  qps=%12.0f  qps/conn=%9.0f\n", cfg.loops, cfg.conns, qps, qps/float64(cfg.conns))
		})
	}
}

// BenchmarkQPS_Muduo muduo C++ dumb-echo 多连接 QPS（需二进制，无则跳过）。
func BenchmarkQPS_Muduo(b *testing.B) {
	if _, err := exec.LookPath(muduoBin); err != nil {
		b.Skipf("muduo binary not found: %v", err)
	}
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := runQPSMeasure(b, startMuduoEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			b.ReportMetric(qps/float64(cfg.conns), "qps/conn")
			fmt.Printf("muduo  L=%-2d C=%-4d  qps=%12.0f  qps/conn=%9.0f\n", cfg.loops, cfg.conns, qps, qps/float64(cfg.conns))
		})
	}
}
