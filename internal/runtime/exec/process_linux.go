//go:build linux

package execruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"pulltide/internal/reconcile"
)

const captureLimit = 64 * 1024

// capture discards overflow while always acknowledging/draining the full stream.
// Neither the buffer nor child output is returned, logged, or persisted.
type capture struct{ bytes []byte }

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	remaining := captureLimit - len(c.bytes)
	if remaining > len(p) {
		remaining = len(p)
	}
	c.bytes = append(c.bytes, p[:remaining]...)
	return n, nil
}

type supervisor struct {
	grace, confirmation time.Duration // Zero selects the mandated five-second bounds.
	observe             func(int) (bool, error)
	signal              func(int, syscall.Signal) error
}

func (s supervisor) run(ctx context.Context, executable string, argv []string, dir string, env []string) reconcile.Result {
	fail := reconcile.Result{Outcome: reconcile.Failure}
	if ctx.Err() != nil || len(argv) == 0 {
		return fail
	}
	if _, ok := ctx.Deadline(); !ok {
		return fail
	}
	grace, confirmation := s.grace, s.confirmation
	if grace == 0 {
		grace = 5 * time.Second
	}
	if confirmation == 0 {
		confirmation = 5 * time.Second
	}
	observe := s.observe
	if observe == nil {
		observe = groupRunning
	}
	signal := s.signal
	if signal == nil {
		signal = func(pgid int, sig syscall.Signal) error { return syscall.Kill(-pgid, sig) }
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return fail
	}
	defer outR.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		outW.Close()
		return fail
	}
	defer errR.Close()
	cmd := exec.Command(executable, argv[1:]...)
	cmd.Args = append([]string(nil), argv...)
	cmd.Dir = dir
	cmd.Env = append([]string{}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = outW
	cmd.Stderr = errW
	if cmd.Start() != nil {
		outW.Close()
		errW.Close()
		return fail
	}
	outW.Close()
	errW.Close()
	pgid := cmd.Process.Pid
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); defer outR.Close(); c := capture{}; _, _ = io.Copy(&c, outR) }()
	go func() { defer drains.Done(); defer errR.Close(); c := capture{}; _, _ = io.Copy(&c, errR) }()
	drained := make(chan struct{})
	drainCompletion := drained
	go func() { drains.Wait(); close(drainCompletion) }()
	exitNotice := make(chan error, 1)
	reap := make(chan struct{})
	waited := make(chan error, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reap) }) }
	defer release()
	go func() { err := waitUnreaped(pgid); exitNotice <- err; <-reap; waited <- cmd.Wait() }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	exited, outputDone, cancelled, uncertain := false, false, false, false
	exitConfirmed := false
	finish := func(outcome reconcile.Outcome) reconcile.Result {
		outR.Close()
		errR.Close()
		<-drainCompletion
		release()
		if exitConfirmed {
			if err := <-waited; err != nil && outcome == reconcile.Success {
				outcome = reconcile.Failure
			}
		}
		return reconcile.Result{Outcome: outcome}
	}
	var terminating, forced bool
	var signalAt, timeLimit time.Time
	ctxDone := ctx.Done()
	send := func(sig syscall.Signal) {
		if err := signal(pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			uncertain = true
		}
	}
	begin := func() {
		if !terminating {
			terminating = true
			signalAt = time.Now()
			timeLimit = signalAt.Add(grace + confirmation)
			send(syscall.SIGTERM)
		}
	}
	for {
		running, e := observe(pgid)
		if e != nil {
			uncertain = true
			begin()
		}
		if exited && running && !terminating {
			uncertain = true
			begin()
		}
		if exited && !running && e == nil && outputDone {
			if uncertain {
				return finish(reconcile.Unknown)
			}
			if cancelled || ctx.Err() != nil {
				return finish(reconcile.Failure)
			}
			return finish(reconcile.Success)
		}
		if exited && !running && !outputDone && !terminating {
			begin()
		}
		if terminating {
			now := time.Now()
			if !forced && !now.Before(signalAt.Add(grace)) {
				forced = true
				send(syscall.SIGKILL)
			}
			if !now.Before(timeLimit) {
				return finish(reconcile.Unknown)
			}
		}
		select {
		case err := <-exitNotice:
			exited = true
			exitConfirmed = err == nil
			exitNotice = nil
			if err != nil {
				uncertain = true
				begin()
			}
		case <-drained:
			outputDone = true
			drained = nil
		case <-ctxDone:
			cancelled = true
			ctxDone = nil
			begin()
		case <-ticker.C:
		}
	}
}

// This is evidence about this group only, conditional on the supported profile.
// It neither finds escaped descendants nor proves external job completion.
func groupRunning(pgid int) (bool, error) {
	if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, dir := range dirs {
		if _, err := strconv.Atoi(dir.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", dir.Name(), "stat"))
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		// comm is parenthesized and may itself contain spaces or closing parentheses.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return false, syscall.EIO
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 3 {
			return false, syscall.EIO
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return false, err
		}
		if group == pgid && fields[0] != "Z" && fields[0] != "X" {
			return true, nil
		}
	}
	return false, nil
}

// WNOWAIT reserves the exited leader's PID/PGID until supervision ends, avoiding
// signalling an unrelated process group after numeric ID reuse. No siginfo fields
// are interpreted; os/exec performs the final reap and exit-status decoding.
func waitUnreaped(pid int) error {
	var info [16]uint64 // Linux siginfo_t is 128 bytes; uint64 provides alignment.
	for {
		_, _, err := syscall.Syscall6(syscall.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}
