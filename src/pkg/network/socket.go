//go:build linux

package network

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// socket 创建非阻塞、close-on-exec 的流式 socket。
// SOCK_NONBLOCK|SOCK_CLOEXEC 原子置位，避免 accept 后再 fcntl 的额外系统调用。
func newSocket(domain int) (int, error) {
	return unix.Socket(domain, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
}

// setsockoptInt 设置 int 型 socket 选项，失败忽略（可选项不致命）。
func setsockoptInt(fd, level, opt, val int) error {
	return unix.SetsockoptInt(fd, level, opt, val)
}

// applyCommonOpts 应用收发缓冲与 reuse 选项（listen 与 connect 共用）。
func applyCommonOpts(fd int, o Options) {
	if o.ReuseAddr {
		_ = setsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	}
	if o.ReusePort {
		_ = setsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}
	if o.SocketRecvBuffer > 0 {
		_ = setsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, o.SocketRecvBuffer)
	}
	if o.SocketSendBuffer > 0 {
		_ = setsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF, o.SocketSendBuffer)
	}
}

// applyConnOpts 应用连接级 TCP 选项（Nagle/keepalive）。
func applyConnOpts(fd int, o Options) {
	if o.TCPNoDelay == TCPNoDelay {
		_ = setsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1)
	}
	if o.TCPKeepAlive > 0 {
		_ = setsockoptInt(fd, unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1)
		_ = setsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_KEEPIDLE, int(o.TCPKeepAlive/1e9))
		if o.TCPKeepInterval > 0 {
			_ = setsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_KEEPINTVL, int(o.TCPKeepInterval/1e9))
		}
		if o.TCPKeepCount > 0 {
			_ = setsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_KEEPCNT, o.TCPKeepCount)
		}
	}
}

// maxBacklog 读取 /proc/sys/net/core/somaxconn，失败回退 SOMAXCONN。
func maxBacklog() int {
	b, err := os.ReadFile("/proc/sys/net/core/somaxconn")
	if err != nil {
		return unix.SOMAXCONN
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n <= 0 {
		return unix.SOMAXCONN
	}
	return n
}

// createListener 创建并 bind/listen 一个非阻塞监听 socket。
// network 仅用于 IPv6 v6only 决策与 Unix 路径清理。
func createListener(network string, addr Address, o Options) (int, error) {
	fd, err := newSocket(addr.domain())
	if err != nil {
		return -1, err
	}

	// IPv6：仅 "tcp6" 显式置 v6only；"tcp"/"tcp4" 不动（默认双栈或 IPv4）。
	if addr.IsIPv6() && network == "tcp6" {
		_ = setsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 1)
	}

	applyCommonOpts(fd, o)

	// Unix：bind 前清理可能残留的路径文件，避免 EADDRINUSE。
	if addr.IsUnix() {
		_ = os.Remove(addr.path)
	}

	sa, err := addr.Sockaddr()
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err = unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err = unix.Listen(fd, maxBacklog()); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// createConnector 创建非阻塞 socket 并发起非阻塞 connect。
// 返回的 fd 处于"拨号中"状态；err 为 nil 表示立即连上，err==EINPROGRESS 表示进行中，
// 调用方据 EINPROGRESS 注册 EPOLLOUT 等待就绪后再校验 SO_ERROR。
func createConnector(addr Address, o Options) (int, error) {
	fd, err := newSocket(addr.domain())
	if err != nil {
		return -1, err
	}
	applyCommonOpts(fd, o)
	applyConnOpts(fd, o)

	sa, err := addr.Sockaddr()
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if err = unix.Connect(fd, sa); err != nil && err != unix.EINPROGRESS {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// acceptConnection 在监听 fd 上非阻塞 accept，返回新连接 fd + 对端地址。
// SOCK_NONBLOCK|SOCK_CLOEXEC 原子置位，省去 accept 后 fcntl。
func acceptConnection(listenFd int) (int, Address, error) {
	nfd, sa, err := unix.Accept4(listenFd, unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
	if err != nil {
		return -1, Address{}, err
	}
	return nfd, fromSockaddr(sa), nil
}

// connectError 在非阻塞 connect 就绪后读取 SO_ERROR 判断成败。
func connectError(fd int) error {
	v, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
	if err != nil {
		return err
	}
	if v == 0 {
		return nil
	}
	return unix.Errno(v)
}

// peerName 获取 fd 的对端地址（已连接后），用于 dial 成功记录对端信息。
func peerName(fd int) (Address, error) {
	sa, err := unix.Getpeername(fd)
	if err != nil {
		return Address{}, err
	}
	return fromSockaddr(sa), nil
}

// sockName 获取 fd 的本地地址。
func sockName(fd int) (Address, error) {
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return Address{}, err
	}
	return fromSockaddr(sa), nil
}
