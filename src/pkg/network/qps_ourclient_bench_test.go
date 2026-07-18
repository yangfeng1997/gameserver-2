//go:build linux

package network_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"project/src/pkg/network"
)

// ourClientLoops 负载客户端的 loop 数（固定，留核给服务端）。
const ourClientLoops = 8

// connState 每连接的回显计数与信号，挂在 Conn.Context 上，供负载 goroutine 同步等待。
type connState struct {
	rcvd atomic.Int64
	sig  chan struct{}
}

// dialOurClient 用 network.Client 重试拨号一个连接。
func dialOurClient(b *testing.B, cli *network.Client, addr string) *network.Conn {
	b.Helper()
	var conn *network.Conn
	var err error
	for range 50 {
		conn, err = cli.Dial("tcp", addr)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.Fatalf("dial %s: %v", addr, err)
	return nil
}

// benchOurClientQPS 用 network.Client 做 conns 个连接同步 ping-pong dur 秒。
func benchOurClientQPS(b *testing.B, start func(b *testing.B, loops, port int) func(), loops, conns int, dur time.Duration) float64 {
	b.Helper()
	port := freePortB(b)
	stopSrv := start(b, loops, port)
	defer stopSrv()

	cli := network.NewClient(network.WithNumEventLoop(ourClientLoops))
	cli.Start()
	defer cli.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	sz := len(qpsPayload)

	// onMessage 先挂（适用于所有后续连接），按 Conn.Context 分发回显计数。
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		st, ok := c.Context().(*connState)
		if !ok {
			in.RetrieveAll()
			return
		}
		st.rcvd.Add(int64(in.ReadableBytes()))
		in.RetrieveAll()
		select {
		case st.sig <- struct{}{}:
		default:
		}
	})

	conns2 := make([]*network.Conn, conns)
	states := make([]*connState, conns)
	for i := range conns {
		conn := dialOurClient(b, cli, addr)
		st := &connState{sig: make(chan struct{}, 1)}
		conn.SetContext(st)
		conns2[i] = conn
		states[i] = st
	}
	defer func() {
		for _, c := range conns2 {
			c.Close()
		}
	}()

	var ops atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(conns)
	for i := range conns {
		go func(conn *network.Conn, st *connState) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn.AsyncSend(qpsPayload)
				for st.rcvd.Load() < int64(sz) {
					select {
					case <-st.sig:
					case <-stop:
						return
					}
				}
				st.rcvd.Store(0)
				ops.Add(1)
			}
		}(conns2[i], states[i])
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	return float64(ops.Load()) / dur.Seconds()
}

// benchOurClientPipe 用 network.Client 做 pipeline（pipe 深度）吞吐。
func benchOurClientPipe(b *testing.B, start func(b *testing.B, loops, port int) func(), loops, conns, pipe int, dur time.Duration) float64 {
	b.Helper()
	port := freePortB(b)
	stopSrv := start(b, loops, port)
	defer stopSrv()

	cli := network.NewClient(network.WithNumEventLoop(ourClientLoops))
	cli.Start()
	defer cli.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	wbuf := bytes.Repeat(qpsPayload, pipe) // pipe 帧拼一个 buffer，1 次 AsyncSend（与 stdlib pipeline 对齐）
	target := int64(len(wbuf))

	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		st, ok := c.Context().(*connState)
		if !ok {
			in.RetrieveAll()
			return
		}
		st.rcvd.Add(int64(in.ReadableBytes()))
		in.RetrieveAll()
		select {
		case st.sig <- struct{}{}:
		default:
		}
	})

	conns2 := make([]*network.Conn, conns)
	states := make([]*connState, conns)
	for i := range conns {
		conn := dialOurClient(b, cli, addr)
		st := &connState{sig: make(chan struct{}, 1)}
		conn.SetContext(st)
		conns2[i] = conn
		states[i] = st
	}
	defer func() {
		for _, c := range conns2 {
			c.Close()
		}
	}()

	var ops atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(conns)
	for i := range conns {
		go func(conn *network.Conn, st *connState) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn.AsyncSend(wbuf)
				for st.rcvd.Load() < target {
					select {
					case <-st.sig:
					case <-stop:
						return
					}
				}
				st.rcvd.Store(0)
				ops.Add(int64(pipe))
			}
		}(conns2[i], states[i])
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	return float64(ops.Load()) / dur.Seconds()
}

