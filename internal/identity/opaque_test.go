package identity

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	literalLaunchID        = "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	literalRunCredentialID = "nrc_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	literalSecret          = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	literalLaunchToken     = literalLaunchID + "." + literalSecret
	literalRunToken        = literalRunCredentialID + "." + literalSecret
)

func TestOpaqueIDsHaveIndependentNamespacesAnd128Bits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prefix   string
		generate func() (string, error)
	}{
		{"route", "rte_", NewRouteID},
		{"run", "run_", NewRunID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(map[string]bool)
			for range 32 {
				id, err := tc.generate()
				if err != nil {
					t.Fatal(err)
				}
				assertOpaqueIDFormat(t, id, tc.prefix)
				if seen[id] {
					t.Fatal("generated duplicate ID")
				}
				seen[id] = true
			}
		})
	}
}

func TestOpaqueCredentialsGenerateScoped256BitSecrets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prefix   string
		generate func() (OpaqueCredential, error)
		parse    func(string) (string, error)
	}{
		{"launch", "nlc_", NewLaunchCredential, ParseLaunchCredential},
		{"run", "nrc_", NewRunCredential, ParseRunCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seenIDs, seenSecrets := map[string]bool{}, map[string]bool{}
			for range 32 {
				credential, err := tc.generate()
				if err != nil {
					t.Fatal(err)
				}
				assertOpaqueIDFormat(t, credential.ID, tc.prefix)
				parts := strings.Split(credential.Token, ".")
				if len(parts) != 2 || parts[0] != credential.ID {
					t.Fatal("token must contain public ID and exactly one secret")
				}
				secret, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
				if err != nil || len(secret) != 32 || len(parts[1]) != 43 {
					t.Fatal("secret must canonically encode 32 random bytes")
				}
				if seenIDs[credential.ID] || seenSecrets[parts[1]] {
					t.Fatal("generated repeated ID or secret")
				}
				seenIDs[credential.ID], seenSecrets[parts[1]] = true, true
				parsed, err := tc.parse(credential.Token)
				if err != nil || parsed != credential.ID {
					t.Fatalf("parse got %q, %v", parsed, err)
				}
			}
		})
	}
}

func TestOpaqueCredentialParsersAcceptLiteralFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, token, want string
		parse             func(string) (string, error)
	}{
		{"launch", literalLaunchToken, literalLaunchID, ParseLaunchCredential},
		{"run", literalRunToken, literalRunCredentialID, ParseRunCredential},
		{"url alphabet", "nrc_abcdefghijklmnopqrstuvwxyz.__________________________________________8", "nrc_abcdefghijklmnopqrstuvwxyz", ParseRunCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.parse(tc.token)
			if err != nil || id != tc.want {
				t.Fatalf("parse got %q, %v; want %q", id, err, tc.want)
			}
		})
	}
}

func TestOpaqueCredentialParsersRejectMalformedTokens(t *testing.T) {
	for _, parser := range []struct {
		name, id, other string
		parse           func(string) (string, error)
	}{
		{"launch", literalLaunchID, literalRunToken, ParseLaunchCredential},
		{"run", literalRunCredentialID, literalLaunchToken, ParseRunCredential},
	} {
		valid := parser.id + "." + literalSecret
		cases := map[string]string{
			"empty": "", "other namespace": parser.other,
			"legacy":        "ftc.ctk_aaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"legacy ID":     "ctk_aaaaaaaaaaaaaaaaaaaaaaaaaa." + literalSecret,
			"leading space": " " + valid, "trailing space": valid + " ",
			"secret newline": parser.id + ".AAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAA",
			"no dot":         parser.id + literalSecret, "extra dot": valid + ".", "empty secret": parser.id + ".",
			"short ID":                   parser.id[:29] + "." + literalSecret,
			"long ID":                    parser.id + "a." + literalSecret,
			"uppercase ID":               strings.ToUpper(parser.id) + "." + literalSecret,
			"invalid base32":             parser.id[:29] + "0." + literalSecret,
			"unicode ID":                 parser.id[:29] + "\u00e9." + literalSecret,
			"padded secret":              valid + "=",
			"noncanonical trailing bits": parser.id + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB",
			"standard base64":            parser.id + ".//////////////////////////////////////////8",
			"invalid base64":             parser.id + ".!AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"31 byte secret":             parser.id + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"33 byte secret":             parser.id + ".AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		}
		for name, token := range cases {
			t.Run(parser.name+"/"+name, func(t *testing.T) {
				id, err := parser.parse(token)
				if id != "" || !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("malformed token returned ID %q, error %v", id, err)
				}
			})
		}
	}
}

func TestOpaqueCredentialJSONExcludesToken(t *testing.T) {
	encoded, err := json.Marshal(OpaqueCredential{ID: literalLaunchID, Token: literalLaunchToken})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"ID":"nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa"}` {
		t.Fatalf("unexpected credential JSON: %s", encoded)
	}
}

func TestOpaqueCredentialHMACBindsFullTokenAndPepper(t *testing.T) {
	hash := HashToken("launch-test-pepper", literalLaunchToken)
	if !TokenHashEqual(hash, HashToken("launch-test-pepper", literalLaunchToken)) {
		t.Fatal("same full token should match")
	}
	for _, tc := range []struct{ pepper, token string }{
		{"run-test-pepper", literalLaunchToken},
		{"launch-test-pepper", literalRunToken},
		{"launch-test-pepper", literalLaunchID + ".__________________________________________8"},
	} {
		if TokenHashEqual(hash, HashToken(tc.pepper, tc.token)) {
			t.Fatal("different pepper, namespace, or secret must not match")
		}
	}
}

func assertOpaqueIDFormat(t *testing.T, id, prefix string) {
	t.Helper()
	if !strings.HasPrefix(id, prefix) || len(id) != 30 || strings.ToLower(id) != id {
		t.Fatalf("unexpected opaque ID format %q", id)
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(id[4:]))
	if err != nil || len(decoded) != 16 {
		t.Fatalf("ID must encode 16 random bytes: %q, %v", id, err)
	}
}
