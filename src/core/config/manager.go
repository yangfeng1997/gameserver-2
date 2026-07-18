// Package config 提供配置热更管理器。
// Manager 是所有配置条目的注册与协调中心：注册 → 起服 Load → 热更 ReloadAll。
//
// 使用范式：
//
//	mgr := config.NewManager()
//	mgr.Register(gatesvr.GatesvrConfigReloader)
//	mgr.Register(logger.LogConfigReloader)
//
//	// 起服阶段（主 goroutine，无锁）
//	gatesvr.LoadGatesvrConfig()
//	logger.LoadLogConfig()
//
//	// 热更（由 App 信号触发）
//	app.AddReloadHook(mgr.ReloadAll)
package config

import "fmt"

// Reloader 是单个配置条目需要实现的接口。
// 每个由 configgen 生成的配置文件都会生成一个对应的 Reloader 单例。
type Reloader interface {
	// Name 返回配置名称，如 "gatesvr", "gatesvr_log", "common"。
	Name() string

	// Reload 重新加载配置文件：读取 YAML → Validate → CheckReload → atomic.Store。
	// 内部无锁（约定仅在 App 主循环 goroutine 中调用）。
	Reload() error

	// SaveSnapshot 保存当前配置的快照（供 Manager 回滚）。
	SaveSnapshot()

	// RestoreSnapshot 回滚到上次 SaveSnapshot 保存的值。
	RestoreSnapshot()
}

// Manager 管理一组 Reloader，提供有序的批量重载和按名称单独重载。
// 零值不可用，必须通过 NewManager 创建。
type Manager struct {
	entries []Reloader
}

// NewManager 创建配置管理器。
func NewManager() *Manager {
	return &Manager{}
}

// Register 注册一个配置条目。注册顺序即为 ReloadAll 的执行顺序。
// 若 Name() 与已注册条目重复则返回 error。
func (m *Manager) Register(r Reloader) error {
	name := r.Name()
	for _, e := range m.entries {
		if e.Name() == name {
			return fmt.Errorf("config: duplicate Reloader name %q", name)
		}
	}
	m.entries = append(m.entries, r)
	return nil
}

// ReloadAll 按注册顺序依次 reload 所有已注册条目。
// 采用 all-or-nothing 策略：任一条目 reload 失败，已成功 reload 的条目全部回滚到旧快照。
func (m *Manager) ReloadAll() error {
	var done []Reloader

	for _, e := range m.entries {
		e.SaveSnapshot()
		if err := e.Reload(); err != nil {
			for _, d := range done {
				d.RestoreSnapshot()
			}
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		done = append(done, e)
	}
	return nil
}

// Reload 按名称 reload 单个配置条目。
func (m *Manager) Reload(name string) error {
	for _, e := range m.entries {
		if e.Name() == name {
			return e.Reload()
		}
	}
	return fmt.Errorf("config %q not found", name)
}
