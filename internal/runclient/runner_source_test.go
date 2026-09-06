package runclient

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	frpauth "github.com/fatedier/frp/pkg/auth"
	frpconfig "github.com/fatedier/frp/pkg/config"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/msg"
)

func readMemoryCredential(path string) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() { data, err := os.ReadFile(path); done <- result{data, err} }()
	select {
	case value := <-done:
		return value.data, value.err
	case <-time.After(2 * time.Second):
		return nil, errors.New("credential_read_timeout")
	}
}

func nativeCredentialPath(configText string) (string, error) {
	var config v1.ClientConfig
	if err := frpconfig.LoadConfigure([]byte(configText), &config, true, "toml"); err != nil {
		return "", err
	}
	source := config.Auth.OIDC.TokenSource
	if config.Auth.Method != v1.AuthMethodOIDC || source == nil || source.Type != "file" || source.File == nil || source.File.Path == "" {
		return "", errors.New("missing_memory_credential_source")
	}
	return source.File.Path, nil
}

func TestRunnerKeepsMemoryCredentialUntilEngineClosesAndNeverWritesItToDisk(t *testing.T) {
	bootstrap := testBootstrap(t)
	privateTemp := t.TempDir()
	t.Setenv("TMPDIR", privateTemp)
	t.Setenv("TMP", privateTemp)
	t.Setenv("TEMP", privateTemp)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := newTestEngine()
	var sourcePath string
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(configText string) (Engine, error) {
		var err error
		sourcePath, err = nativeCredentialPath(configText)
		if err != nil {
			t.Error(err)
			return nil, err
		}
		var config v1.ClientConfig
		if err := frpconfig.LoadConfigure([]byte(configText), &config, true, "toml"); err != nil {
			return nil, err
		}
		nativeAuth, err := frpauth.BuildClientAuth(&config.Auth)
		if err != nil {
			return nil, err
		}
		login, ping, work := &msg.Login{}, &msg.Ping{}, &msg.NewWorkConn{}
		if err := nativeAuth.Setter.SetLogin(login); err != nil {
			return nil, err
		}
		if err := nativeAuth.Setter.SetPing(ping); err != nil {
			return nil, err
		}
		if err := nativeAuth.Setter.SetNewWorkConn(work); err != nil {
			return nil, err
		}
		if len(nativeAuth.EncryptionKey()) != 0 || login.PrivilegeKey != "run-secret" || ping.PrivilegeKey != "run-secret" || work.PrivilegeKey != "run-secret" {
			t.Error("stock auth did not use direct in-memory run proof")
		}
		for i := 0; i < 2; i++ {
			data, err := readMemoryCredential(sourcePath)
			if err != nil || string(data) != "run-secret" {
				t.Errorf("memory source not repeatably available before engine construction: %v", err)
			}
		}
		if err := filepath.WalkDir(privateTemp, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "run-secret") {
				t.Error("run credential was written to temporary disk state")
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
		engine.onClose = func() {
			data, err := readMemoryCredential(sourcePath)
			if err != nil || string(data) != "run-secret" {
				t.Errorf("credential source closed before engine: %v", err)
			}
		}
		cancel()
		return engine, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, bootstrap, initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); !errors.Is(err, context.Canceled) {
		t.Fatalf("runner cancellation: %v", err)
	}
	if sourcePath == "" {
		t.Fatal("engine received no memory source")
	}
	if _, err := readMemoryCredential(sourcePath); err == nil || err.Error() == "credential_read_timeout" {
		t.Fatalf("credential source remained readable or blocked after cleanup: %v", err)
	}
}

func TestRunnerClosesMemorySourceWhenEngineConstructionFails(t *testing.T) {
	var sourcePath string
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, EngineFactory: func(configText string) (Engine, error) {
		path, err := nativeCredentialPath(configText)
		if err != nil {
			return nil, err
		}
		sourcePath = path
		return nil, errors.New("raw-engine-secret")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), testBootstrap(t), initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); !errors.Is(err, ErrEngineStopped) || strings.Contains(err.Error(), "raw-engine-secret") {
		t.Fatalf("engine failure: %v", err)
	}
	if sourcePath == "" {
		t.Fatal("engine received no memory source")
	}
	if _, err := readMemoryCredential(sourcePath); err == nil || err.Error() == "credential_read_timeout" {
		t.Fatalf("failed engine retained memory source: %v", err)
	}
}

func TestRunnerRevokesMemorySourceAfterUnconfirmedEngineClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := &unconfirmedEngine{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	defer func() {
		close(engine.release)
		select {
		case <-engine.finished:
		case <-time.After(time.Second):
			t.Error("test engine did not exit")
		}
	}()
	var sourcePath string
	backend := testBackend{heartbeat: func(context.Context, string, string) (HeartbeatResult, error) { return HeartbeatResult{}, nil }, stop: func(context.Context, string, string) (Run, error) { return Run{}, nil }}
	runner, err := NewRunner(RunnerOptions{Backend: backend, CloseTimeout: 30 * time.Millisecond, EngineFactory: func(configText string) (Engine, error) {
		path, err := nativeCredentialPath(configText)
		if err != nil {
			return nil, err
		}
		sourcePath = path
		cancel()
		return engine, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, testBootstrap(t), initialRun(time.Now()), Target{"http", "127.0.0.1", 3000}); !errors.Is(err, ErrCloseUnconfirmed) {
		t.Fatalf("unconfirmed engine close: %v", err)
	}
	if sourcePath == "" {
		t.Fatal("engine received no memory source")
	}
	if _, err := readMemoryCredential(sourcePath); err == nil || err.Error() == "credential_read_timeout" {
		t.Fatalf("unconfirmed engine kept native proof available: %v", err)
	}
}
