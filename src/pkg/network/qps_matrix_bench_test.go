//go:build linux

package network_test

import (
	"fmt"
	"testing"

	"project/src/pkg/network"
)

// matrixCfg 矩阵压测配置（pipeline pipe=64，服务端真满载）。
type matrixCfg struct {
	loops int
	conns int
}

var matrixConfigs = []matrixCfg{
	{1, 256},
	{4, 1024},
	{8, 1024},
	{8, 4096},
}

// BenchmarkMatrix_Pipeline 三方 pipeline 吞吐矩阵（pipe=64，2s/cell）。
// 验证 codec/背压/atomic 修复后无性能回归，并给 (L,C) 扩展性视图。
func BenchmarkMatrix_Pipeline(b *testing.B) {
	backends := []struct {
		name  string
		start func(b *testing.B, loops, port int) func()
	}{
		{"our", startOurEcho},
		{"gnet", startGnetEcho},
		{"muduo", startMuduoEcho},
	}
	for _, cfg := range matrixConfigs {
		for _, be := range backends {
			name := fmt.Sprintf("%s_L%d_C%d", be.name, cfg.loops, cfg.conns)
			b.Run(name, func(b *testing.B) {
				qps := pipeMeasure(b, be.start, cfg.loops, cfg.conns, 64, qpsDur)
				fmt.Printf("  %-28s qps=%12.0f\n", name, qps)
			})
		}
	}
}

// 引用 network 以保留 import（矩阵 bench 经辅助函数使用）。
var _ = network.NewServer
