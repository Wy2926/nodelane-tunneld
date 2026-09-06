//go:build windows

package cliauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func systemLocker(options SystemStoreOptions) func(context.Context) (func(), error) {
	return func(ctx context.Context) (func(), error) {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return nil, ErrCredentialsUnavailable
		}
		sid := user.User.Sid.String()
		sum := sha256.Sum256([]byte(sid + "\x00" + options.Service + "\x00" + options.Account))
		name, err := windows.UTF16PtrFromString("Global\\NodeLane.nt." + hex.EncodeToString(sum[:]))
		if err != nil {
			return nil, ErrCredentialsUnavailable
		}
		descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")")
		if err != nil {
			return nil, ErrCredentialsUnavailable
		}
		attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
		handle, err := windows.CreateMutexEx(&attributes, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
		if handle == 0 || (err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS)) {
			return nil, ErrCredentialsUnavailable
		}
		// Windows mutex ownership is thread-affine even when a goroutine migrates.
		runtime.LockOSThread()
		for {
			if err = ctx.Err(); err != nil {
				windows.CloseHandle(handle)
				runtime.UnlockOSThread()
				return nil, err
			}
			status, waitErr := windows.WaitForSingleObject(handle, 50)
			if waitErr != nil {
				windows.CloseHandle(handle)
				runtime.UnlockOSThread()
				return nil, ErrCredentialsUnavailable
			}
			switch status {
			case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
				return func() { _ = windows.ReleaseMutex(handle); _ = windows.CloseHandle(handle); runtime.UnlockOSThread() }, nil
			case uint32(windows.WAIT_TIMEOUT):
			default:
				windows.CloseHandle(handle)
				runtime.UnlockOSThread()
				return nil, ErrCredentialsUnavailable
			}
		}
	}
}
