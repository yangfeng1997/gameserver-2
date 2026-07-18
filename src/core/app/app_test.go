package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testModule struct {
	DefaultModule
	name     string
	dependsOn []string
	calls    *[]string
	ready    *Ready
}

func (m *testModule) Name() string { return m.name }
func (m *testModule) DependsOn() []string { return m.dependsOn }
func (m *testModule) Init() error {
	*m.calls = append(*m.calls, m.name+":init")
	return nil
}
func (m *testModule) AfterInit() error {
	*m.calls = append(*m.calls, m.name+":after")
	if m.ready != nil {
		m.ready.Done()
	}
	return nil
}
func (m *testModule) BeforeShutdown() { *m.calls = append(*m.calls, m.name+":before") }
func (m *testModule) Shutdown()      { *m.calls = append(*m.calls, m.name+":shutdown") }

func TestSortModules(t *testing.T) {
	calls := make([]string, 0, 8)
	a := &testModule{name: "a", calls: &calls}
	b := &testModule{name: "b", dependsOn: []string{"a"}, calls: &calls}
	c := &testModule{name: "c", dependsOn: []string{"b"}, calls: &calls}
	ordered, err := sortModules([]Module{c, a, b})
	if err != nil {
		t.Fatalf("sortModules: %v", err)
	}
	if got, want := ordered[0].Name(), "a"; got != want {
		t.Fatalf("first = %q, want %q", got, want)
	}
	if got, want := ordered[1].Name(), "b"; got != want {
		t.Fatalf("second = %q, want %q", got, want)
	}
	if got, want := ordered[2].Name(), "c"; got != want {
		t.Fatalf("third = %q, want %q", got, want)
	}
}

func TestSortModulesCycle(t *testing.T) {
	a := &testModule{name: "a", dependsOn: []string{"b"}}
	b := &testModule{name: "b", dependsOn: []string{"a"}}
	if _, err := sortModules([]Module{a, b}); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestReady(t *testing.T) {
	ready := NewReady()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		ready.Done()
	}()
	if err := ready.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestReadyFail(t *testing.T) {
	ready := NewReady()
	want := errors.New("boom")
	ready.Fail(want)
	if err := ready.WaitReady(context.Background()); !errors.Is(err, want) {
		t.Fatalf("WaitReady = %v, want %v", err, want)
	}
}

func TestAppLifecycle(t *testing.T) {
	calls := make([]string, 0, 16)
	readyA := NewReady()
	readyB := NewReady()
	m1 := &testModule{name: "a", calls: &calls, ready: readyA}
	m2 := &testModule{name: "b", dependsOn: []string{"a"}, calls: &calls, ready: readyB}
	builder := NewBuilder().
		AddModule(m2).
		AddModule(m1)
	app, err := builder.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		app.Shutdown()
	}()
	if err := app.Startup(ctx); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	want := []string{"a:init", "b:init", "a:after", "b:after", "b:before", "a:before", "b:shutdown", "a:shutdown"}
	if len(calls) != len(want) {
		t.Fatalf("calls len = %d, want %d (%v)", len(calls), len(want), calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q (all=%v)", i, calls[i], want[i], calls)
		}
	}
}
