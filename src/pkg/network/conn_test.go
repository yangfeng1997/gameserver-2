//go:build linux

package network_test

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"project/src/pkg/network"
)

func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// dialWithRetry 在服务端 acceptor 就绪前重试拨号。
func dialWithRetry(t testing.TB, networkStr, addr string, cli *network.Client) *network.Conn {
	t.Helper()
	var (
		conn *network.Conn
		err  error
	)
	for range 50 {
		conn, err = cli.Dial(networkStr, addr)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s://%s: %v", networkStr, addr, err)
	return nil
}

func newEchoServer(t *testing.T) (*network.Server, string) {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv, err := network.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.AddAddress("tcp", addr); err != nil {
		t.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		c.Send(in.Peek())
		in.RetrieveAll()
	})
	go srv.Run()
	return srv, addr
}

func TestEchoTCP(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	received := make(chan []byte, 8)
	cli := network.NewClient()
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		b := append([]byte(nil), in.Peek()...)
		in.RetrieveAll()
		received <- b
	})
	conn := dialWithRetry(t, "tcp", addr, cli)
	defer conn.Close()

	payload := []byte("hello-network-echo")
	conn.AsyncSend(payload)

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Fatalf("got %q want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for echo")
	}
}

func TestEchoUDS(t *testing.T) {
	path := fmt.Sprintf("/tmp/network-echo-uds-%d", time.Now().UnixNano())
	srv, err := network.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.AddAddress("unix", path); err != nil {
		t.Fatal(err)
	}
	srv.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		c.Send(in.Peek())
		in.RetrieveAll()
	})
	go srv.Run()
	defer srv.Stop()

	received := make(chan []byte, 8)
	cli := network.NewClient()
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		b := append([]byte(nil), in.Peek()...)
		in.RetrieveAll()
		received <- b
	})
	conn := dialWithRetry(t, "unix", path, cli)
	defer conn.Close()

	payload := []byte("uds-echo")
	conn.AsyncSend(payload)

	select {
	case got := <-received:
		if !bytes.Equal(got, payload) {
			t.Fatalf("got %q want %q", got, payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for uds echo")
	}
}

func TestEchoMultiple(t *testing.T) {
	srv, addr := newEchoServer(t)
	defer srv.Stop()

	const n = 100
	payload := []byte("ping")
	want := bytes.Repeat(payload, n)

	cli := network.NewClient()
	done := make(chan struct{})
	var got []byte
	cli.SetOnMessage(func(c *network.Conn, in *network.Buffer) {
		got = append(got, in.Peek()...)
		in.RetrieveAll()
		if len(got) >= len(want) {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	conn := dialWithRetry(t, "tcp", addr, cli)
	defer conn.Close()

	for range n {
		conn.AsyncSend(payload)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout, got %d/%d bytes", len(got), len(want))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(want))
	}
}
