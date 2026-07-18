package config_test

import (
	"os"
	"testing"

	"project/src/core/config"
)

func TestExpandEnv(t *testing.T) {
	os.Setenv("TEST_KEY_A", "alpha")
	os.Setenv("TEST_KEY_B", "beta")
	defer os.Unsetenv("TEST_KEY_A")
	defer os.Unsetenv("TEST_KEY_B")

	input := []byte("host: ${TEST_KEY_A}\nport: ${TEST_KEY_B}\n")
	got, err := config.ExpandEnv(input)
	if err != nil {
		t.Fatalf("ExpandEnv: %v", err)
	}
	want := "host: alpha\nport: beta\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestExpandEnvMissing(t *testing.T) {
	input := []byte("host: ${MISSING_VAR_THAT_DOES_NOT_EXIST}\n")
	_, err := config.ExpandEnv(input)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}
