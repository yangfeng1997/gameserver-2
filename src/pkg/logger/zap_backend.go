package logger

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapBackend 将 Backend 适配到 uber-go/zap。
type ZapBackend struct {
	z         *zap.Logger
	atomLevel zap.AtomicLevel
}

// NewZapBackend 用已有的 *zap.Logger 创建 ZapBackend。
func NewZapBackend(z *zap.Logger) *ZapBackend {
	return &ZapBackend{z: z}
}

// NewZapFileLogger 创建文件 Logger，支持格式化输出和滚动。
//
// FormatConsole：[2006-01-02 15:04:05.000][info] [a/b/c.go:42] msg  fields
// FormatJSON：  {"T":"...","L":"info","C":"a/b/c.go:42","M":"msg",...}
func NewZapFileLogger(cfg FileLoggerConfig) (Logger, *LogCloser, error) {
	rw, err := NewRotatingWriter(cfg.Rotate)
	if err != nil {
		return nil, nil, err
	}

	var enc zapcore.Encoder
	switch cfg.Format {
	case FormatJSON:
		enc = zapcore.NewJSONEncoder(jsonEncoderConfig())
	default:
		enc = zapcore.NewConsoleEncoder(bracketEncoderConfig())
	}

	var w zapcore.WriteSyncer
	if cfg.StderrAlso {
		w = zapcore.NewMultiWriteSyncer(zapcore.AddSync(rw), zapcore.AddSync(os.Stderr))
	} else {
		w = zapcore.AddSync(rw)
	}

	atom := zap.NewAtomicLevelAt(toZapLevel(cfg.Level))
	core := zapcore.NewCore(enc, w, atom)
	z := zap.New(core,
		zap.AddCaller(),
		zap.AddCallerSkip(2+cfg.CallerSkip),
		zap.WithFatalHook(zapcore.WriteThenPanic),
	)
	zb := &ZapBackend{z: z, atomLevel: atom}
	return New(zb), &LogCloser{zb: zb, rw: rw}, nil
}

// Format 日志输出格式。
type Format string

const (
	FormatConsole Format = "console" // [time][level] [caller] msg fields
	FormatJSON    Format = "json"    // {"T":...,"L":...,"C":...,"M":...,...}
)

// FileLoggerConfig 文件日志完整配置。
type FileLoggerConfig struct {
	Level      Level        `yaml:"level"`
	Format     Format       `yaml:"format"`
	StderrAlso bool         `yaml:"stderr_also"`
	CallerSkip int          `yaml:"caller_skip"`
	Rotate     RotateConfig `yaml:"rotate"`
}

// LogCloser 负责 flush 和关闭底层文件，程序退出前调用。
type LogCloser struct {
	zb *ZapBackend
	rw io.Closer
}

func (c *LogCloser) Close() error {
	_ = c.zb.Sync()
	return c.rw.Close()
}

// SetLevel 运行时切换日志级别。
func (c *LogCloser) SetLevel(l Level) { c.zb.SetLevel(l) }

// Backend 接口实现。

func (b *ZapBackend) IsEnabled(level Level) bool {
	return b.z.Core().Enabled(toZapLevel(level))
}

func (b *ZapBackend) With(fields []Field) Backend {
	return &ZapBackend{z: b.z.With(toZapFields(fields)...)}
}

func (b *ZapBackend) Log(level Level, msg string, fields []Field) {
	zfs := toZapFields(fields)
	switch level {
	case DebugLevel:
		b.z.Debug(msg, zfs...)
	case InfoLevel:
		b.z.Info(msg, zfs...)
	case WarnLevel:
		b.z.Warn(msg, zfs...)
	case ErrorLevel:
		b.z.Error(msg, zfs...)
	case FatalLevel:
		b.z.Fatal(msg, zfs...)
	}
}

// ZapBackend 扩展方法。

// SetLevel 运行时切换日志级别（热更路径，无锁）。
func (b *ZapBackend) SetLevel(l Level) {
	b.atomLevel.SetLevel(toZapLevel(l))
}

// Sync 刷新 zap 缓冲，程序退出前调用。
func (b *ZapBackend) Sync() error {
	return b.z.Sync()
}

// InternalLogger 返回底层 *zap.Logger，供需要直接操作 zap 的场景使用。
func (b *ZapBackend) InternalLogger() *zap.Logger {
	return b.z
}

// SugaredLogger 包装。

