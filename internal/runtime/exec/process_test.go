//go:build linux

package execruntime

import (
	"context"
	"os"
	"path/filepath"
	"pulltide/internal/reconcile"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestForegroundRunner(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    reconcile.Outcome
	}{{"exit 0", reconcile.Success}, {"exit 3", reconcile.Failure}, {"sleep 0.01 & wait", reconcile.Success}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got := supervisor{}.run(ctx, "/bin/sh", []string{"/bin/sh", "-c", tc.command}, t.TempDir(), []string{"PATH=/usr/bin:/bin", "LANG=C"})
		cancel()
		if got.Outcome != tc.want {
			t.Fatal(tc.command, got)
		}
	}
}
func TestTimeoutCancellationAndUncertainGroups(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		unknown      bool
	}{{"term", "sleep 30", false}, {"kill", "trap '' TERM; sleep 30", false}, {"background", "sleep 30 & exit 0", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			s := supervisor{grace: 40 * time.Millisecond, confirmation: 300 * time.Millisecond}
			got := s.run(ctx, "/bin/sh", []string{"/bin/sh", "-c", tc.script}, t.TempDir(), []string{"PATH=/usr/bin:/bin", "LANG=C"})
			want := reconcile.Failure
			if tc.unknown {
				want = reconcile.Unknown
			}
			if got.Outcome != want {
				t.Fatal(got, want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if (supervisor{}).run(ctx, "/bin/sh", []string{"sh", "-c", "exit 0"}, t.TempDir(), nil).Outcome != reconcile.Failure {
		t.Fatal("cancelled command launched")
	}
}
func TestOutputIsBoundedAndDiscarded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := (supervisor{}).run(ctx, "/bin/sh", []string{"sh", "-c", "i=0; while [ $i -lt 10000 ]; do printf 'FAKE_SECRET_1234567890\\n'; printf 'FAKE_SECRET_1234567890\\n' >&2; i=$((i+1)); done"}, t.TempDir(), []string{"PATH=/usr/bin:/bin", "LANG=C"})
	if got.Outcome != reconcile.Success {
		t.Fatal(got)
	}
	c := capture{}
	data := []byte(strings.Repeat("secret", 20000))
	n, err := c.Write(data)
	if err != nil || n != len(data) || len(c.bytes) != 64*1024 {
		t.Fatal("capture is not bounded")
	}
}
func TestInconclusiveObservationIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := supervisor{grace: 20 * time.Millisecond, confirmation: 30 * time.Millisecond, observe: func(int) (bool, error) { return false, syscall.EACCES }}
	if s.run(ctx, "/bin/true", []string{"true"}, t.TempDir(), nil).Outcome != reconcile.Unknown {
		t.Fatal("unreadable process evidence treated as completion")
	}
}
func TestProcObserver(t *testing.T) {
	running, err := groupRunning(syscall.Getpgrp())
	if err != nil || !running {
		t.Fatal("own process group not observed", err)
	}
}

func TestCancellationAfterLaunch(t *testing.T) {
	root := privateDir(t)
	marker := filepath.Join(root, "started")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan reconcile.Result, 1)
	go func() {
		done <- (supervisor{grace: 30 * time.Millisecond, confirmation: 200 * time.Millisecond}).run(ctx, "/bin/sh", []string{"sh", "-c", `printf started > "$1"; sleep 30`, "fixture", marker}, root, []string{"PATH=/usr/bin:/bin", "LANG=C"})
	}()
	deadline := time.After(time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fixture did not launch")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if result := <-done; result.Outcome != reconcile.Failure {
		t.Fatal("cancellation not conclusively terminated", result)
	}
}
func TestDefaultEscalationUsesFiveSecondGrace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var term, kill time.Time
	s := supervisor{signal: func(group int, sig syscall.Signal) error {
		if sig == syscall.SIGTERM {
			term = time.Now()
		}
		if sig == syscall.SIGKILL {
			kill = time.Now()
		}
		return syscall.Kill(-group, sig)
	}}
	got := s.run(ctx, "/bin/sh", []string{"sh", "-c", `trap '' TERM; exec sleep 30`}, privateDir(t), []string{"PATH=/usr/bin:/bin", "LANG=C"})
	if got.Outcome != reconcile.Failure || term.IsZero() || kill.IsZero() || kill.Sub(term) < 5*time.Second || kill.Sub(term) > 6*time.Second {
		t.Fatal("incorrect escalation", got, kill.Sub(term))
	}
}
func TestSignallingFailureIsUncertain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	s := supervisor{grace: 20 * time.Millisecond, confirmation: 200 * time.Millisecond, signal: func(int, syscall.Signal) error { return syscall.EPERM }}
	if result := s.run(ctx, "/bin/sleep", []string{"sleep", "0.05"}, privateDir(t), nil); result.Outcome != reconcile.Unknown {
		t.Fatal("failed signalling claimed completion", result)
	}
}

func TestLeaderRemainsReservedDuringGroupCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	signals := 0
	s := supervisor{grace: 30 * time.Millisecond, confirmation: 100 * time.Millisecond, signal: func(group int, sig syscall.Signal) error {
		signals++
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(group), "stat")); err != nil {
			t.Fatal("leader was reaped before signalling ended", err)
		}
		return syscall.Kill(-group, sig)
	}}
	result := s.run(ctx, "/bin/sh", []string{"sh", "-c", "sleep 30 & exit 0"}, privateDir(t), []string{"PATH=/usr/bin:/bin", "LANG=C"})
	if result.Outcome != reconcile.Unknown || signals == 0 {
		t.Fatal("background violation was not gated", result)
	}
}
func TestUnconfirmedGroupIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := supervisor{grace: 20 * time.Millisecond, confirmation: 25 * time.Millisecond, observe: func(int) (bool, error) { return true, nil }}
	if result := s.run(ctx, "/bin/true", []string{"true"}, privateDir(t), nil); result.Outcome != reconcile.Unknown {
		t.Fatal("unconfirmed group treated as terminated", result)
	}
}
