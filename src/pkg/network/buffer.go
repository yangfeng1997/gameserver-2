//go:build linux

package network

import (
	"bytes"
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// prependBytes 预留前置空间，供协议帧头零拷贝塞回缓冲前端。
	prependBytes = 8
	// initialSize Buffer 初始可写容量，按需 2 倍扩容。
	initialSize = 1024
	// extraBufSize ReadFd 的栈上辅助缓冲，单次 readv 批量读取突发数据。
	extraBufSize = 1 << 16
	// readPlainMin 可写区不小于此值时走单次 unix.Read，省 readv 的 iovec 装配开销；
	// 可写区不足（buffer 近满，突发场景）才回退 readv 带上 extra 一次拿全。
	// 与 gnet/muduo 一致：常态单 read，突发 readv 兜底。
	readPlainMin = 1024
)

// Buffer muduo 式可前置 prependable 缓冲。
//
//	[ prepend | 已读区 | 可读区 | 可写区 ]
//	          ^ridx    ^widx           ^len
//
// 设计要点：
//   - 连续单段：局部性好，可读区即一段 []byte，零拷贝 Peek/Next。
//   - 可前置：未消费空间可回收做帧头前置写入，无额外分配。
//   - compact-before-grow：可写不足时优先把可读区压实到前端，避免无谓扩容拷贝；
//     仍不够再 2 倍扩容（均摊 O(1)）。
//   - ReadFd 用 readv 一次性读入 [可写区 + 栈辅助缓冲]，突发数据一次 syscall 拿全。
//   - WritevView 暴露可读区为 [][]byte，供 writev 聚合发送（v1 单段，预留多段扩展）。
//
// 突发溢出 linkedlist 作为调优阶段预留杠杆，v1 不启用——基准若落后于 gnet elastic 再上。
type Buffer struct {
	buf  []byte
	ridx int // 可读区起点
	widx int // 可写区起点
}

// NewBuffer 构造容量至少为 cap 的 Buffer；cap 过小取 initialSize。
func NewBuffer(cap int) *Buffer {
	if cap < initialSize {
		cap = initialSize
	}
	size := prependBytes + cap
	b := &Buffer{buf: make([]byte, size)}
	b.ridx = prependBytes
	b.widx = prependBytes
	return b
}

// --- 容量查询 ---

// ReadableBytes 可读字节数。
func (b *Buffer) ReadableBytes() int { return b.widx - b.ridx }

// WritableBytes 可写字节数（连续可写尾部）。
func (b *Buffer) WritableBytes() int { return len(b.buf) - b.widx }

// PrependableBytes 前置可塞字节数（即已读区可回收空间）。
func (b *Buffer) PrependableBytes() int { return b.ridx }

// Cap 底层切片总容量。
func (b *Buffer) Cap() int { return len(b.buf) }

// Len 可读字节数（ReadableBytes 别名，惯用）。
func (b *Buffer) Len() int { return b.ReadableBytes() }

// --- 读侧（零拷贝视图） ---

// Peek 返回可读区切片，不推进读指针。调用方据此解析协议帧。
func (b *Buffer) Peek() []byte { return b.buf[b.ridx:b.widx] }

// Next 零拷贝返回接下来 n 字节切片并推进读指针。
// 返回切片指向缓冲内部，缓冲变更前有效。n 超出可读则返回全部可读。
func (b *Buffer) Next(n int) []byte {
	if n > b.ReadableBytes() {
		n = b.ReadableBytes()
	}
	s := b.buf[b.ridx : b.ridx+n]
	b.Retrieve(n)
	return s
}

// Read 拷贝最多 len(p) 字节到 p，推进读指针，返回实际拷贝数。
func (b *Buffer) Read(p []byte) int {
	n := copy(p, b.Peek())
	b.Retrieve(n)
	return n
}

// Retrieve 推进读指针 n 字节；n>=可读则复位到初始前置位置。
func (b *Buffer) Retrieve(n int) {
	if n >= b.ReadableBytes() {
		b.RetrieveAll()
		return
	}
	b.ridx += n
}

// RetrieveAll 复位缓冲，可读区清空，读/写指针归位到前置位置，backing 复用不释放。
func (b *Buffer) RetrieveAll() {
	b.ridx = prependBytes
	b.widx = prependBytes
}

// RetrieveAsString 取出 n 字节为字符串并推进读指针。n 超出取全部。
func (b *Buffer) RetrieveAsString(n int) string {
	if n > b.ReadableBytes() {
		n = b.ReadableBytes()
	}
	s := string(b.Peek()[:n])
	b.Retrieve(n)
	return s
}

// Find 在可读区查找 delim 首次出现的偏移（相对可读区起点），未找到返回 -1。
func (b *Buffer) Find(delim []byte) int {
	return bytes.Index(b.Peek(), delim)
}

// PeekN 返回可读区前 n 字节的切片，不推进读指针；可读不足 n 返回 nil。
// 分帧友好：先看长度字段而不消费。返回切片指向缓冲内部，缓冲变更前有效。
func (b *Buffer) PeekN(n int) []byte {
	if n < 0 || n > b.ReadableBytes() {
		return nil
	}
	return b.buf[b.ridx : b.ridx+n]
}

