//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var getUserDefaultLocaleName = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

func systemLocale() string {
	const localeNameMaxLength = 85
	buffer := make([]uint16, localeNameMaxLength)
	written, _, _ := getUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if written == 0 {
		return ""
	}
	return windows.UTF16ToString(buffer)
}
