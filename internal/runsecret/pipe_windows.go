package runsecret

import (
	"errors"

	"golang.org/x/sys/windows"
)

type pendingPipe struct {
	handle    windows.Handle
	operation windows.Overlapped
	waiting   bool
}

func newPendingPipe(name *uint16) (*pendingPipe, error) {
	handle, err := windows.CreateNamedPipe(name,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, maxCredentialBytes, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	pipe := &pendingPipe{handle: handle, operation: windows.Overlapped{HEvent: event}}
	err = windows.ConnectNamedPipe(handle, &pipe.operation)
	switch {
	case errors.Is(err, windows.ERROR_IO_PENDING):
		pipe.waiting = true
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		_ = windows.SetEvent(event)
	default:
		pipe.close()
		return nil, err
	}
	return pipe, nil
}

func (p *pendingPipe) complete() error {
	if !p.waiting {
		return nil
	}
	var transferred uint32
	err := windows.GetOverlappedResult(p.handle, &p.operation, &transferred, false)
	if !errors.Is(err, windows.ERROR_IO_INCOMPLETE) {
		p.waiting = false
	}
	return err
}

func (p *pendingPipe) close() {
	if p.waiting {
		// OVERLAPPED memory and its event must outlive the kernel's completion.
		_ = windows.CancelIoEx(p.handle, &p.operation)
		var transferred uint32
		_ = windows.GetOverlappedResult(p.handle, &p.operation, &transferred, true)
		p.waiting = false
	}
	if p.handle != 0 {
		_ = windows.CloseHandle(p.handle)
		p.handle = 0
	}
	if p.operation.HEvent != 0 {
		_ = windows.CloseHandle(p.operation.HEvent)
		p.operation.HEvent = 0
	}
}
