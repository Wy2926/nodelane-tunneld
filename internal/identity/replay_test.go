package identity

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"strings"
	"testing"
	"time"
)

var replayTestKey = []byte("0123456789abcdef0123456789abcdef")

func replayTestContext() ReplayContext {
	return ReplayContext{
		Operation: "start_run", PrincipalKey: "account-123",
		KeyHash:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		RequestHash: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		RouteID:     "rte_aaaaaaaaaaaaaaaaaaaaaaaaaa", RunID: "run_aaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExpiresAt: time.Date(2026, 9, 5, 10, 2, 0, 123456000, time.UTC),
	}
}

func newTestReplayCipher(t *testing.T) *ReplayCipher {
	t.Helper()
	c, err := NewReplayCipher(replayTestKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReplayCipherRequires256BitKey(t *testing.T) {
	for _, size := range []int{0, 1, 16, 24, 31, 33, 64} {
		c, err := NewReplayCipher(make([]byte, size))
		if c != nil || !errors.Is(err, ErrInvalidReplayKey) {
			t.Fatalf("key size %d returned cipher %v, error %v", size, c, err)
		}
	}
}

func TestReplayCipherPreservesExactBytesAndUsesFreshNonces(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	plaintext := []byte(`{"token":"nrc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","status":"starting"}` + "\n")
	first, err := c.Seal(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Seal(ctx, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("sealing reused an envelope")
	}
	if bytes.Contains(first, plaintext) || bytes.Contains(first, []byte(literalRunToken)) {
		t.Fatal("ciphertext exposed plaintext")
	}
	for _, envelope := range [][]byte{first, second} {
		opened, err := c.Open(ctx, envelope, ctx.ExpiresAt.Add(-time.Microsecond))
		if err != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("roundtrip changed bytes: %q, %v", opened, err)
		}
	}
}

func TestReplayCipherAuthenticatesIndependentLiteralJSONContext(t *testing.T) {
	ctx := replayTestContext()
	// This fixture does not marshal ReplayContext: it detects missing tags, fields, and UTC normalization.
	aad := []byte(`{"operation":"start_run","principal_key":"account-123","key_hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","request_hash":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789","route_id":"rte_aaaaaaaaaaaaaaaaaaaaaaaaaa","run_id":"run_aaaaaaaaaaaaaaaaaaaaaaaaaa","expires_at":"2026-09-05T10:02:00.123456Z"}`)
	block, err := aes.NewCipher(replayTestKey)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("fixed-nonce!")
	plaintext := []byte(`{"token":"nrc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`)
	envelope := aead.Seal(append([]byte(nil), nonce...), nonce, plaintext, aad)
	opened, err := newTestReplayCipher(t).Open(ctx, envelope, ctx.ExpiresAt.Add(-time.Second))
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("literal AAD fixture failed: %q, %v", opened, err)
	}
}

func TestReplayCipherBindsEveryContextField(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	envelope, err := c.Seal(ctx, []byte("private response"))
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*ReplayContext){
		"operation":    func(v *ReplayContext) { v.Operation = "redeem_launch" },
		"principal":    func(v *ReplayContext) { v.PrincipalKey = "account-456" },
		"key hash":     func(v *ReplayContext) { v.KeyHash = strings.Repeat("1", 64) },
		"request hash": func(v *ReplayContext) { v.RequestHash = strings.Repeat("2", 64) },
		"route":        func(v *ReplayContext) { v.RouteID = "rte_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"run":          func(v *ReplayContext) { v.RunID = "run_bbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"expiry":       func(v *ReplayContext) { v.ExpiresAt = v.ExpiresAt.Add(time.Microsecond) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed := ctx
			change(&changed)
			plaintext, err := c.Open(changed, envelope, ctx.ExpiresAt.Add(-time.Second))
			if !errors.Is(err, ErrInvalidReplayCiphertext) || len(plaintext) != 0 {
				t.Fatalf("mutated field returned plaintext %q, error %v", plaintext, err)
			}
		})
	}
}

