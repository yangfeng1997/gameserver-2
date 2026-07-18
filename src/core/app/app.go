package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"project/src/pkg/logger"
)

const defaultReadyTimeout = 10 * time.Second

// App is the process orchestrator for a single service instance.
type App struct {
	name         string
	nodeID       string
	pprofEnabled bool
	pprofAddr    string

	modules   []Module
	moduleMap map[string]Module

	shutdownHooks []func()
	reloadHooks   []func() error

	poster *PosterQueue
	ctx    context.Context
	cancel context.CancelFunc
	running atomic.Bool

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// Context returns the process lifecycle context.
func (a *App) Context() context.Context {
	if a == nil {
		return context.Background()
	}
	return a.ctx
}

// Startup initializes modules, waits for readiness, and blocks until shutdown.
func (a *App) Startup(ctx context.Context) error {
	if a == nil {
		return errors.New("app is nil")
	}
	if a.running.Load() {
		return errors.New("app already running")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	baseCtx, cancel := context.WithCancel(ctx)
	a.ctx = baseCtx
	a.cancel = cancel
	a.poster.Start()

	if err := a.startPprof(); err != nil {
		a.poster.Stop()
		cancel()
		return err
	}

	for _, module := range a.modules {
		module.Set(a)
	}
	for _, module := range a.modules {
		if err := module.Init(); err != nil {
			a.stopRuntime()
			return fmt.Errorf("init module %s: %w", module.Name(), err)
		}
	}
	for _, module := range a.modules {
		if err := module.AfterInit(); err != nil {
			a.stopRuntime()
			return fmt.Errorf("after init module %s: %w", module.Name(), err)
		}
	}
	if err := a.waitReady(); err != nil {
		a.stopRuntime()
		return err
	}

	a.running.Store(true)
	defer func() {
		a.running.Store(false)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, watchedSignals()...)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-a.ctx.Done():
			logger.Info("app context done")
			a.stopRuntime()
			return nil
		case sig := <-sigCh:
			if isReloadSignal(sig) {
				if err := a.Reload(); err != nil {
					logger.Error("reload failed", logger.Err(err))
				}
				continue
			}
			if isDrainSignal(sig) || isTerminateSignal(sig) {
				a.stopRuntime()
				return nil
			}
		}
	}
}

// Shutdown stops the app and cancels its context.
func (a *App) Shutdown() {
	if a == nil {
		return
	}
	a.stopRuntime()
}

// Reload runs registered reload hooks in order.
func (a *App) Reload() error {
	if a == nil {
		return errors.New("app is nil")
	}
	for _, hook := range append([]func() error{}, a.reloadHooks...) {
		if err := hook(); err != nil {
			return err
		}
	}
	return nil
}

// Post enqueues a function to the serializer queue.
func (a *App) Post(fn func()) {
	if a == nil || a.poster == nil {
		return
	}
	a.poster.Post(fn)
}

// GetModule returns a registered module by name.
func (a *App) GetModule(name string) (Module, error) {
	if a == nil {
		return nil, errors.New("app is nil")
	}
	module, ok := a.moduleMap[name]
	if !ok {
		return nil, fmt.Errorf("module %s not found", name)
	}
	return module, nil
}

// AddRoutine increments the internal wait group.
func (a *App) AddRoutine(delta int) {
	a.wg.Add(delta)
}

// DoneRoutine decrements the internal wait group.
func (a *App) DoneRoutine() {
	a.wg.Done()
}

// WaitRoutines waits until all tracked goroutines exit.
func (a *App) WaitRoutines() {
	a.wg.Wait()
}

// IsRunning reports whether the app has entered its run loop.
func (a *App) IsRunning() bool {
	return a.running.Load()
}

// Name returns the configured process name.
func (a *App) Name() string {
	if a == nil {
		return ""
	}
	return a.name
}

// NodeID returns the configured node identifier.
func (a *App) NodeID() string {
	if a == nil {
		return ""
	}
	return a.nodeID
}

func (a *App) stopRuntime() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		for i := len(a.modules) - 1; i >= 0; i-- {
			a.modules[i].BeforeShutdown()
		}
		for i := len(a.modules) - 1; i >= 0; i-- {
			a.modules[i].Shutdown()
		}
		if a.poster != nil {
			a.poster.Stop()
		}
		a.WaitRoutines()
		for i := len(a.shutdownHooks) - 1; i >= 0; i-- {
			a.shutdownHooks[i]()
		}
	})
}

func (a *App) waitReady() error {
	for _, module := range a.modules {
		waiter, ok := module.(ReadyWaiter)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(a.ctx, defaultReadyTimeout)
		err := waiter.WaitReady(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("wait ready module %s: %w", module.Name(), err)
		}
	}
	return nil
}

func (a *App) startPprof() error {
	if !a.pprofEnabled {
		return nil
	}
	addr := a.pprofAddr
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Addr: addr, Handler: mux}
	a.shutdownHooks = append(a.shutdownHooks, func() {
		_ = server.Close()
	})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server stopped", logger.Err(err))
		}
	}()
	return nil
}
