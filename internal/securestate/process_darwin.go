//go:build darwin

package securestate

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func processStartIdentity(pid int) (string, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	started := info.Proc.P_starttime
	return fmt.Sprintf("%d:%d", started.Sec, started.Usec), nil
}

// processAlive uses Darwin's kill(2) directly. os.FindProcess alone does not
// prove that a PID still exists on macOS, so stale herdr-tandem locks could otherwise
// look live forever after a crashed or killed process.
func processAlive(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
