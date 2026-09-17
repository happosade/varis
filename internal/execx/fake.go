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

// FakeExecutor records every call and returns a configured Result: from
// Funcs (for tests that need to simulate a command's filesystem side
// effects, like xorriso writing an ISO file) if present for that command
// name, otherwise from Results (zero value: nil output, nil error).
type FakeExecutor struct {
	Results map[string]Result
	Funcs   map[string]func(args []string) Result

	mu    sync.Mutex
	calls []Call
}

func (f *FakeExecutor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Name: name, Args: args})
	f.mu.Unlock()

	if fn, ok := f.Funcs[name]; ok {
		r := fn(args)
		return r.Output, r.Err
	}
	r := f.Results[name]
	return r.Output, r.Err
}

func (f *FakeExecutor) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}
