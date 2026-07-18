// Package network 是一套面向 GameServerPro 的高性能事件驱动网络库。
//
// 设计参考 muduo 的 one-loop-per-thread 反应堆模型与 gnet 的性能优化手段，
// 但按 Go 的语言特性重新组织，不照搬任意一方。
//
// 核心抽象（与 muduo/Go 生态行业名词对齐）：
//
//   - EventLoop：单线程事件循环，所有 I/O 与回调在其上串行执行
//   - EventLoopGroup：sub-reactor 线程池，按策略分发新连接
//   - Poller：epoll 多路复用封装（Linux）
//   - Channel：fd + 关心事件 + 就绪回调，epoll 的操作单位
//   - Address：传输无关地址，覆盖 IPv4/IPv6 与 Unix 路径
//   - Buffer：muduo 式可前置 prependable 缓冲，配合 writev 聚合写
//   - Conn：连接级状态机 + input/output Buffer + 用户回调（TCP 与 stream UDS 通用）
//   - Acceptor：监听 fd，accept 循环后回调上层建立 Conn
//   - Connector/Client：主动拨号与重试
//   - Server：mainLoop + subLoopPool + Acceptor + 连接表的高层封装
//
// 性能要点：直接 syscall 绕过 net.Conn；每 loop 复用读缓冲；
// eventfd + 无锁任务队列做跨线程投递；timerfd + 时间轮做定时器；
// sync.Pool 复用 Buffer/Channel/Task，压低 GC 压力；
// 可选 SO_REUSEPORT 让内核分发 accept；可选边沿触发。
//
// 日志：库自带 Logger 接口（gnet 风格），默认 stdlib 实现，WithLogger 注入，
// 不耦合业务日志实现。
package network
