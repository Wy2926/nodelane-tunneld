package runsecret

import (
	"context"
	"errors"
	"sync"
)

const maxCredentialBytes = 4096

type memorySource interface {
	path() string
	close() error
}

// Source exposes a per-run credential through a memory-only ReadFile path.
// Callers own the lifetime and must Close after the last credential consumer.
type Source struct {
	state *sourceState
}

type sourceState struct {
	backend memorySource
	done    chan struct{}
	once    sync.Once
	err     error
}

// New copies credential into an OS-backed memory source. No disk file is created.
func New(ctx context.Context, credential string) (*Source, error) {
	if ctx == nil {
		return nil, errors.New("run credential source requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(credential) == 0 || len(credential) > maxCredentialBytes {
		return nil, errors.New("invalid run credential size")
	}
	data := []byte(credential)
	defer clear(data)
	backend, err := newMemorySource(data)
	if err != nil {
		return nil, err
	}
	source := &Source{state: &sourceState{backend: backend, done: make(chan struct{})}}
	if err := ctx.Err(); err != nil {
		_ = source.Close()
		return nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = source.Close()
		case <-source.state.done:
		}
	}()
	return source, nil
}

// Path is consumed by stock frpc's type=file ValueSource in the same process.
func (s *Source) Path() string {
	if s == nil || s.state == nil {
		return ""
	}
	return s.state.backend.path()
}

// Close is idempotent and releases the source, canceling pending pipe I/O.
func (s *Source) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	s.state.once.Do(func() {
		close(s.state.done)
		s.state.err = s.state.backend.close()
	})
	return s.state.err
}

func (Source) String() string   { return "runsecret.Source(<redacted>)" }
func (Source) GoString() string { return "runsecret.Source(<redacted>)" }
