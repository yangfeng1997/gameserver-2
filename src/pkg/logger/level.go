package logger

import "fmt"

// Level 日志级别
type Level int8

const (
	DebugLevel Level = iota // 仅开发/调试环境启用
	InfoLevel               // 常规运维信息
	WarnLevel               // 潜在问题，不影响服务
	ErrorLevel              // 需人工介入的错误
	FatalLevel              // 不可恢复，记录后 os.Exit(1)
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	case FatalLevel:
		return "fatal"
	default:
		return "unknown"
	}
}

func (l Level) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

func (l *Level) UnmarshalText(text []byte) error {
	switch string(text) {
	case "debug":
		*l = DebugLevel
	case "info", "":
		*l = InfoLevel
	case "warn", "warning":
		*l = WarnLevel
	case "error":
		*l = ErrorLevel
	case "fatal":
		*l = FatalLevel
	default:
		return fmt.Errorf("unknown log level %q", text)
	}
	return nil
}
