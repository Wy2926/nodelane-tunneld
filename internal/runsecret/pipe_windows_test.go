package runsecret

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestPendingPipeConnectSignalsItsOverlappedEvent(t *testing.T) {
	pipe, path := newTestPendingPipe(t)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	status, err := windows.WaitForSingleObject(pipe.operation.HEvent, 1000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("connect completion did not signal its event: status=%d err=%v", status, err)
	}
	if err := pipe.complete(); err != nil {
		t.Fatal(err)
	}
}

func TestPendingPipeCloseCancelsConnectAndReleasesHandles(t *testing.T) {
	pipe, _ := newTestPendingPipe(t)
	compare := windows.NewLazySystemDLL("kernelbase.dll").NewProc("CompareObjectHandles")
	if err := compare.Find(); err != nil {
		t.Fatal(err)
	}
	handles := []windows.Handle{pipe.handle, pipe.operation.HEvent}
	references := make([]windows.Handle, len(handles))
	for i, handle := range handles {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &references[i], 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(references[i]) })
	}
	done := make(chan struct{})
	go func() { pipe.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending asynchronous connect did not cancel")
	}
	for i, handle := range handles {
		var duplicate windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err == nil {
			// The runtime can reuse a closed handle number; compare kernel objects.
			same, _, _ := compare.Call(uintptr(duplicate), uintptr(references[i]))
			_ = windows.CloseHandle(duplicate)
			if same != 0 {
				t.Fatal("pipe or connect-event handle leaked")
			}
		}
	}
}

func newTestPendingPipe(t *testing.T) (*pendingPipe, string) {
	t.Helper()
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	path := `\\.\pipe\nodelane-runsecret-test-` + hex.EncodeToString(nonce[:])
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{MessageMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := newPendingPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pipe.close)
	return pipe, path
}
