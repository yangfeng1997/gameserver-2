//go:build linux

package network_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// benchClientQPSPipe 用 conns 个 stdlib 客户端、pipeline 深度 pipe 的发送-读取，
// 解耦单连接 RTT 串行化，压服务端真实吞吐上限。
func benchClientQPSPipe(b *testing.B, port, conns, pipe int, dur time.Duration) float64 {
	b.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	sz := len(qpsPayload)
	wbuf := bytes.Repeat(qpsPayload, pipe) // pipe 帧
	rbuf := make([]byte, pipe*sz)

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
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := c.Write(wbuf); err != nil {
					return
				}
				if _, err := io.ReadFull(c, rbuf); err != nil {
					return
				}
				ops.Add(int64(pipe))
			}
		}(clients[i])
	}
	time.Sleep(dur)
	close(stop)
	wg.Wait()
	return float64(ops.Load()) / dur.Seconds()
}

// pipeMeasure 启动服务端，跑 pipeline 客户端，返回 qps。
func pipeMeasure(b *testing.B, start func(b *testing.B, loops, port int) func(), loops, conns, pipe int, dur time.Duration) float64 {
	b.Helper()
	port := freePortB(b)
	stop := start(b, loops, port)
	defer stop()
	return benchClientQPSPipe(b, port, conns, pipe, dur)
}

// BenchmarkPipe_Our 本库 pipeline 吞吐。
func BenchmarkPipe_Our(b *testing.B) {
	qps := pipeMeasure(b, startOurEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("our    pipe L8 C1024 pipe=64  qps=%12.0f\n", qps)
}

// BenchmarkPipe_Gnet gnet pipeline 吞吐。
func BenchmarkPipe_Gnet(b *testing.B) {
	qps := pipeMeasure(b, startGnetEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("gnet   pipe L8 C1024 pipe=64  qps=%12.0f\n", qps)
}

// BenchmarkPipe_Muduo muduo pipeline 吞吐。
func BenchmarkPipe_Muduo(b *testing.B) {
	if _, err := exec.LookPath(muduoBin); err != nil {
		b.Skipf("muduo binary not found: %v", err)
	}
	qps := pipeMeasure(b, startMuduoEcho, 8, 1024, 64, qpsDur)
	b.ReportMetric(qps, "qps")
	fmt.Printf("muduo  pipe L8 C1024 pipe=64  qps=%12.0f\n", qps)
}

// BenchmarkProfPipe_Our 长时 pipeline，供 -cpuprofile 采样服务端热点。
func BenchmarkProfPipe_Our(b *testing.B) {
	qps := pipeMeasure(b, startOurEcho, 8, 1024, 64, 10*time.Second)
	b.ReportMetric(qps, "qps")
}

// BenchmarkProfPipe_Gnet 长时 gnet pipeline，供 -cpuprofile 对比服务端热点。
func BenchmarkProfPipe_Gnet(b *testing.B) {
	qps := pipeMeasure(b, startGnetEcho, 8, 1024, 64, 10*time.Second)
	b.ReportMetric(qps, "qps")
}
