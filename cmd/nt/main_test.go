package main

import "testing"

func TestSafeConsoleFieldRemovesControlCharacters(t *testing.T) {
	if got := safeConsoleField("203.0.113.9\x1b[31m\n", 64); got != "203.0.113.9[31m" {
		t.Fatalf("safe console field = %q", got)
	}
}
