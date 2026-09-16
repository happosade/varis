package burn

import (
	"context"
	"testing"

	"varis/internal/execx"
)

func TestCreateParity_InvokesPar2CreateWithPercent(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := CreateParity(context.Background(), fake, "/spool/BD0001.tar", 15); err != nil {
		t.Fatalf("CreateParity: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "par2create" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Args[0] != "-r15" || calls[0].Args[1] != "/spool/BD0001.tar" {
		t.Errorf("args = %v", calls[0].Args)
	}
}

func TestBuildISO_InvokesXorriso(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := BuildISO(context.Background(), fake, "/spool/BD0001-src", "/spool/BD0001.iso"); err != nil {
		t.Fatalf("BuildISO: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "xorriso" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestBurnISO_InvokesWodimWithDevice(t *testing.T) {
	fake := &execx.FakeExecutor{}
	if err := BurnISO(context.Background(), fake, "/dev/sr0", "/spool/BD0001.iso"); err != nil {
		t.Fatalf("BurnISO: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "wodim" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Args[0] != "dev=/dev/sr0" {
		t.Errorf("args = %v", calls[0].Args)
	}
}
