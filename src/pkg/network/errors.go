package network

import "errors"

// 库内 sentinel 错误。错误携带语义而非堆栈，调用方按值判等。
var (
	// ErrServerClosed 服务器已关闭/正在关闭，Stop 或 Run 返回。
	ErrServerClosed = errors.New("network: server closed")

	// ErrClientClosed 客户端已关闭。
	ErrClientClosed = errors.New("network: client closed")

	// ErrConnectionClosed 连接已关闭，对其操作返回。
	ErrConnectionClosed = errors.New("network: connection closed")

	// ErrConnectionRefused 对端拒绝或拨号失败。
	ErrConnectionRefused = errors.New("network: connection refused")

	// ErrShutdown 引擎进入关闭流程，连接被主动断开。
	ErrShutdown = errors.New("network: engine shutdown")

	// ErrInvalidNetwork 不支持的网络类型（仅 tcp/tcp4/tcp6/unix）。
	ErrInvalidNetwork = errors.New("network: invalid network")

	// ErrInvalidAddress 地址格式或解析失败。
	ErrInvalidAddress = errors.New("network: invalid address")

	// ErrBufferTooLarge 单次读写超出缓冲容量上限。
	ErrBufferTooLarge = errors.New("network: buffer too large")
)
