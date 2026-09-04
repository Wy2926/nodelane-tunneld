package frpplugin

import (
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
