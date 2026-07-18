package logger

// Logger 业务代码依赖的核心接口，参数全部强类型，无 interface{} 可变参数
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)

	// Fatal 记录日志后调用 os.Exit(1)，程序终止。
	// 仅限启动阶段/不可恢复错误使用；业务逻辑用 Error。
	Fatal(msg string, fields ...Field)

	// With 派生子 Logger，携带固定字段（如 roomID、playerID）
	With(fields ...Field) Logger

	// IsEnabled 热路径 level 检查，避免构造昂贵字段后发现级别未开启
	IsEnabled(level Level) bool
}
