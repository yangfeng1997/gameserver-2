//go:build linux

package network

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// family 地址族，值存储免接口装箱。
type family uint8

const (
	famInvalid family = iota
	famIPv4
	famIPv6
	famUnix
)

// Address 传输无关地址，覆盖 IPv4/IPv6 与 Unix domain socket。
//
// 采用值存储而非 unix.Sockaddr 接口，使 accept 热路径构造 Address 零分配：
// 从 unix.Accept4 返回的 Sockaddr 字段拷出即可。Sockaddr() 仅在 bind/connect
// （启动或拨号路径）构造一次具体 Sockaddr，可接受单次小分配。
type Address struct {
	fam  family
	port int      // IPv4/IPv6 用
	ip4  [4]byte  // IPv4 网络序
	ip6  [16]byte // IPv6 网络序
	zone uint32   // IPv6 scope_id（链路本地用，否则 0）
	path string   // Unix 套接字路径
}

// NewIPv4 从 4 字节 IP 与端口构造。ip 长度必须为 4（网络序）。
func NewIPv4(ip net.IP, port int) Address {
	var a Address
	a.fam = famIPv4
	a.port = port
	copy(a.ip4[:], ip.To4())
	return a
}

// NewIPv6 从 16 字节 IP 与端口构造。zone 为 scope_id。
func NewIPv6(ip net.IP, port int, zone uint32) Address {
	var a Address
	a.fam = famIPv6
	a.port = port
	a.zone = zone
	copy(a.ip6[:], ip.To16())
	return a
}

// NewUnix 从路径构造 Unix domain socket 地址。
func NewUnix(path string) Address {
	return Address{fam: famUnix, path: path}
}

// zoneToScope 将 net.TCPAddr.Zone（字符串，接口名或数字）解析为 IPv6 scope_id。
// 空字符串返回 0（全局地址）。
func zoneToScope(zone string) uint32 {
	if zone == "" {
		return 0
	}
	if n, err := strconv.ParseUint(zone, 10, 32); err == nil {
		return uint32(n)
	}
	if iface, err := net.InterfaceByName(zone); err == nil {
		return uint32(iface.Index)
	}
	return 0
}

// ParseAddress 按 network 解析 address 字符串。
//
//	network: "tcp" | "tcp4" | "tcp6" | "unix"
//	address: "host:port"（TCP）或路径（Unix）
//
// 启动/拨号路径使用，允许主机名解析。返回规范 network 与 Address。
func ParseAddress(network, address string) (string, Address, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		ta, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return network, Address{}, err
		}
		if ip4 := ta.IP.To4(); ip4 != nil && network != "tcp6" {
			return network, NewIPv4(ip4, ta.Port), nil
		}
		return network, NewIPv6(ta.IP.To16(), ta.Port, zoneToScope(ta.Zone)), nil
	case "unix":
		ua, err := net.ResolveUnixAddr(network, address)
		if err != nil {
			return network, Address{}, err
		}
		return network, NewUnix(ua.Name), nil
	default:
		return network, Address{}, fmt.Errorf("%w: %s", ErrInvalidNetwork, network)
	}
}

// fromSockaddr 从 unix.Accept4 返回的 Sockaddr 构造 Address，热路径零分配。
func fromSockaddr(sa unix.Sockaddr) Address {
	switch v := sa.(type) {
	case *unix.SockaddrInet4:
		var a Address
		a.fam = famIPv4
		a.port = v.Port
		copy(a.ip4[:], v.Addr[:])
		return a
	case *unix.SockaddrInet6:
		var a Address
		a.fam = famIPv6
		a.port = v.Port
		copy(a.ip6[:], v.Addr[:])
		a.zone = v.ZoneId
		return a
	case *unix.SockaddrUnix:
		return Address{fam: famUnix, path: v.Name}
	}
	return Address{}
}

// Sockaddr 构造具体 Sockaddr 供 bind/connect 使用。仅在非热路径调用。
func (a Address) Sockaddr() (unix.Sockaddr, error) {
	switch a.fam {
	case famIPv4:
		sa := &unix.SockaddrInet4{Port: a.port}
		copy(sa.Addr[:], a.ip4[:])
		return sa, nil
	case famIPv6:
		sa := &unix.SockaddrInet6{Port: a.port, ZoneId: a.zone}
		copy(sa.Addr[:], a.ip6[:])
		return sa, nil
	case famUnix:
		// 路径最长 unix.UNIX_PATH_MAX；过长则报错。
		return &unix.SockaddrUnix{Name: a.path}, nil
	}
	return nil, ErrInvalidAddress
}

// domain 返回 socket(2) 域，供 socket 创建使用。
func (a Address) domain() int {
	switch a.fam {
	case famIPv4:
		return unix.AF_INET
	case famIPv6:
		return unix.AF_INET6
	case famUnix:
		return unix.AF_UNIX
	}
	return unix.AF_INET
}

// sockType 返回 socket 类型；本库仅流式，故 SOCK_STREAM。
func (a Address) sockType() int { return unix.SOCK_STREAM }

// Network 返回规范网络名。
func (a Address) Network() string {
	switch a.fam {
	case famIPv4:
		return "tcp4"
	case famIPv6:
		return "tcp6"
	case famUnix:
		return "unix"
	}
	return "tcp"
}

// IsIPv4 / IsIPv6 / IsUnix 地址族判定。
func (a Address) IsIPv4() bool { return a.fam == famIPv4 }
func (a Address) IsIPv6() bool { return a.fam == famIPv6 }
func (a Address) IsUnix() bool { return a.fam == famUnix }

// Port 返回端口；Unix 地址返回 0。
func (a Address) Port() int { return a.port }

// IP 返回 net.IP 副本（仅 IPv4/IPv6）；Unix 返回 nil。
func (a Address) IP() net.IP {
	switch a.fam {
	case famIPv4:
		ip := make(net.IP, 4)
		copy(ip, a.ip4[:])
		return ip
	case famIPv6:
		ip := make(net.IP, 16)
		copy(ip, a.ip6[:])
		return ip
	}
	return nil
}

// String 返回可读形式："1.2.3.4:80"、"[::1]:80"、"unix:///path"。
func (a Address) String() string {
	switch a.fam {
	case famIPv4:
		return net.IP(a.ip4[:]).String() + ":" + strconv.Itoa(a.port)
	case famIPv6:
		return "[" + net.IP(a.ip6[:]).String() + "]:" + strconv.Itoa(a.port)
	case famUnix:
		return "unix://" + a.path
	}
	return "<invalid>"
}
