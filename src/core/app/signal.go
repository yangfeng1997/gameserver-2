package app

import (
	"os"
	"syscall"
)

func watchedSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP}
}

func isTerminateSignal(sig os.Signal) bool {
	switch sig {
	case os.Interrupt, syscall.SIGTERM:
		return true
	default:
		return false
	}
}

func isDrainSignal(sig os.Signal) bool {
	return sig == syscall.SIGQUIT
}

func isReloadSignal(sig os.Signal) bool {
	return sig == syscall.SIGHUP
}