// --- 同步矩阵（network.Client 负载）---

func BenchmarkOC_Our(b *testing.B) {
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := benchOurClientQPS(b, startOurEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			fmt.Printf("our-cli→our    L=%-2d C=%-4d  qps=%12.0f\n", cfg.loops, cfg.conns, qps)
		})
	}
}
func BenchmarkOC_Gnet(b *testing.B) {
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := benchOurClientQPS(b, startGnetEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			fmt.Printf("our-cli→gnet   L=%-2d C=%-4d  qps=%12.0f\n", cfg.loops, cfg.conns, qps)
		})
	}
}
func BenchmarkOC_Muduo(b *testing.B) {
	if _, err := exec.LookPath(muduoBin); err != nil {
		b.Skipf("muduo binary not found: %v", err)
	}
	for _, cfg := range qpsConfigs {
		b.Run(fmt.Sprintf("L%d_C%d", cfg.loops, cfg.conns), func(b *testing.B) {
			qps := benchOurClientQPS(b, startMuduoEcho, cfg.loops, cfg.conns, qpsDur)
			b.ReportMetric(qps, "qps")
			fmt.Printf("our-cli→muduo  L=%-2d C=%-4d  qps=%12.0f\n", cfg.loops, cfg.conns, qps)
		})
	}
}

// --- pipeline 极限（network.Client 负载，L8/C1024/pipe=64）---

func BenchmarkOCPipe_Our(b *testing.B) {
	qps := benchOurClientPipe(b, startOurEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("our-cli→our    pipe L8 C1024 p64  qps=%12.0f\n", qps)
}
func BenchmarkOCPipe_Gnet(b *testing.B) {
	qps := benchOurClientPipe(b, startGnetEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("our-cli→gnet   pipe L8 C1024 p64  qps=%12.0f\n", qps)
}
func BenchmarkOCPipe_Muduo(b *testing.B) {
	if _, err := exec.LookPath(muduoBin); err != nil {
		b.Skipf("muduo binary not found: %v", err)
	}
	qps := benchOurClientPipe(b, startMuduoEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("our-cli→muduo  pipe L8 C1024 p64  qps=%12.0f\n", qps)
}

// --- burst 极限（network.Client 每 cycle 64 次 AsyncSend(4B)，验证 eventfd 合并唤醒 + 零闭包）---

func benchOurClientBurst(b *testing.B, start func(b *testing.B, loops, port int) func(), loops, conns, pipe int, dur time.Duration) float64 {
	b.Helper()
	port := freePortB(b)
	stopSrv := start(b, loops, port)
	defer stopSrv()

	cli := network.NewClient(network.WithNumEventLoop(ourClientLoops))
	cli.Start()
	defer cli.Stop()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	sz := len(qpsPayload)
	target := int64(pipe * sz)

	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		st, ok := c.Context().(*connState)
		if !ok {
			in.RetrieveAll()
			return
		}
		st.rcvd.Add(int64(in.ReadableBytes()))
		in.RetrieveAll()
		select {
		case st.sig <- struct{}{}:
		default:
		}
	})

	conns2 := make([]*network.Conn, conns)
	states := make([]*connState, conns)
	for i := range conns {
		conn := dialOurClient(b, cli, addr)
		st := &connState{sig: make(chan struct{}, 1)}
		conn.SetContext(st)
		conns2[i] = conn
		states[i] = st
	}
	defer func() {
		for _, c := range conns2 {
			c.Close()
		}
	}()

	var ops atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(conns)
	for i := range conns {
		go func(conn *network.Conn, st *connState) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for range pipe {
					conn.AsyncSend(qpsPayload)
				}
				for st.rcvd.Load() < target {
					select {
					case <-st.sig:
					case <-stop:
						return
					}
				}
				st.rcvd.Store(0)
				ops.Add(int64(pipe))
			}
		}(conns2[i], states[i])
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	return float64(ops.Load()) / dur.Seconds()
}

func BenchmarkOCBurst_Our(b *testing.B) {
	qps := benchOurClientBurst(b, startOurEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("our-cli→our    burst L8 C1024 p64 (64 AsyncSend/cycle)  qps=%12.0f\n", qps)
}
