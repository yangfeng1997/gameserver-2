package config_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"project/src/core/config"
)

// fakeReloader implements config.Reloader for testing.
type fakeReloader struct {
	name       string
	calls      int
	value      int
	savedValue int
	reloadFn   func() error
}

func (f *fakeReloader) Name() string          { return f.name }
func (f *fakeReloader) SaveSnapshot()         { f.savedValue = f.value }
func (f *fakeReloader) RestoreSnapshot()      { f.value = f.savedValue }
func (f *fakeReloader) Reload() error {
	f.calls++
	if f.reloadFn != nil {
		return f.reloadFn()
	}
	f.value++ // bump version on success
	return nil
}

func TestManagerReloadAll(t *testing.T) {
	mgr := config.NewManager()

	a := &fakeReloader{name: "a", value: 1}
	b := &fakeReloader{name: "b", value: 2}

	mgr.Register(a)
	mgr.Register(b)

	if err := mgr.ReloadAll(); err != nil {
		t.Fatalf("ReloadAll: %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("expected 1 call each, got a=%d b=%d", a.calls, b.calls)
	}
	if a.value != 2 || b.value != 3 {
		t.Fatalf("expected values 2 and 3, got a=%d b=%d", a.value, b.value)
	}
}

func TestManagerReloadAllRollback(t *testing.T) {
	mgr := config.NewManager()

	a := &fakeReloader{name: "a", value: 10}
	b := &fakeReloader{name: "b", value: 20, reloadFn: func() error {
		return fmt.Errorf("b failed")
	}}

	mgr.Register(a)
	mgr.Register(b)

	err := mgr.ReloadAll()
	if err == nil {
		t.Fatal("expected error from b")
	}

	// a should have been rolled back to value=10
	if a.value != 10 {
		t.Fatalf("expected a rolled back to 10, got %d", a.value)
	}
	// b called once and failed
	if b.calls != 1 {
		t.Fatalf("expected b called once, got %d", b.calls)
	}
}

func TestManagerReloadByName(t *testing.T) {
	mgr := config.NewManager()
	a := &fakeReloader{name: "a", value: 1}
	b := &fakeReloader{name: "b", value: 2}
	mgr.Register(a)
	mgr.Register(b)

	if err := mgr.Reload("a"); err != nil {
		t.Fatalf("reload a: %v", err)
	}
	if a.calls != 1 || b.calls != 0 {
		t.Fatalf("expected a=1 b=0, got a=%d b=%d", a.calls, b.calls)
	}

	if err := mgr.Reload("nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent config")
	}
}

func TestManagerRegisterDuplicate(t *testing.T) {
	mgr := config.NewManager()
	a := &fakeReloader{name: "a"}
	b := &fakeReloader{name: "a"}

	if err := mgr.Register(a); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := mgr.Register(b); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()

	type testCfg struct {
		Name  string `yaml:"name"`
		Value int    `yaml:"value"`
	}

	yamlContent := "name: hello\nvalue: 42\n"
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err = config.ExpandEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	var cfg testCfg
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Name != "hello" || cfg.Value != 42 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadYAMLEnvExpansion(t *testing.T) {
	os.Setenv("TEST_SVR_NAME", "gatesvr")
	defer os.Unsetenv("TEST_SVR_NAME")

	dir := t.TempDir()
	type testCfg struct {
		Name string `yaml:"name"`
	}
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte("name: ${TEST_SVR_NAME}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err = config.ExpandEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	var cfg testCfg
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "gatesvr" {
		t.Fatalf("expected gatesvr, got %q", cfg.Name)
	}
}

func TestLoadYAMLUnknownFields(t *testing.T) {
	dir := t.TempDir()
	type testCfg struct {
		Name string `yaml:"name"`
	}
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte("name: hello\nunknown_field: bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err = config.ExpandEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	var cfg testCfg
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err == nil {
		t.Fatal("expected error for unknown field")
	}
}
