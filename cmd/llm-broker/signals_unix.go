//go:build !windows

package main

import (
	"os"
	"syscall"
)

func signals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT}
}
