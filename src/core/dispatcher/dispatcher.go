package dispatcher

import (
	"fmt"

	"project/src/core/codec"
	"project/src/core/errcode"
	"project/src/core/session"
)

// RouteEntry 路由条目：cmdID → 目标服务类型 + route 字符串 + 响应 cmdID。
type RouteEntry struct {
	CmdID      uint32
	ServerType uint32
	Route      string
	RspCmdID   uint32
}

// HandlerFunc 本地消息处理函数。
type HandlerFunc func(*session.Session, *codec.Message) error

// Middleware 消息处理中间件。
type Middleware func(HandlerFunc) HandlerFunc

// ForwardFunc 转发消息到其他服务的函数。
type ForwardFunc func(*session.Session, *codec.Message, RouteEntry) error

// Dispatcher 消息分发器：路由表 → 本地 handler 或转发。
type Dispatcher struct {
	selfServerType uint32
	routes         map[uint32]RouteEntry
	handlers       map[uint32]HandlerFunc
	middlewares    []Middleware
	forward        ForwardFunc
}

// New 创建分发器。
func New(selfServerType uint32) *Dispatcher {
	return &Dispatcher{
		selfServerType: selfServerType,
		routes:         make(map[uint32]RouteEntry),
		handlers:       make(map[uint32]HandlerFunc),
	}
}

// RegisterRoute 注册路由条目。
func (d *Dispatcher) RegisterRoute(cmdID uint32, entry RouteEntry) {
	d.routes[cmdID] = entry
}

// RegisterHandler 注册本地处理函数。
func (d *Dispatcher) RegisterHandler(cmdID uint32, fn HandlerFunc) {
	d.handlers[cmdID] = fn
}

// Use 添加中间件（先进后出：最后一个最先执行）。
func (d *Dispatcher) Use(m Middleware) { d.middlewares = append(d.middlewares, m) }

// SetForward 设置转发函数。
func (d *Dispatcher) SetForward(fn ForwardFunc) { d.forward = fn }

// Dispatch 分发消息。
func (d *Dispatcher) Dispatch(sess *session.Session, msg *codec.Message) error {
	if msg == nil {
		return errcode.New(errcode.ERR_UNMARSHAL, "nil message")
	}

	handler := d.handlers[msg.CmdID]
	entry, hasRoute := d.routes[msg.CmdID]
	if hasRoute && entry.ServerType != d.selfServerType {
		if d.forward == nil {
			return errcode.New(errcode.ERR_NO_ROUTE, "forward handler not set")
		}
		return d.forward(sess, msg, entry)
	}
	if handler == nil {
		if hasRoute {
			return errcode.New(errcode.ERR_NO_ROUTE, fmt.Sprintf("handler not found for cmd %d", msg.CmdID))
		}
		return errcode.New(errcode.ERR_NO_ROUTE, fmt.Sprintf("route not found for cmd %d", msg.CmdID))
	}
	chain := handler
	for i := len(d.middlewares) - 1; i >= 0; i-- {
		chain = d.middlewares[i](chain)
	}
	return chain(sess, msg)
}
