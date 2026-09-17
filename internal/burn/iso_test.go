package burn

import (
	"context"
	"testing"

	"varis/internal/execx"
)

func TestExtractFromDisc_InvokesXorrisoIndevExtract(t *testing.T) {
	fake := &execx.FakeExecutor{}
	err := ExtractFromDisc(context.Background(), fake, "/dev/sr0", "/BD:0001.toc.json", "/tmp/out.json")
	if err != nil {
		t.Fatalf("ExtractFromDisc: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Name != "xorriso" {
		t.Fatalf("calls = %+v", calls)
	}
	want := []string{"-indev", "/dev/sr0", "-extract", "/BD:0001.toc.json", "/tmp/out.json"}
	if len(calls[0].Args) != len(want) {
		t.Fatalf("args = %v, want %v", calls[0].Args, want)
	}
	for i := range want {
		if calls[0].Args[i] != want[i] {
			t.Fatalf("args = %v, want %v", calls[0].Args, want)
		}
	}
}
