package logger

// SugaredLogger 便捷日志接口，适合低频路径（启动、关闭、错误处理）
//
// 性能参考（zap benchmark，仅供量级参考）
// - Debug/Info/Error（强类型 Field）：~100 ns，0 alloc  → 高频热路径首选
// - Debugw/Infow（松散 kv）         ：~200 ns，1 alloc  → 低频路径，key-value 结构化输出
// - Debugf/Infof（printf 格式化）   ：~300 ns，2 alloc  → 低频路径，拼接描述性文字
//
// 注意：Debugw/Infow 的 keysAndValues 必须严格 key-value 交替，key 必须是 string
// 否则 zap 运行时补 !BADKEY，不会编译报错
type SugaredLogger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)

	Debugw(msg string, keysAndValues ...any)
	Infow(msg string, keysAndValues ...any)
	Warnw(msg string, keysAndValues ...any)
	Errorw(msg string, keysAndValues ...any)
	Fatalw(msg string, keysAndValues ...any)

	// With 派生子 SugaredLogger，绑定固定 kv 字段
	With(keysAndValues ...any) SugaredLogger
}

// Sugar 获取全局 SugaredLogger。
// 若全局 Logger 底层不支持 Sugar，返回 nopSugared（不输出）。
func Sugar() SugaredLogger {
	if sp, ok := L().(interface{ Sugar() SugaredLogger }); ok {
		return sp.Sugar()
	}
	return nopSugared{}
}

// SugarOf 返回指定 Logger 实例的 SugaredLogger。
// 与 Sugar()（只取全局）不同，此函数取任意实例——典型用于：
//
//	netSugar := logger.SugarOf(logger.L().With(logger.String("component", "network")))
//	network.WithLogger(netSugar)  // 网络库每条日志自动带 component=network
//
// Logger 接口刻意不声明 Sugar()（保持与 Backend 无关），故经类型断言取；
// adapterLogger（New/NewZapFileLogger/With 返回的具体类型）支持 Sugar()，且
// 派生（With）logger 的 sugar 继承其绑定字段。若该 Logger 底层不支持 sugar，
// 返回 nopSugared（不输出），不 panic。
func SugarOf(l Logger) SugaredLogger {
	if sp, ok := l.(interface{ Sugar() SugaredLogger }); ok {
		return sp.Sugar()
	}
	return nopSugared{}
}

// nopSugared 空实现，Sugar() 的兜底返回值。
type nopSugared struct{}

func (nopSugared) Debugf(_ string, _ ...any)   {}
func (nopSugared) Infof(_ string, _ ...any)    {}
func (nopSugared) Warnf(_ string, _ ...any)    {}
func (nopSugared) Errorf(_ string, _ ...any)   {}
func (nopSugared) Fatalf(_ string, _ ...any)   {}
func (nopSugared) Debugw(_ string, _ ...any)   {}
func (nopSugared) Infow(_ string, _ ...any)    {}
func (nopSugared) Warnw(_ string, _ ...any)    {}
func (nopSugared) Errorw(_ string, _ ...any)   {}
func (nopSugared) Fatalw(_ string, _ ...any)   {}
func (nopSugared) With(_ ...any) SugaredLogger { return nopSugared{} }
