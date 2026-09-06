package runsecret

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSourceWindowsCloseUnblocksIdleReaders(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	files := make([]*os.File, 0, 20)
	for i := 0; i < 20; i++ {
		file, err := os.Open(source.Path())
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		t.Cleanup(func() { _ = file.Close() })
	}
	// Confirm these clients reached live writers, not only pending listeners.
	for _, file := range files {
		var first [1]byte
		if _, err := file.Read(first[:]); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- source.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked on readers that never consume data")
	}
	for _, file := range files {
		readDone := make(chan struct{})
		go func() {
			_, _ = file.Read(make([]byte, 8192))
			close(readDone)
		}()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatal("reader blocked after source Close")
		}
	}
}

func TestSourceWindowsPipeUsesProtectedCurrentUserDACL(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	file, err := os.Open(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := descriptor.String(), "D:P(A;;FA;;;"+user.User.Sid.String()+")"; got != want {
		t.Fatalf("pipe has unexpected access permissions: %s", got)
	}
}

func TestSourceWindowsCloseClearsOwnedCredential(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	owned := source.state.backend.(*windowsSource).credential
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Trim(string(owned), "\x00") != "" {
		t.Fatal("owned credential buffer not cleared on Close")
	}
}

func TestSourceWindowsRefusesAnotherProcess(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSourceWindowsForeignProcessHelper$")
	cmd.Env = append(os.Environ(), "NODELANE_RUNSECRET_TEST_PIPE="+source.Path())
	if err := cmd.Run(); err != nil {
		t.Fatalf("foreign process received data or did not finish: %v", err)
	}
	assertReadCredential(t, source.Path())
}

func TestSourceWindowsForeignProcessHelper(t *testing.T) {
	path := os.Getenv("NODELANE_RUNSECRET_TEST_PIPE")
	if path == "" {
		return
	}
	data, _ := os.ReadFile(path)
	if len(data) != 0 {
		t.Fatal("source disclosed bytes to another process")
	}
}
