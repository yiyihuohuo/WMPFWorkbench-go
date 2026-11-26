// Package process retains process handles instead of killing by reused PID.
package process

import (
	"context"
	"os/exec"
	"sync"
)

type Owned struct {
	Cmd  *exec.Cmd
	job  *job
	done chan struct{}
	once sync.Once
	err  error
}

func Start(cmd *exec.Cmd) (*Owned, error) {
	j, err := newJob()
	if err != nil {
		return nil, err
	}
	Hide(cmd)
	if err = cmd.Start(); err != nil {
		j.close()
		return nil, err
	}
	if err = j.assign(cmd.Process); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		j.close()
		return nil, err
	}
	p := &Owned{Cmd: cmd, job: j, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); p.once.Do(func() { j.close() }); close(p.done) }()
	return p, nil
}
func (p *Owned) Done() <-chan struct{} { return p.done }
func (p *Owned) Err() error            { <-p.done; return p.err }
func (p *Owned) Stop(ctx context.Context) error {
	p.once.Do(func() { p.job.close(); p.Cmd.Process.Kill() })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
