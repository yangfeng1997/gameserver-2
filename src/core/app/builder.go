package app

import (
	"fmt"
)

type Builder struct {
	opts          options
	modules       []Module
	shutdownHooks []func()
	reloadHooks   []func() error
}

func NewBuilder(opts ...Option) *Builder {
	b := &Builder{}
	for _, opt := range opts {
		if opt != nil {
			opt(&b.opts)
		}
	}
	return b
}

func (b *Builder) AddModule(module Module) *Builder {
	if module != nil {
		b.modules = append(b.modules, module)
	}
	return b
}

func (b *Builder) AddShutdownHook(hook func()) *Builder {
	if hook != nil {
		b.shutdownHooks = append(b.shutdownHooks, hook)
	}
	return b
}

func (b *Builder) AddReloadHook(hook func() error) *Builder {
	if hook != nil {
		b.reloadHooks = append(b.reloadHooks, hook)
	}
	return b
}

func (b *Builder) Build() (*App, error) {
	ordered, err := sortModules(b.modules)
	if err != nil {
		return nil, err
	}
	app := &App{
		name:          b.opts.name,
		nodeID:        b.opts.nodeID,
		pprofEnabled:  b.opts.pprof,
		pprofAddr:     b.opts.pprofAddr,
		modules:       ordered,
		moduleMap:     make(map[string]Module, len(ordered)),
		shutdownHooks: append([]func(){}, b.shutdownHooks...),
		reloadHooks:   append([]func() error{}, b.reloadHooks...),
		poster:        NewPosterQueue(b.opts.postBuf),
	}
	for _, module := range ordered {
		name := module.Name()
		if name == "" {
			return nil, fmt.Errorf("app: module name is empty")
		}
		if _, exists := app.moduleMap[name]; exists {
			return nil, fmt.Errorf("app: duplicate module %q", name)
		}
		app.moduleMap[name] = module
	}
	return app, nil
}

func sortModules(modules []Module) ([]Module, error) {
	if len(modules) == 0 {
		return nil, nil
	}
	moduleMap := make(map[string]Module, len(modules))
	for _, module := range modules {
		if module == nil {
			return nil, fmt.Errorf("app: module is nil")
		}
		name := module.Name()
		if name == "" {
			return nil, fmt.Errorf("app: module name is empty")
		}
		if _, exists := moduleMap[name]; exists {
			return nil, fmt.Errorf("app: duplicate module %q", name)
		}
		moduleMap[name] = module
	}

	indegree := make(map[string]int, len(modules))
	graph := make(map[string][]string, len(modules))
	for _, module := range modules {
		name := module.Name()
		for _, dep := range module.DependsOn() {
			if dep == "" {
				continue
			}
			d, ok := moduleMap[dep]
			if !ok || d == nil {
				return nil, fmt.Errorf("app: module %q depends on missing module %q", name, dep)
			}
			graph[dep] = append(graph[dep], name)
			indegree[name]++
		}
	}

	queue := make([]string, 0, len(modules))
	for _, module := range modules {
		name := module.Name()
		if indegree[name] == 0 {
			queue = append(queue, name)
		}
	}

	ordered := make([]Module, 0, len(modules))
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		ordered = append(ordered, moduleMap[name])
		for _, next := range graph[name] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(ordered) != len(modules) {
		return nil, fmt.Errorf("app: module dependency cycle detected")
	}
	return ordered, nil
}