// zapSugared 包装 zap.SugaredLogger，实现 SugaredLogger 接口。
type zapSugared struct {
	s *zap.SugaredLogger
}

func (z *zapSugared) Debugf(format string, args ...any) { z.s.Debugf(format, args...) }
func (z *zapSugared) Infof(format string, args ...any)  { z.s.Infof(format, args...) }
func (z *zapSugared) Warnf(format string, args ...any)  { z.s.Warnf(format, args...) }
func (z *zapSugared) Errorf(format string, args ...any) { z.s.Errorf(format, args...) }
func (z *zapSugared) Fatalf(format string, args ...any) { z.s.Fatalf(format, args...) }

func (z *zapSugared) Debugw(msg string, kv ...any) { z.s.Debugw(msg, kv...) }
func (z *zapSugared) Infow(msg string, kv ...any)  { z.s.Infow(msg, kv...) }
func (z *zapSugared) Warnw(msg string, kv ...any)  { z.s.Warnw(msg, kv...) }
func (z *zapSugared) Errorw(msg string, kv ...any) { z.s.Errorw(msg, kv...) }
func (z *zapSugared) Fatalw(msg string, kv ...any) { z.s.Fatalw(msg, kv...) }

func (z *zapSugared) With(kv ...any) SugaredLogger {
	return &zapSugared{s: z.s.With(kv...)}
}

// Sugar 从 ZapBackend 创建 SugaredLogger。
func (b *ZapBackend) Sugar() SugaredLogger {
	return &zapSugared{s: b.z.Sugar()}
}

// Encoder 配置 - [time][level] [caller] 与 JSON。

func bracketEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:          "T",
		LevelKey:         "L",
		CallerKey:        "C",
		MessageKey:       "M",
		LineEnding:       zapcore.DefaultLineEnding,
		EncodeTime:       bracketTimeEncoder,
		EncodeLevel:      bracketLevelEncoder,
		EncodeCaller:     bracketCallerEncoder,
		EncodeDuration:   zapcore.StringDurationEncoder,
		ConsoleSeparator: " ",
	}
}

func bracketTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + t.Format("2006-01-02 15:04:05.000") + "]")
}

func bracketLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + l.String() + "]")
}

func bracketCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString("[" + trimCallerPath(caller.File, 3) + ":" + strconv.Itoa(caller.Line) + "]")
}

func jsonEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "T",
		LevelKey:       "L",
		CallerKey:      "C",
		MessageKey:     "M",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05.000"),
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeCaller:   jsonCallerEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}
}

func jsonCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(trimCallerPath(caller.File, 3) + ":" + strconv.Itoa(caller.Line))
}

func trimCallerPath(full string, depth int) string {
	full = filepath.ToSlash(full)
	parts := strings.Split(full, "/")
	if len(parts) <= depth {
		return full
	}
	return strings.Join(parts[len(parts)-depth:], "/")
}

// Level / Field → zap 类型映射。

func toZapLevel(l Level) zapcore.Level {
	switch l {
	case DebugLevel:
		return zapcore.DebugLevel
	case WarnLevel:
		return zapcore.WarnLevel
	case ErrorLevel:
		return zapcore.ErrorLevel
	case FatalLevel:
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}

func toZapFields(fields []Field) []zap.Field {
	if len(fields) == 0 {
		return nil
	}
	zfs := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		switch f.Type {
		case StringType:
			zfs = append(zfs, zap.String(f.Key, f.String))
		case Int64Type:
			zfs = append(zfs, zap.Int64(f.Key, f.Integer))
		case Int32Type:
			zfs = append(zfs, zap.Int32(f.Key, int32(f.Integer)))
		case Float64Type:
			zfs = append(zfs, zap.Float64(f.Key, f.Float))
		case BoolType:
			zfs = append(zfs, zap.Bool(f.Key, f.Integer == 1))
		case DurationType:
			zfs = append(zfs, zap.Duration(f.Key, time.Duration(f.Integer)))
		case TimeType:
			zfs = append(zfs, zap.String(f.Key, f.Interface.(time.Time).Format("2006-01-02 15:04:05.000")))
		case ErrorType:
			if f.Interface != nil {
				zfs = append(zfs, zap.NamedError(f.Key, f.Interface.(error)))
			}
		case AnyType:
			zfs = append(zfs, zap.Any(f.Key, f.Interface))
		}
	}
	return zfs
}
