package network

import (
	"fmt"
	"log"
	"os"
)

// Level 日志级别，从低到高。
type Level int

const (
	// DebugLevel 调试：开发期细节，生产关闭。
	DebugLevel Level = iota
	// InfoLevel 信息：正常运行态。
	InfoLevel
	// WarnLevel 警告：可恢复的异常。
	WarnLevel
	// ErrorLevel 错误：需关注但程序继续。
	ErrorLevel
	// FatalLevel 致命：记录后 os.Exit(1)，仅限启动/不可恢复路径。
	FatalLevel
)

// Logger 库内日志接口（gnet 风格），printf 签名，解耦于业务日志实现。
// 默认 stdLogger（基于 stdlib log，输出 stderr），通过 WithLogger 注入自定义实现。
// 业务若想复用自有 logger，只需写一个适配本接口的薄包装传入 WithLogger。
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// nopLogger 空实现，用作默认零值兜底。
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
func (nopLogger) Fatalf(format string, args ...any) {
	fmt.Printf(format, args...)
	os.Exit(1)
}

// stdLogger 基于 stdlib log 的默认实现，带级别门控。
type stdLogger struct {
	logger *log.Logger
	level  Level
}

// NewStdLogger 构造一个输出到 w 的默认 Logger，低于 level 的不输出。
// 默认 prefix="network "、带微秒与日期。
func NewStdLogger(out *os.File, level Level) Logger {
	return &stdLogger{
		logger: log.New(out, "network ", log.LstdFlags|log.Lmicroseconds),
		level:  level,
	}
}

func (s *stdLogger) Debugf(format string, args ...any) {
	if s.level > DebugLevel {
		return
	}
	s.logger.Printf("[DEBUG] "+format, args...)
}
func (s *stdLogger) Infof(format string, args ...any) {
	if s.level > InfoLevel {
		return
	}
	s.logger.Printf("[INFO] "+format, args...)
}
func (s *stdLogger) Warnf(format string, args ...any) {
	if s.level > WarnLevel {
		return
	}
	s.logger.Printf("[WARN] "+format, args...)
}
func (s *stdLogger) Errorf(format string, args ...any) {
	if s.level > ErrorLevel {
		return
	}
	s.logger.Printf("[ERROR] "+format, args...)
}
func (s *stdLogger) Fatalf(format string, args ...any) {
	s.logger.Printf("[FATAL] "+format, args...)
	os.Exit(1)
}

// defaultLogger 包级默认实例，Options 未注入 Logger 时使用。
var defaultLogger Logger = NewStdLogger(os.Stderr, InfoLevel)
