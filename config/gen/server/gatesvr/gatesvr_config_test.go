package gatesvr_test

import (
	"testing"

	"project/config/gen/server/gatesvr"
	"project/src/core/config"
)

func TestGatesvrReloaderImplementsInterface(t *testing.T) {
	var _ config.Reloader = gatesvr.GatesvrConfigReloader
}
