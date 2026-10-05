package driver

import (
	"os"

	"golang.org/x/sys/unix"
)

// enterNetNamespace moves the calling thread into the namespace ns refers to.
func enterNetNamespace(ns *os.File) error { return unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET) }
