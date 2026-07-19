package rpc

import (
	"fmt"
	"time"

	"project/src/core/nodeid"
)

// RoutingMode 路由选择方式。
type RoutingMode uint8

const (
	RoutingAny            RoutingMode = iota
	RoutingConsistentHash
	RoutingDirect
	RoutingBroadcast
)

// Target RPC 调用目标。
type Target struct {
	ServerType uint32
	Mode       RoutingMode
	Key        string
	NodeID     uint32
	Deadline   time.Duration
}

// At 直接指定目标节点。
func (t Target) At(nodeID uint32) Target {
	t.Mode = RoutingDirect
	t.NodeID = nodeID
	return t
}

// ByHash 按 key 选择节点。
func (t Target) ByHash(key string) Target {
	t.Mode = RoutingConsistentHash
	t.Key = key
	return t
}

// Broadcast 广播到同类型所有节点。
func (t Target) Broadcast() Target {
	t.Mode = RoutingBroadcast
	return t
}

// Timeout 覆盖本次调用超时。
func (t Target) Timeout(d time.Duration) Target {
	t.Deadline = d
	return t
}

// Header RPC 头部信息。
type Header struct {
	SeqID      uint64
	Route      string
	DeadlineMs int64
	WaiterID   uint64
	SrcNodeID  uint32
	DestNodeID uint32
	// ServerType 目标服务类型。
	ServerType  uint32
	RoutingMode RoutingMode
	RoutingKey  string
}

// NormalizeRouteTarget 补齐并校验目标服务类型。
func NormalizeRouteTarget(destNodeID, destServerType uint32) (uint32, error) {
	if destNodeID == 0 {
		return destServerType, nil
	}
	_, nodeServerType, _ := nodeid.Decode(destNodeID)
	if destServerType == 0 {
		return nodeServerType, nil
	}
	if nodeServerType != destServerType {
		return 0, fmt.Errorf("dest server type mismatch: node=%s decoded=%d header=%d",
			nodeid.String(destNodeID), nodeServerType, destServerType)
	}
	return destServerType, nil
}

// Reply 一次性回包句柄。
type Reply[T any] func(T, error)

// Poster 主循环投递接口。
type Poster interface {
	Post(func())
}
