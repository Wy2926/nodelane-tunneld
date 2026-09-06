package runsecret

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSourceLinuxMemfdIsSealedAndCloseOnExec(t *testing.T) {
	source, err := New(context.Background(), testCredential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	fd, err := strconv.Atoi(strings.TrimPrefix(source.Path(), "/proc/self/fd/"))
	if err != nil {
		t.Fatal("source is not a process memfd")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("memfd is inheritable: flags=%d err=%v", flags, err)
	}
	seals, err := unix.FcntlInt(uintptr(fd), unix.F_GET_SEALS, 0)
	want := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if err != nil || seals&want != want {
		t.Fatalf("memfd is mutable: seals=%d err=%v", seals, err)
	}
	writer, err := os.OpenFile(source.Path(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteAt([]byte("changed"), 0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("sealed memfd write: %v", err)
	}
	assertReadCredential(t, source.Path())
}
