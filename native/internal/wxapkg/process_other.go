//go:build !windows

package wxapkg

import "os/exec"

func hideWorker(cmd *exec.Cmd) {}
