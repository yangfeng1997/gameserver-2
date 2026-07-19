package rpc

import "sync/atomic"

var defaultCore atomic.Pointer[Core]

// Init 设置包级默认 RPC 引擎。
func Init(core *Core) {
	defaultCore.Store(core)
}

// Default 返回默认 RPC 引擎。
func Default() *Core {
	return defaultCore.Load()
}

// MustDefault 返回默认 RPC 引擎，未初始化则 panic。
func MustDefault() *Core {
	core := Default()
	if core == nil {
		panic("rpc: default core not initialized")
	}
	return core
}

// Ready 返回默认 Core 是否已初始化。
func Ready() bool {
	return Default() != nil
}
