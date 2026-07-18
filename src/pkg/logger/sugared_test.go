package logger_test

import (
	"bytes"
	"strings"
	"testing"

	"project/src/pkg/logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newBufLogger 建一个写到 buf 的 Logger（经 adapterLogger 包装，走 SugarOf 类型断言路径）。
func newBufLogger(buf *bytes.Buffer) logger.Logger {
	enc := zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:     "T",
		LevelKey:    "L",
		MessageKey:  "M",
		EncodeLevel: zapcore.CapitalLevelEncoder,
	})
	z := zap.New(zapcore.NewCore(enc, zapcore.AddSync(buf), zapcore.DebugLevel))
	return logger.New(logger.NewZapBackend(z))
}

// TestSugarOf_Instance 实例级 sugar 可用：经 Logger 接口（adapterLogger）取 sugar，
// printf 输出落到底层 backend。
func TestSugarOf_Instance(t *testing.T) {
	var buf bytes.Buffer
	log := newBufLogger(&buf)

	s := logger.SugarOf(log) // 接口不暴露 Sugar()，靠断言取
	s.Infof("conn read err sid=%d", 123)

	out := buf.String()
	if !strings.Contains(out, "conn read err sid=123") {
		t.Fatalf("实例 sugar 未输出预期消息, got: %q", out)
	}
}

// TestSugarOf_BoundFields 绑定字段的派生 logger 的 sugar 继承字段：
// log.With(component=network).Sugar() 的每条日志带 component=network。
func TestSugarOf_BoundFields(t *testing.T) {
	var buf bytes.Buffer
	log := newBufLogger(&buf)

	netSugar := logger.SugarOf(log.With(logger.String("component", "network")))
	netSugar.Errorf("conn err")

	out := buf.String()
	if !strings.Contains(out, "conn err") {
		t.Fatalf("派生 sugar 未输出消息, got: %q", out)
	}
	if !strings.Contains(out, "network") {
		t.Fatalf("派生 sugar 未继承绑定字段 component=network, got: %q", out)
	}
}

// noSugarLogger 实现 Logger 但不暴露 Sugar()，验证 SugarOf 回退 nopSugared 不 panic。
type noSugarLogger struct{}

func (noSugarLogger) Debug(string, ...logger.Field) {}
func (noSugarLogger) Info(string, ...logger.Field)  {}
func (noSugarLogger) Warn(string, ...logger.Field)  {}
func (noSugarLogger) Error(string, ...logger.Field) {}
func (noSugarLogger) Fatal(string, ...logger.Field) {}
func (noSugarLogger) With(...logger.Field) logger.Logger { return noSugarLogger{} }
func (noSugarLogger) IsEnabled(logger.Level) bool       { return false }

func TestSugarOf_Fallback(t *testing.T) {
	s := logger.SugarOf(noSugarLogger{}) // 无 Sugar() 方法
	// 回退 nopSugared：调用不 panic、不输出。
	s.Debugf("x=%d", 1)
	s.Infow("msg", "k", "v")
	s = s.With("k", "v")
	s.Errorf("nope")
}