// ReadUint32BE 读取 big-endian uint32 并推进读指针；可读不足 4 返回 0,false。
func (b *Buffer) ReadUint32BE() (uint32, bool) {
	if b.ReadableBytes() < 4 {
		return 0, false
	}
	v := binary.BigEndian.Uint32(b.buf[b.ridx:])
	b.Retrieve(4)
	return v, true
}

// PeekUint32BE 读取 big-endian uint32 但不推进读指针；可读不足 4 返回 0,false。
func (b *Buffer) PeekUint32BE() (uint32, bool) {
	if b.ReadableBytes() < 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(b.buf[b.ridx:]), true
}

// ReadUint16BE 读取 big-endian uint16 并推进读指针；可读不足 2 返回 0,false。
func (b *Buffer) ReadUint16BE() (uint16, bool) {
	if b.ReadableBytes() < 2 {
		return 0, false
	}
	v := binary.BigEndian.Uint16(b.buf[b.ridx:])
	b.Retrieve(2)
	return v, true
}

// PrependUint32BE 把 v 以 big-endian 前置到可读区之前（帧头长度回填，零扩容）。
// 前置空间不足返回 false。
func (b *Buffer) PrependUint32BE(v uint32) bool {
	return b.Prepend([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// --- 写侧 ---

// Append 追加数据，必要时 compact 或扩容。
func (b *Buffer) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	b.ensureWritable(len(data))
	b.widx += copy(b.buf[b.widx:], data)
}

// AppendString 追加字符串，零拷贝（unsafe，不分配）。
func (b *Buffer) AppendString(s string) {
	if len(s) == 0 {
		return
	}
	b.Append(stringToBytes(s))
}

// Prepend 将 data 前置到可读区之前。要求前置空间足够（典型帧头长度小）。
// 空间不足返回 false，调用方需自行处理。
func (b *Buffer) Prepend(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	if b.PrependableBytes() < len(data) {
		return false
	}
	b.ridx -= len(data)
	copy(b.buf[b.ridx:], data)
	return true
}

// BeginWrite 返回可写区切片，供直接写入后配合 HasWritten 提交。
func (b *Buffer) BeginWrite() []byte { return b.buf[b.widx:] }

// HasWritten 提交直接写入 BeginWrite 的 n 字节，推进写指针。
func (b *Buffer) HasWritten(n int) { b.widx += n }

// ensureWritable 保证可写区至少 n 字节。优先压实可读区，不够再扩容。
func (b *Buffer) ensureWritable(n int) {
	if b.WritableBytes() >= n {
		return
	}
	// 已读区 + 可写区 够装：压实可读区到前端。
	if b.PrependableBytes()+b.WritableBytes() >= n+prependBytes {
		readable := b.ReadableBytes()
		copy(b.buf[prependBytes:], b.buf[b.ridx:b.widx])
		b.ridx = prependBytes
		b.widx = prependBytes + readable
		return
	}
	// 扩容：2 倍直到够装。
	readable := b.ReadableBytes()
	need := prependBytes + readable + n
	newCap := len(b.buf)
	for newCap < need {
		newCap *= 2
	}
	grown := make([]byte, newCap)
	copy(grown[prependBytes:], b.buf[b.ridx:b.widx])
	b.buf = grown
	b.ridx = prependBytes
	b.widx = prependBytes + readable
}

// --- 系统 I/O ---

// ReadFd 读 fd 数据入可写区。可写区够用（≥readPlainMin）走单次 unix.Read，
// 零 iovec 开销（常态）；可写区不足（buffer 近满，突发场景）回退 readv
// 一次性读入 [可写区 + extra 辅助缓冲]，突发一次 syscall 拿全。
// extra 与 iov 由调用方提供可复用缓冲，热路径零分配。
// 返回读取字节数。EAGAIN/EWOULDBLOCK 由调用方按 err 处理。
func (b *Buffer) ReadFd(fd int, extra []byte, iov [][]byte) (int, error) {
	w := b.buf[b.widx:]
	if len(w) >= readPlainMin {
		// 常态：可写区够用，单次 read。
		n, err := unix.Read(fd, w)
		if n > 0 {
			b.widx += n
		}
		return n, err
	}
	// 突发：可写区不足，readv 一次性读入 [可写区 + extra]。
	iov[0] = w
	iov[1] = extra
	n, err := unix.Readv(fd, iov)
	if n <= 0 {
		return n, err
	}
	if n <= len(w) {
		// 仅可写区被填充。
		b.widx += n
	} else {
		// 可写区填满，余量在 extra。
		b.widx = len(b.buf)
		b.Append(extra[:n-len(w)])
	}
	return n, nil
}

// WritevView 返回可读区的 iovec 视图，供 writev 聚合发送。
// v1 单段（连续可读区）；预留多段（突发溢出 linkedlist）扩展位。
func (b *Buffer) WritevView() [][]byte {
	return b.WritevViewBuf(nil)
}

// WritevViewBuf 复用传入的 iov 切片，避免热路径分配。
func (b *Buffer) WritevViewBuf(iov [][]byte) [][]byte {
	view := b.Peek()
	if len(view) == 0 {
		return iov[:0]
	}
	return append(iov, view)
}

// stringToBytes 零拷贝 string→[]byte，仅用于只读追加场景（不修改底层）。
func stringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
