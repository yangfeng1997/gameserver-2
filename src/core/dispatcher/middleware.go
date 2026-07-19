package dispatcher

import (
	"fmt"
	"runtime/debug"

	"project/src/core/codec"
	"project/src/core/errcode"
	"project/src/core/session"
)

// RecoverMiddleware 捕获 handler panic 并转为 ERR_INTERNAL。
func RecoverMiddleware() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(sess *session.Session, msg *codec.Message) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = errcode.New(errcode.ERR_INTERNAL,
						fmt.Sprintf("panic recovered: %v\n%s", r, string(debug.Stack())))
				}
			}()
			return next(sess, msg)
		}
	}
}

// AuthMiddleware 未认证会话拦截，白名单中的 cmdID 免鉴权。
func AuthMiddleware(whitelist map[uint32]bool) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(sess *session.Session, msg *codec.Message) error {
			if msg == nil {
				return errcode.New(errcode.ERR_UNMARSHAL, "nil message")
			}
			if whitelist[msg.CmdID] {
				return next(sess, msg)
			}
			if sess == nil || !sess.Authed {
				return errcode.New(errcode.ERR_UNAUTHED, "session not authenticated")
			}
			return next(sess, msg)
		}
	}
}
