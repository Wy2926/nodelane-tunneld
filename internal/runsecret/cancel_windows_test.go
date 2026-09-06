package runsecret

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSourceWindowsLateReaderCancellationIsBounded(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSourceWindowsLateReaderCancellationHelper$", "-test.timeout=8s")
	cmd.Env = append(os.Environ(), "NODELANE_TEST_LATE_READER=1")
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("late reader cancellation failed or hung: %v\n%s", err, output)
	}
}

func TestSourceWindowsLateReaderCancellationHelper(t *testing.T) {
	if os.Getenv("NODELANE_TEST_LATE_READER") != "1" {
		return
	}
	t.Run("prime-concurrency", TestSourceReadFileRepeatedAndConcurrent)
	t.Run("prime-close", TestSourceCloseRejectsReads)
	runtime.GC()
	for i := 0; i < 32; i++ {
		t.Run("connected-before-accept", func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			pipe, path := newTestPendingPipe(t)
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			status, err := windows.WaitForSingleObject(pipe.operation.HEvent, 1000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatalf("late connection did not complete: status=%d err=%v", status, err)
			}
			var serverReference windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), pipe.handle, windows.CurrentProcess(), &serverReference, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(serverReference)
			finished := make(chan struct{})
			go func() {
				time.Sleep(time.Millisecond)
				pipe.close()
				close(finished)
			}()
			var data [1]byte
			_, _ = file.Read(data[:])
			<-finished
		})
	}
	for i := 0; i < 128; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		source, err := New(ctx, testCredential)
		if err != nil {
			t.Fatal(err)
		}
		path := source.Path()
		cancel()
		_, _ = os.ReadFile(path)
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
