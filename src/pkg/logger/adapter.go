package logger

// Backend 三方日志库适配器需要实现的内部接口
// 业务代码不直接依赖 Backend，只通过 Logger 使用
type Backend interface {
	Log(level Level, msg string, fields []Field)
	With(fields []Field) Backend
	IsEnabled(level Level) bool
}

// adapterLogger 将 Logger 接口桥接到 Backend
type adapterLogger struct {
	backend Backend
}

// New 用指定 Backend 创建 Logger
func New(b Backend) Logger {
	return &adapterLogger{backend: b}
}

func (l *adapterLogger) Debug(msg string, fields ...Field) {
	if l.backend.IsEnabled(DebugLevel) {
		l.backend.Log(DebugLevel, msg, fields)
	}
}

func (l *adapterLogger) Info(msg string, fields ...Field) {
	l.backend.Log(InfoLevel, msg, fields)
}

func (l *adapterLogger) Warn(msg string, fields ...Field) {
	l.backend.Log(WarnLevel, msg, fields)
}

func (l *adapterLogger) Error(msg string, fields ...Field) {
	l.backend.Log(ErrorLevel, msg, fields)
}

func (l *adapterLogger) Fatal(msg string, fields ...Field) {
	l.backend.Log(FatalLevel, msg, fields)
}

func (l *adapterLogger) With(fields ...Field) Logger {
	return &adapterLogger{backend: l.backend.With(fields)}
}

func (l *adapterLogger) IsEnabled(level Level) bool {
	return l.backend.IsEnabled(level)
}

// Sugar 透传 Backend 的 SugaredLogger 能力。
// 若 Backend 不支持，返回 nopSugared（不输出）。
func (l *adapterLogger) Sugar() SugaredLogger {
	if sp, ok := l.backend.(interface{ Sugar() SugaredLogger }); ok {
		return sp.Sugar()
	}
	return nopSugared{}
}
