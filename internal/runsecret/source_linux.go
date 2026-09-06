package runsecret

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

type linuxSource struct {
	file     *os.File
	filePath string
}

func newMemorySource(credential []byte) (memorySource, error) {
	fd, err := unix.MemfdCreate("nodelane-run-credential", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create memory credential: %w", err)
	}
	file := os.NewFile(uintptr(fd), "nodelane-run-credential")
	success := false
	defer func() {
		if !success {
			_ = file.Close()
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return nil, fmt.Errorf("restrict memory credential: %w", err)
	}
	if _, err := file.Write(credential); err != nil {
		return nil, fmt.Errorf("populate memory credential: %w", err)
	}
	seals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return nil, fmt.Errorf("seal memory credential: %w", err)
	}
	success = true
	return &linuxSource{file: file, filePath: "/proc/self/fd/" + strconv.Itoa(fd)}, nil
}

func (s *linuxSource) path() string { return s.filePath }
func (s *linuxSource) close() error { return s.file.Close() }
