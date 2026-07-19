package session

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Connection is the minimal connection interface session needs.
type Connection interface {
	RemoteAddr() string
}

// Session 连接上的玩家会话。
type Session struct {
	ID         string
	UID        int64
	ConnID     string
	Conn       Connection
	Authed     bool
	BoundNodes map[uint32]uint32
}

// SetBound 绑定指定 serverType 的 nodeID。
func (s *Session) SetBound(serverType, nodeID uint32) {
	if s.BoundNodes == nil {
		s.BoundNodes = make(map[uint32]uint32)
	}
	s.BoundNodes[serverType] = nodeID
}

// SetAuthed 设置认证状态。
func (s *Session) SetAuthed(authed bool) {
	s.Authed = authed
}

// SessionManager 会话管理器，按 ConnID + UID 双索引。
type SessionManager struct {
	mu     sync.RWMutex
	seq    atomic.Uint64
	byConn map[string]*Session
	byUID  map[int64]*Session
}

// NewSessionManager 创建会话管理器。
func NewSessionManager() *SessionManager {
	return &SessionManager{
		byConn: make(map[string]*Session),
		byUID:  make(map[int64]*Session),
	}
}

// OnConnect 创建新会话。
func (m *SessionManager) OnConnect(c Connection) *Session {
	if c == nil {
		return nil
	}
	sess := &Session{
		ID:         fmt.Sprintf("%d-%s", m.seq.Add(1), c.RemoteAddr()),
		ConnID:     c.RemoteAddr(),
		Conn:       c,
		BoundNodes: make(map[uint32]uint32),
	}
	m.mu.Lock()
	m.byConn[sess.ConnID] = sess
	m.mu.Unlock()
	return sess
}

// OnDisconnect 断开时清理会话。
func (m *SessionManager) OnDisconnect(c Connection) {
	if c == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.byConn[c.RemoteAddr()]
	if !ok {
		return
	}
	delete(m.byConn, sess.ConnID)
	if sess.UID != 0 {
		delete(m.byUID, sess.UID)
	}
}

// OnTimeout 超时清理。
func (m *SessionManager) OnTimeout(c Connection) { m.OnDisconnect(c) }

// BindSession 绑定 UID 和亲和节点到连接。
func (m *SessionManager) BindSession(connID string, uid int64, bound map[uint32]uint32) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.byConn[connID]
	if sess == nil {
		sess = &Session{ID: connID, ConnID: connID, UID: uid, BoundNodes: make(map[uint32]uint32)}
		m.byConn[connID] = sess
	}
	if old := m.byUID[uid]; old != nil && old != sess {
		old.UID = 0
		old.Authed = false
	}
	if sess.UID != 0 {
		delete(m.byUID, sess.UID)
	}
	sess.UID = uid
	sess.Authed = true
	if sess.BoundNodes == nil {
		sess.BoundNodes = make(map[uint32]uint32)
	}
	for k, v := range bound {
		sess.BoundNodes[k] = v
	}
	if uid != 0 {
		m.byUID[uid] = sess
	}
	return sess
}

// SetBound 写入单个亲和节点。
func (m *SessionManager) SetBound(uid int64, serverType, nodeID uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.byUID[uid]; sess != nil {
		sess.SetBound(serverType, nodeID)
	}
}

// GetByConnID 按连接 ID 查找会话。
func (m *SessionManager) GetByConnID(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byConn[id]
}

// GetByUID 按 UID 查找会话。
func (m *SessionManager) GetByUID(uid int64) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byUID[uid]
}
