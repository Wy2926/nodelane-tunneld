//go:build !windows

package main

func systemLocale() string {
	// Unix-like systems expose the process locale through LC_* or LANG.
	return ""
}
