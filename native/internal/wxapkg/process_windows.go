//go:build windows

package wxapkg

import (
	"os/exec"
	"syscall"
)

func hideWorker(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
