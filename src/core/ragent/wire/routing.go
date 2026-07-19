package wire

// RoutingMode RouterAgent wire 级路由决策类型。
type RoutingMode uint8

const (
	RoutingModeAny       RoutingMode = iota
	RoutingModeDirect
	RoutingModeHash
	RoutingModeBroadcast
)
