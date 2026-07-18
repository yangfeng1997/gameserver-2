package app

import (
	"os"
	"testing"
)

func TestSignalHelpers(t *testing.T) {
	if !isTerminateSignal(os.Interrupt) {
		t.Fatal("os.Interrupt should terminate")
	}
	if isReloadSignal(os.Interrupt) {
		t.Fatal("os.Interrupt should not reload")
	}
	if isDrainSignal(os.Interrupt) {
		t.Fatal("os.Interrupt should not drain")
	}
}
