//go:build !windows

package process

import (
	"os"
	"os/exec"
)

type job struct{}

func newJob() (*job, error)             { return &job{}, nil }
func (j *job) assign(*os.Process) error { return nil }
func (j *job) close()                   {}
func Hide(*exec.Cmd)                    {}
