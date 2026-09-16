package execx

import (
	"context"
	"errors"
	"testing"
)

func TestFakeExecutor_RecordsCallsAndReturnsConfiguredResult(t *testing.T) {
	fake := &FakeExecutor{
		Results: map[string]Result{
			"par2create": {Output: []byte("ok"), Err: nil},
			"xorriso":    {Output: nil, Err: errors.New("boom")},
		},
	}

	out, err := fake.Run(context.Background(), "par2create", "-r10", "set.par2")
	if err != nil || string(out) != "ok" {
		t.Fatalf("par2create call = %q, %v", out, err)
	}

	_, err = fake.Run(context.Background(), "xorriso", "-as", "mkisofs")
	if err == nil {
		t.Fatal("expected xorriso call to fail")
	}

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "par2create" || calls[1].Name != "xorriso" {
		t.Errorf("Calls() = %+v", calls)
	}
}
