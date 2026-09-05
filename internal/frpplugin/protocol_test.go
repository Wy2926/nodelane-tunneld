package frpplugin

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeOfficialLoginRequest(t *testing.T) {
	request, err := DecodeRequest(strings.NewReader(`{
		"version":"0.1.0",
		"op":"Login",
		"content":{
			"version":"0.70.0",
			"client_id":"cli_test",
			"metas":{"tunnel_token":"secret"},
			"client_address":"203.0.113.5:40000"
		}
	}`), "0.1.0", "Login")
	if err != nil {
		t.Fatal(err)
	}
	var content LoginContent
	if err := request.DecodeContent(&content); err != nil {
		t.Fatal(err)
	}
	if content.ClientID != "cli_test" || content.Metas["tunnel_token"] != "secret" {
		t.Fatalf("decoded login content = %#v", content)
	}
}

func TestDecodeRequestRejectsQueryBodyMismatch(t *testing.T) {
	_, err := DecodeRequest(strings.NewReader(`{"version":"0.1.0","op":"Login","content":{}}`), "0.1.0", "Ping")
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("DecodeRequest() error = %v, want mismatch", err)
	}
}

func TestDecodeRequestRejectsAmbiguousOrUnknownEnvelopeFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "duplicate operation", body: `{"version":"0.1.0","op":"Ping","op":"Login","content":{}}`},
		{name: "duplicate nested metadata", body: `{"version":"0.1.0","op":"Login","content":{"metas":{"nodelane_run_id":"first","nodelane_run_id":"second"}}}`},
		{name: "unknown envelope field", body: `{"version":"0.1.0","op":"Login","content":{},"authorization":"secret"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRequest(strings.NewReader(test.body), APIVersion, "Login"); err == nil {
				t.Fatal("DecodeRequest() error = nil, want strict envelope rejection")
			}
		})
	}
}

func TestDecodeRequestRejectsOversizedEnvelope(t *testing.T) {
	body := `{"version":"0.1.0","op":"Login","content":{"padding":"` + strings.Repeat("x", MaxRequestBytes) + `"}}`
	_, err := DecodeRequest(strings.NewReader(body), APIVersion, "Login")
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("DecodeRequest() error = %v, want ErrRequestTooLarge", err)
	}
}

func TestDecodeContentRejectsUnknownAndDuplicateFields(t *testing.T) {
	for _, content := range []string{
		`{"user":"first","user":"second","metas":{}}`,
		`{"user":"","metas":{},"account_access_token":"secret"}`,
	} {
		request := Request{Op: OpLogin, Content: []byte(content)}
		var decoded LoginContent
		if err := request.DecodeContent(&decoded); err == nil {
			t.Fatalf("DecodeContent(%s) error = nil, want strict content rejection", content)
		}
	}
}

func TestDecodeRequestPropagatesReaderFailure(t *testing.T) {
	readerErr := errors.New("reader failed")
	_, err := DecodeRequest(failingReader{err: readerErr}, APIVersion, "Login")
	if !errors.Is(err, readerErr) {
		t.Fatalf("DecodeRequest() error = %v, want reader failure", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

var _ io.Reader = failingReader{}