func TestReplayCipherRejectsWrongKeysAndBrokenEnvelopes(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	envelope, err := c.Seal(ctx, []byte("private response"))
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := NewReplayCipher([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := wrong.Open(ctx, envelope, ctx.ExpiresAt.Add(-time.Second))
	if !errors.Is(err, ErrInvalidReplayCiphertext) || len(plaintext) != 0 {
		t.Fatalf("wrong key returned plaintext %q, error %v", plaintext, err)
	}
	for size := 0; size < len(envelope); size++ {
		plaintext, err := c.Open(ctx, envelope[:size], ctx.ExpiresAt.Add(-time.Second))
		if !errors.Is(err, ErrInvalidReplayCiphertext) || len(plaintext) != 0 {
			t.Fatalf("truncation at %d returned plaintext %q, error %v", size, plaintext, err)
		}
	}
	for index := range envelope {
		broken := bytes.Clone(envelope)
		broken[index] ^= 1
		plaintext, err := c.Open(ctx, broken, ctx.ExpiresAt.Add(-time.Second))
		if !errors.Is(err, ErrInvalidReplayCiphertext) || len(plaintext) != 0 {
			t.Fatalf("mutation at %d returned plaintext %q, error %v", index, plaintext, err)
		}
	}
	plaintext, err = c.Open(ctx, append(bytes.Clone(envelope), 0), ctx.ExpiresAt.Add(-time.Second))
	if !errors.Is(err, ErrInvalidReplayCiphertext) || len(plaintext) != 0 {
		t.Fatalf("extra envelope byte returned plaintext %q, error %v", plaintext, err)
	}
}

func TestReplayCipherExpiryIsAbsoluteAndNeverRenewed(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	original := ctx
	envelope, err := c.Seal(ctx, []byte("private response"))
	if err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{ctx.ExpiresAt.Add(-time.Minute), ctx.ExpiresAt.Add(-time.Nanosecond)} {
		plaintext, err := c.Open(ctx, envelope, now)
		if err != nil || string(plaintext) != "private response" {
			t.Fatalf("before expiry returned %q, %v", plaintext, err)
		}
	}
	for _, now := range []time.Time{ctx.ExpiresAt, ctx.ExpiresAt.Add(time.Nanosecond), ctx.ExpiresAt.Add(time.Hour)} {
		plaintext, err := c.Open(ctx, envelope, now)
		if !errors.Is(err, ErrReplayExpired) || len(plaintext) != 0 {
			t.Fatalf("expired replay returned %q, %v", plaintext, err)
		}
	}
	if ctx != original {
		t.Fatal("replay changed original context")
	}
}

func TestReplayCipherNormalizesEquivalentExpiryTimeZones(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	local := ctx
	local.ExpiresAt = ctx.ExpiresAt.In(time.FixedZone("UTC+8", 8*60*60))
	for _, pair := range [][2]ReplayContext{{ctx, local}, {local, ctx}} {
		envelope, err := c.Seal(pair[0], []byte("private response"))
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := c.Open(pair[1], envelope, ctx.ExpiresAt.Add(-time.Second))
		if err != nil || string(plaintext) != "private response" {
			t.Fatalf("equivalent zone returned %q, %v", plaintext, err)
		}
	}
}

func TestReplayCipherRejectsInvalidContexts(t *testing.T) {
	c, ctx := newTestReplayCipher(t), replayTestContext()
	envelope, err := c.Seal(ctx, []byte("private response"))
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*ReplayContext){
		"empty operation":        func(v *ReplayContext) { v.Operation = "" },
		"unsupported operation":  func(v *ReplayContext) { v.Operation = "delete_route" },
		"oversized operation":    func(v *ReplayContext) { v.Operation = strings.Repeat("a", 257) },
		"empty principal":        func(v *ReplayContext) { v.PrincipalKey = "" },
		"invalid UTF8 principal": func(v *ReplayContext) { v.PrincipalKey = "account-\xff" },
		"oversized principal":    func(v *ReplayContext) { v.PrincipalKey = strings.Repeat("a", 257) },
		"principal byte limit":   func(v *ReplayContext) { v.PrincipalKey = strings.Repeat("\u00e9", 129) },
		"empty key hash":         func(v *ReplayContext) { v.KeyHash = "" },
		"short key hash":         func(v *ReplayContext) { v.KeyHash = strings.Repeat("a", 63) },
		"long key hash":          func(v *ReplayContext) { v.KeyHash = strings.Repeat("a", 65) },
		"uppercase key hash":     func(v *ReplayContext) { v.KeyHash = strings.Repeat("A", 64) },
		"nonhex key hash":        func(v *ReplayContext) { v.KeyHash = strings.Repeat("g", 64) },
		"empty request hash":     func(v *ReplayContext) { v.RequestHash = "" },
		"short request hash":     func(v *ReplayContext) { v.RequestHash = strings.Repeat("a", 63) },
		"long request hash":      func(v *ReplayContext) { v.RequestHash = strings.Repeat("a", 65) },
		"uppercase request hash": func(v *ReplayContext) { v.RequestHash = strings.Repeat("A", 64) },
		"nonhex request hash":    func(v *ReplayContext) { v.RequestHash = strings.Repeat("g", 64) },
		"empty route":            func(v *ReplayContext) { v.RouteID = "" },
		"wrong route namespace":  func(v *ReplayContext) { v.RouteID = v.RunID },
		"invalid route alphabet": func(v *ReplayContext) { v.RouteID = "rte_00000000000000000000000000" },
		"short route":            func(v *ReplayContext) { v.RouteID = "rte_aaa" },
		"oversized route":        func(v *ReplayContext) { v.RouteID += "a" },
		"empty start run":        func(v *ReplayContext) { v.RunID = "" },
		"empty redeem run":       func(v *ReplayContext) { v.Operation = "redeem_launch"; v.RunID = "" },
		"wrong run namespace":    func(v *ReplayContext) { v.RunID = v.RouteID },
		"invalid run alphabet":   func(v *ReplayContext) { v.RunID = "run_00000000000000000000000000" },
		"short run":              func(v *ReplayContext) { v.RunID = "run_aaa" },
		"oversized run":          func(v *ReplayContext) { v.RunID += "a" },
		"invalid optional run":   func(v *ReplayContext) { v.Operation = "create_route"; v.RunID = "invalid" },
		"zero expiry":            func(v *ReplayContext) { v.ExpiresAt = time.Time{} },
		"submicrosecond expiry":  func(v *ReplayContext) { v.ExpiresAt = v.ExpiresAt.Add(time.Nanosecond) },
		"unmarshalable expiry":   func(v *ReplayContext) { v.ExpiresAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			invalid := ctx
			change(&invalid)
			sealed, err := c.Seal(invalid, []byte("private response"))
			if !errors.Is(err, ErrInvalidReplayContext) || len(sealed) != 0 {
				t.Fatalf("invalid seal returned %x, %v", sealed, err)
			}
			plaintext, err := c.Open(invalid, envelope, ctx.ExpiresAt.Add(-time.Second))
			if !errors.Is(err, ErrInvalidReplayContext) || len(plaintext) != 0 {
				t.Fatalf("invalid open returned %q, %v", plaintext, err)
			}
		})
	}
}

func TestReplayCipherAcceptsSupportedOperationAssociations(t *testing.T) {
	c := newTestReplayCipher(t)
	for _, operation := range []string{"create_route", "start_run", "redeem_launch"} {
		t.Run(operation, func(t *testing.T) {
			ctx := replayTestContext()
			ctx.Operation = operation
			ctx.PrincipalKey = strings.Repeat("p", 256)
			if operation == "create_route" {
				ctx.RunID = ""
			}
			envelope, err := c.Seal(ctx, []byte("private response"))
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := c.Open(ctx, envelope, ctx.ExpiresAt.Add(-time.Second))
			if err != nil || string(plaintext) != "private response" {
				t.Fatalf("supported operation returned %q, %v", plaintext, err)
			}
		})
	}
}
