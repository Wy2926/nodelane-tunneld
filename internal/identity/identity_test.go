package identity

import (
	"strings"
	"testing"
)

func TestTokenHashBindsPepperAndToken(t *testing.T) {
	pepper := strings.Repeat("\x0b", 20)
	const token = "Hi There"
	const expected = "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
	if hash := HashToken(pepper, token); !TokenHashEqual(expected, hash) {
		t.Fatalf("token hash = %q, want the HMAC-SHA256 test vector", hash)
	}
	if TokenHashEqual(expected, HashToken("different-pepper", token)) ||
		TokenHashEqual(expected, HashToken(pepper, "different-token")) ||
		TokenHashEqual(expected, "") {
		t.Fatal("changed token, pepper, or missing hash passed verification")
	}
}
