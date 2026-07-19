// Package routeragent RouterAgent sidecar：UDS 监听 + 本地进程路由。
package routeragent

import (
	"context"

	"project/config/gen/server/common"
	"project/config/gen/server/routeragent"
	"project/src/core/app"
	"project/src/core/config"
	"project/src/core/nodeid"
	ragentagent "project/src/core/ragent/agent"
)

// Module RouterAgent App 模块。
type Module struct {
	app.DefaultModule
	runtime *ragentagent.Runtime
}

// NewModule 创建 RouterAgent 模块。
func NewModule() *Module {
	return &Module{
		runtime: ragentagent.NewRuntime(),
	}
}

func (m *Module) Name() string { return "routeragent" }

func (m *Module) Init() error {
	cfg := routeragent.RouteragentConfigInstance()
	if cfg == nil {
		return nil // will be set by LoadConfigs before Init
	}
	nid, _ := nodeid.Parse(m.App().NodeID())
	m.runtime.SetNodeID(nid.Uint32())
	m.runtime.ApplyConfig(cfg.SockPath)
	return m.runtime.Init()
}

func (m *Module) AfterInit() error {
	return m.runtime.AfterInit()
}

func (m *Module) WaitReady(ctx context.Context) error {
	return m.runtime.WaitReady(ctx)
}

func (m *Module) BeforeShutdown() {
	m.runtime.BeforeShutdown()
}

func (m *Module) Shutdown() {
	m.runtime.Shutdown()
}

// ---- 配置 ----

func RouteragentConfig() *routeragent.RouteragentConfig {
	return routeragent.RouteragentConfigInstance()
}

func LoadConfigs() error {
	if err := common.LoadCommonConfig(); err != nil {
		return err
	}
	if err := routeragent.LoadRouteragentConfig(); err != nil {
		return err
	}
	return nil
}

func ReloadConfigs() error {
	mgr := config.NewManager()
	mgr.Register(common.CommonConfigReloader)
	mgr.Register(routeragent.RouteragentConfigReloader)
	return mgr.ReloadAll()
}
