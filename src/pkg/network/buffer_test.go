//go:build linux

package network

import (
	"bytes"
	"testing"
)

func TestBufferAppendRetrieve(t *testing.T) {
	b := NewBuffer(0)
	b.AppendString("hello")
	b.AppendString(" world")
	if got := b.ReadableBytes(); got != 11 {
		t.Fatalf("readable=%d want 11", got)
	}
	got := b.RetrieveAsString(5)
	if got != "hello" {
		t.Fatalf("got %q want hello", got)
	}
	if b.ReadableBytes() != 6 {
		t.Fatalf("after retrieve 5, readable=%d want 6", b.ReadableBytes())
	}
	b.RetrieveAll()
	if b.ReadableBytes() != 0 {
		t.Fatalf("after retrieveAll, readable=%d want 0", b.ReadableBytes())
	}
}

func TestBufferNext(t *testing.T) {
	b := NewBuffer(0)
	b.AppendString("abcdef")
	got := b.Next(3)
	if !bytes.Equal(got, []byte("abc")) {
		t.Fatalf("got %q want abc", got)
	}
	got = b.Next(100) // 超出取剩余
	if !bytes.Equal(got, []byte("def")) {
		t.Fatalf("got %q want def", got)
	}
	if b.ReadableBytes() != 0 {
		t.Fatalf("readable=%d want 0", b.ReadableBytes())
	}
}

func TestBufferPrepend(t *testing.T) {
	b := NewBuffer(0)
	b.AppendString("world")
	if !b.Prepend([]byte("hello ")) {
		t.Fatal("prepend failed")
	}
	got := b.RetrieveAsString(11)
	if got != "hello world" {
		t.Fatalf("got %q want hello world", got)
	}
}

func TestBufferFind(t *testing.T) {
	b := NewBuffer(0)
	b.AppendString("GET /index HTTP/1.1\r\nHost: a\r\n\r\n")
	idx := b.Find([]byte("\r\n"))
	if idx != 19 {
		t.Fatalf("find \\r\\n idx=%d want 19", idx)
	}
	if b.Find([]byte("XYZ")) != -1 {
		t.Fatal("find missing should be -1")
	}
}

func TestBufferCompactBeforeGrow(t *testing.T) {
	b := NewBuffer(0)
	// 写满并消费，反复触发 compact 而非扩容。
	chunk := bytes.Repeat([]byte("x"), 512)
	for range 100 {
		b.Append(chunk)
		b.RetrieveAll()
	}
	b.AppendString("tail")
	if got := b.RetrieveAsString(4); got != "tail" {
		t.Fatalf("got %q want tail", got)
	}
	// 容量应未爆炸（compact 生效）。
	if b.Cap() > initialSize+prependBytes {
		t.Fatalf("cap grew unexpectedly to %d", b.Cap())
	}
}

func TestBufferWritevView(t *testing.T) {
	b := NewBuffer(0)
	b.AppendString("abc")
	view := b.WritevView()
	if len(view) != 1 || !bytes.Equal(view[0], []byte("abc")) {
		t.Fatalf("view=%v want [abc]", view)
	}
}
