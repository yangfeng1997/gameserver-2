package logger

import (
	"testing"
	"time"
)

// ---------- 辅助 ----------

// NopBackend 用于 benchmark，无操作 Backend。
type NopBackend struct {
	enabled bool
}

var _ Backend = (*NopBackend)(nil)

func (n *NopBackend) IsEnabled(Level) bool  { return n.enabled }
func (n *NopBackend) With([]Field) Backend  { return n }
func (n *NopBackend) Log(Level, string, []Field) {}

// mockLogger 嵌入 Logger 绕过快路径类型断言，强制走接口调度。
type mockLogger struct {
	Logger
}

func (m *mockLogger) Info(msg string, fields ...Field) {}

// ---------- 强类型 Field 日志 ----------

func BenchmarkInfoNoFields(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("hello world")
	}
}

func BenchmarkInfo1Field(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("hello", String("key", "val"))
	}
}

func BenchmarkInfo5Fields(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("msg",
			String("s", "hello"),
			Int64("i64", int64(i)),
			Float64("f64", 3.14),
			Bool("ok", true),
			Duration("d", time.Millisecond),
		)
	}
}

func BenchmarkInfo10Fields(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("msg",
			String("s1", "hello"),
			Int64("i1", 1),
			Int64("i2", 2),
			Float64("f1", 3.14),
			Float64("f2", 2.71),
			Bool("b1", true),
			Bool("b2", false),
			Duration("d1", time.Millisecond),
			Duration("d2", time.Second),
			Int("n", 42),
		)
	}
}

func BenchmarkDebugDisabled(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: false}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Debug("should be skipped")
	}
}

func BenchmarkDebugEnabled(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Debug("enabled")
	}
}

// ---------- 接口层 vs 快路径 ----------

func BenchmarkInterfacePath(b *testing.B) {
	var l Logger = &mockLogger{}
	global.Store(&l)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("via interface")
	}
}

func BenchmarkFastPath(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Info("fast path")
	}
}

// ---------- With 派生 ----------

func BenchmarkWith(b *testing.B) {
	l := New(&NopBackend{enabled: true})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.With(String("uid", "abc"))
	}
}

func BenchmarkWithThenLog(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sub := With(String("uid", "abc"))
		sub.Info("action")
	}
}

// ---------- SugaredLogger ----------

func BenchmarkInfof(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Infof("hello %d %s", i, "world")
	}
}

func BenchmarkInfow(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Infow("msg", "key", "val", "key2", 42)
	}
}

// ---------- 分配数 ----------

func BenchmarkAllocs(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Info("msg",
			String("s", "hello"),
			Int64("i64", int64(i)),
			Float64("f64", 3.14),
		)
	}
}

func Benchmark5FieldsAllocs(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Info("msg",
			String("s", "hello"),
			Int64("i64", int64(i)),
			Float64("f64", 3.14),
			Bool("ok", true),
			Duration("d", time.Millisecond),
		)
	}
}

// ---------- Fatal ----------

func BenchmarkFatal(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Fatal("fatal msg")
	}
}

// ---------- 并行 ----------

func BenchmarkParallelInfo(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: true}))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			Info("concurrent", String("g", "parallel"))
		}
	})
}

func BenchmarkParallelDebugDisabled(b *testing.B) {
	SetGlobal(New(&NopBackend{enabled: false}))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			Debug("concurrent skip")
		}
	})
}
