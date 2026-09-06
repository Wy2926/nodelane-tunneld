package runsecret

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestPipeClosePreservesBufferedCredentialAndEOF(t *testing.T) {
	for i := 0; i < 20; i++ {
		for _, credential := range []string{testCredential, strings.Repeat("x", 4096)} {
			t.Run(fmt.Sprintf("%d/%d", i, len(credential)), func(t *testing.T) {
				t.Parallel()
				assertClosedPipeCredential(t, credential)
			})
		}
	}
}

func assertClosedPipeCredential(t *testing.T, credential string) {
	t.Helper()
	pipe, path := newTestPendingPipe(t)
	client, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if status, err := windows.WaitForSingleObject(pipe.operation.HEvent, 1000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("connect event: %d %v", status, err)
	}
	if err := pipe.complete(); err != nil {
		t.Fatal(err)
	}
	server, err := winio.NewOpenFile(pipe.handle)
	if err != nil {
		t.Fatal(err)
	}
	pipe.handle = 0
	pipe.close()
	t.Cleanup(func() { _ = server.Close() })
	written := make(chan error, 1)
	go func() {
		_, err := server.Write([]byte(credential))
		_ = server.Close()
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded credential was not buffered before the reader started")
	}
	data, err := io.ReadAll(client)
	if err != nil || string(data) != credential {
		t.Fatalf("closed writer lost buffered credential or EOF: size=%d err=%v", len(data), err)
	}
}

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
