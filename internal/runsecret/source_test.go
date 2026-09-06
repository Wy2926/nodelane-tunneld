//go:build linux || windows

package runsecret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testCredential = "synthetic-run-credential-only-0000000000000000000000000000000000000000000000"

func TestSourceReadFileRepeatedAndConcurrent(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for i := 0; i < 3; i++ {
		assertReadCredential(t, source.Path())
	}
	const readers = 20
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 10; j++ {
				data, readErr := os.ReadFile(source.Path())
				if readErr != nil {
					errs <- readErr
					return
				}
				if string(data) != testCredential {
					errs <- fmt.Errorf("credential changed or incomplete")
					return
				}
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent ReadFile did not finish")
	}
	close(errs)
	for readErr := range errs {
		t.Error(readErr)
	}
}

func TestSourceCloseRejectsReads(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	path := source.Path()
	assertReadCredential(t, path)
	done := make(chan error, 1)
	go func() { done <- source.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("closed source remains readable")
	}
}

func TestSourceCancellationClosesSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source, err := New(ctx, testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	path := source.Path()
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.ReadFile(path); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled source remains readable")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSourceRejectsInvalidInput(t *testing.T) {
	for _, credential := range []string{"", strings.Repeat("x", 4097)} {
		if source, err := New(context.Background(), credential); err == nil {
			_ = source.Close()
			t.Fatal("invalid credential accepted")
		}
	}
	if source, err := New(nil, testCredential); err == nil {
		_ = source.Close()
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if source, err := New(ctx, testCredential); err != context.Canceled {
		if source != nil {
			_ = source.Close()
		}
		t.Fatalf("canceled context: %v", err)
	}
}

func TestSourceSupportsMaximumCredential(t *testing.T) {
	credential := strings.Repeat("x", 4096)
	source, err := New(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	data, err := os.ReadFile(source.Path())
	if err != nil || string(data) != credential {
		t.Fatalf("maximum-size credential: len=%d err=%v", len(data), err)
	}
}

func TestSourceDoesNotPersistOrFormatCredential(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TEMP", tempDir)
	t.Setenv("TMP", tempDir)
	t.Setenv("TMPDIR", tempDir)
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	assertReadCredential(t, source.Path())
	for _, value := range []any{source, *source} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, value), testCredential) {
				t.Fatal("formatting exposes credential")
			}
		}
	}
	if err := filepath.WalkDir(tempDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return fmt.Errorf("credential source created an unexpected disk file")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertReadCredential(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != testCredential {
		t.Fatal("credential changed or incomplete")
	}
}
