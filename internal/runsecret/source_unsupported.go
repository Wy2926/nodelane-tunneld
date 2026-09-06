//go:build !linux && !windows

package runsecret

import "errors"

func newMemorySource([]byte) (memorySource, error) {
	return nil, errors.New("memory-only run credentials are unavailable on this operating system")
}
