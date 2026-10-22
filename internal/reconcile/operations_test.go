package reconcile

import (
	"context"
	"os"
	"pulltide/internal/state"
	"strings"
	"testing"
	"time"
)

func TestBoundedDoesNotAcceptSuccessAtExpiredDeadline(t *testing.T) {
	for n := 0; n < 8; n++ {
		result := bounded(context.Background(), time.Millisecond, func(ctx context.Context) Result { <-ctx.Done(); return Result{Outcome: Success} })
		if result.Outcome != Failure {
			t.Fatal("accepted success after operation timeout")
		}
	}
}
func TestBoundedPanicAndInvalidOutcomeAreUnknown(t *testing.T) {
	for _, call := range []func(context.Context) Result{
		func(context.Context) Result { panic("secret") },
		func(context.Context) Result { return Result{Outcome: "invalid-secret"} },
	} {
		if bounded(context.Background(), time.Second, call).Outcome != Unknown {
			t.Fatal("invalid completion evidence accepted")
		}
	}
}
func TestBoundedZeroBudgetNeverInvokes(t *testing.T) {
	invoked := false
	r := bounded(context.Background(), 0, func(context.Context) Result { invoked = true; return Result{Outcome: Success} })
	if invoked || r.Outcome != Failure {
		t.Fatal("expired action invoked")
	}
}

func TestBootEvidenceIsIndependentOfOwnershipAndWorkloadHealth(t *testing.T) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	boot := strings.TrimSpace(string(data))
	st := &state.State{Journal: &state.Journal{BootID: boot}}
	if newBoot(st) {
		t.Fatal("same boot accepted")
	}
	st.Journal.BootID = "earlier-local-boot"
	if !newBoot(st) {
		t.Fatal("changed boot not recognized")
	}
	st.Journal.Operation = &state.Operation{BootID: boot}
	if newBoot(st) {
		t.Fatal("journal boot overrode exact operation boot")
	}
}
