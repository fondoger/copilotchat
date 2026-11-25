//go:build unix

package plugininstall

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// A source file swapped for a FIFO between Lstat and Open must not hang a worker.
const readFlags = os.O_RDONLY | unix.O_NONBLOCK

func prepareCLI(*exec.Cmd) {}

func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
func unlockFile(f *os.File)     { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
