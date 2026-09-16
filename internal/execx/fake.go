package execx

import (
	"context"
	"sync"
)

type Call struct {
	Name string
	Args []string
}

type Result struct {
	Output []byte
	Err    error
}

// FakeExecutor records every call and returns the Result configured for
// that command name (zero value: nil output, nil error).
type FakeExecutor struct {
	Results map[string]Result

	mu    sync.Mutex
	calls []Call
}

func (f *FakeExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args})
	f.mu.Unlock()

	r := f.Results[name]
	return r.Output, r.Err
}

func (f *FakeExecutor) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}
