package runsecret

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const (
	readyPipeInstances = 63
	maxPipeInstances   = 128
	pipeClientTimeout  = 5 * time.Second
)

type deadlinePipe interface {
	io.WriteCloser
	SetWriteDeadline(time.Time) error
}

type windowsSource struct {
	pipePath   string
	pipeName   *uint16
	credential []byte
	listener   net.Listener
	pending    []*pendingPipe
	wake       windows.Handle
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	mu         sync.Mutex
	closed     bool
	active     map[deadlinePipe]struct{}
}

func newMemorySource(credential []byte) (memorySource, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify credential owner: %w", err)
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("name memory credential: %w", err)
	}
	path := `\\.\pipe\nodelane-run-` + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")",
		MessageMode:        true,
		OutputBufferSize:   maxCredentialBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("create memory credential pipe: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		_ = listener.Close()
		return nil, errors.New("invalid memory credential pipe name")
	}
	wake, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("prepare credential cancellation: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	source := &windowsSource{
		pipePath: path, pipeName: name, credential: bytes.Clone(credential),
		listener: listener, wake: wake, ctx: ctx, cancel: cancel, active: make(map[deadlinePipe]struct{}),
	}
	if err := source.replenish(); err != nil {
		source.releasePending()
		_ = listener.Close()
		_ = windows.CloseHandle(wake)
		cancel()
		clear(source.credential)
		return nil, fmt.Errorf("prepare memory credential pipe: %w", err)
	}
	source.wg.Add(1)
	go source.accept()
	return source, nil
}

func (s *windowsSource) path() string { return s.pipePath }

// go-winio's listener prepares only one instance at a time. Stock os.ReadFile
// does not retry a busy pipe. Reserve one of Windows' 64 wait slots for shutdown
// and pre-arm the remaining slots before publishing the path.
func (s *windowsSource) replenish() error {
	s.mu.Lock()
	active := len(s.active)
	closed := s.closed
	s.mu.Unlock()
	for !closed && len(s.pending) < readyPipeInstances && len(s.pending)+active < maxPipeInstances {
		pipe, err := newPendingPipe(s.pipeName)
		if err != nil {
			return err
		}
		s.pending = append(s.pending, pipe)
	}
	return nil
}

func (s *windowsSource) accept() {
	defer s.wg.Done()
	defer s.releasePending()
	for {
		if s.ctx.Err() != nil {
			return
		}
		// A resource-pressure failure leaves existing clients usable and retries
		// after a completion, cancellation, or the bounded wait below.
		_ = s.replenish()
		events := make([]windows.Handle, 1, len(s.pending)+1)
		events[0] = s.wake
		for _, pipe := range s.pending {
			events = append(events, pipe.operation.HEvent)
		}
		event, err := windows.WaitForMultipleObjects(events, false, 1000)
		if err != nil {
			return
		}
		if event == uint32(windows.WAIT_TIMEOUT) || event == windows.WAIT_OBJECT_0 {
			continue
		}
		index := int(event-windows.WAIT_OBJECT_0) - 1
		if index < 0 || index >= len(s.pending) {
			return
		}
		pipe := s.pending[index]
		s.pending[index] = s.pending[len(s.pending)-1]
		s.pending = s.pending[:len(s.pending)-1]
		if pipe.complete() != nil {
			pipe.close()
			continue
		}
		handle := pipe.handle
		pipe.handle = 0
		pipe.close()
		if !s.acceptClient(handle) {
			_ = windows.CloseHandle(handle)
		}
	}
}

func (s *windowsSource) acceptClient(handle windows.Handle) bool {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(handle, &pid); err != nil || pid != uint32(os.Getpid()) {
		return false
	}
	file, err := winio.NewOpenFile(handle)
	if err != nil {
		return false
	}
	pipe, ok := file.(deadlinePipe)
	if !ok {
		_ = file.Close()
		return true
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = pipe.Close()
		return true
	}
	s.active[pipe] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go s.serve(pipe)
	return true
}

func (s *windowsSource) serve(pipe deadlinePipe) {
	defer s.wg.Done()
	defer func() {
		_ = pipe.Close()
		s.mu.Lock()
		delete(s.active, pipe)
		s.mu.Unlock()
		_ = windows.SetEvent(s.wake)
	}()
	deadline := time.Now().Add(pipeClientTimeout)
	if pipe.SetWriteDeadline(deadline) != nil {
		return
	}
	// Full Close preserves buffered bytes for the reader and then delivers EOF.
	// Disconnect must only be used for rejected or canceled pending instances.
	if n, err := pipe.Write(s.credential); err != nil || n != len(s.credential) {
		return
	}
}

func (s *windowsSource) releasePending() {
	for _, pipe := range s.pending {
		pipe.close()
	}
	s.pending = nil
}

func (s *windowsSource) close() error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	pipes := make([]deadlinePipe, 0, len(s.active))
	for pipe := range s.active {
		pipes = append(pipes, pipe)
	}
	s.mu.Unlock()
	_ = windows.SetEvent(s.wake)
	_ = s.listener.Close()
	for _, pipe := range pipes {
		_ = pipe.Close()
	}
	s.wg.Wait()
	_ = windows.CloseHandle(s.wake)
	clear(s.credential)
	return nil
}
