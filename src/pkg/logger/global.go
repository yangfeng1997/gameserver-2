package logger

import "sync/atomic"

// global 包级默认 Logger，原子指针保证并发安全替换
var global atomic.Pointer[Logger]

// globalSugar 缓存全局 SugaredLogger，避免 Debugf/Debugw 每次调用重新分配包装对象
var globalSugar atomic.Pointer[SugaredLogger]

func init() {
	var l Logger = &nopLogger{}
	global.Store(&l)
	var s SugaredLogger = nopSugared{}
	globalSugar.Store(&s)
}

// SetGlobal 替换全局 Logger（通常在 main 初始化后调用一次）
func SetGlobal(l Logger) {
	global.Store(&l)
	s := Sugar()
	globalSugar.Store(&s)
}

// L 获取全局 Logger
func L() Logger {
	return *global.Load()
}

// 包级快捷函数：强类型 Field，零反射，适合高频热路径。
func Debug(msg string, fields ...Field) { emit(DebugLevel, msg, fields) }
func Info(msg string, fields ...Field)  { emit(InfoLevel, msg, fields) }
func Warn(msg string, fields ...Field)  { emit(WarnLevel, msg, fields) }
func Error(msg string, fields ...Field) { emit(ErrorLevel, msg, fields) }
func Fatal(msg string, fields ...Field) { emit(FatalLevel, msg, fields) }

// 包级快捷函数：printf 格式化，适合少量变量拼接的低频路径。
func Debugf(format string, args ...any) { (*globalSugar.Load()).Debugf(format, args...) }
func Infof(format string, args ...any)  { (*globalSugar.Load()).Infof(format, args...) }
func Warnf(format string, args ...any)  { (*globalSugar.Load()).Warnf(format, args...) }
func Errorf(format string, args ...any) { (*globalSugar.Load()).Errorf(format, args...) }
func Fatalf(format string, args ...any) { (*globalSugar.Load()).Fatalf(format, args...) }

// 包级快捷函数：松散 key-value，适合快速打点。
func Debugw(msg string, kv ...any) { (*globalSugar.Load()).Debugw(msg, kv...) }
func Infow(msg string, kv ...any)  { (*globalSugar.Load()).Infow(msg, kv...) }
func Warnw(msg string, kv ...any)  { (*globalSugar.Load()).Warnw(msg, kv...) }
func Errorw(msg string, kv ...any) { (*globalSugar.Load()).Errorw(msg, kv...) }
func Fatalw(msg string, kv ...any) { (*globalSugar.Load()).Fatalw(msg, kv...) }

// emit 是包级函数的公共快路径：*adapterLogger 直接操作 backend，其余走接口调度
func emit(level Level, msg string, fields []Field) {
	l := *global.Load()
	if cl, ok := l.(*adapterLogger); ok {
		if level == DebugLevel && !cl.backend.IsEnabled(DebugLevel) {
			return
		}
		cl.backend.Log(level, msg, fields)
		return
	}
	switch level {
	case DebugLevel:
		l.Debug(msg, fields...)
	case InfoLevel:
		l.Info(msg, fields...)
	case WarnLevel:
		l.Warn(msg, fields...)
	case ErrorLevel:
		l.Error(msg, fields...)
	case FatalLevel:
		l.Fatal(msg, fields...)
	}
}

// With 从全局 Logger 派生子 Logger，携带固定字段。
func With(fields ...Field) Logger { return L().With(fields...) }

// nopLogger 空实现，init 阶段兜底，避免 nil panic。
type nopLogger struct{}

func (n *nopLogger) Debug(_ string, _ ...Field) {}
func (n *nopLogger) Info(_ string, _ ...Field)  {}
func (n *nopLogger) Warn(_ string, _ ...Field)  {}
func (n *nopLogger) Error(_ string, _ ...Field) {}
func (n *nopLogger) Fatal(_ string, _ ...Field) {}
func (n *nopLogger) With(_ ...Field) Logger     { return n }
func (n *nopLogger) IsEnabled(_ Level) bool     { return false }
