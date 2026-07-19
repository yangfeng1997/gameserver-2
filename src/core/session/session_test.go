package session

import "testing"

type fakeConn struct{ addr string }

func (f fakeConn) RemoteAddr() string { return f.addr }

func TestSessionManagerOnConnect(t *testing.T) {
	m := NewSessionManager()
	conn := fakeConn{addr: "127.0.0.1:12345"}
	sess := m.OnConnect(conn)
	if sess == nil {
		t.Fatal("expected non-nil session")
	}
	if sess.ConnID != conn.addr {
		t.Errorf("ConnID=%q, want %q", sess.ConnID, conn.addr)
	}

	got := m.GetByConnID(conn.addr)
	if got != sess {
		t.Error("GetByConnID should return the session")
	}
}

func TestSessionManagerOnDisconnect(t *testing.T) {
	m := NewSessionManager()
	conn := fakeConn{addr: "10.0.0.1:80"}
	sess := m.OnConnect(conn)
	sess.UID = 100
	m.byUID[100] = sess

	m.OnDisconnect(conn)
	if m.GetByConnID(conn.addr) != nil {
		t.Error("session should be removed after disconnect")
	}
	if m.GetByUID(100) != nil {
		t.Error("uid should be removed after disconnect")
	}
}

func TestSessionManagerBindSession(t *testing.T) {
	m := NewSessionManager()
	conn := fakeConn{addr: "10.0.0.1:80"}
	sess := m.OnConnect(conn)

	m.BindSession(conn.addr, 200, map[uint32]uint32{2: 42})
	if sess.UID != 200 {
		t.Errorf("uid=%d, want 200", sess.UID)
	}
	if !sess.Authed {
		t.Error("should be authed after bind")
	}
	if sess.BoundNodes[2] != 42 {
		t.Errorf("bound[2]=%d, want 42", sess.BoundNodes[2])
	}
}

func TestSessionManagerGetByUID(t *testing.T) {
	m := NewSessionManager()
	conn := fakeConn{addr: "10.0.0.1:80"}
	m.OnConnect(conn)
	m.BindSession(conn.addr, 300, nil)

	got := m.GetByUID(300)
	if got == nil || got.UID != 300 {
		t.Fatal("GetByUID should return the session")
	}
}
