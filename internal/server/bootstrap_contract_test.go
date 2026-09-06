package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const bootstrapFixtureVersion = "9.9.9-fixture"

func TestBootstrapVerifiedReleaseForwarding(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("installers support Windows and Linux")
	}
	archive, asset := bootstrapArchive(t)
	modes := []string{"posix-pipe"}
	if runtime.GOOS == "windows" {
		modes = []string{"cmd-install", "cmd-pipe", "powershell-file", "powershell-block"}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "install paths with spaces")
			for _, dir := range []string{root, filepath.Join(root, "tmp"), filepath.Join(root, "home")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			requests := map[string]int{}
			digest := fmt.Sprintf("%x", sha256.Sum256(archive))
			release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests[r.URL.Path]++
				mu.Unlock()
				switch r.URL.Path {
				case "/stable.txt":
					_, _ = io.WriteString(w, bootstrapFixtureVersion+"\n")
				case "/" + bootstrapFixtureVersion + "/" + asset:
					_, _ = w.Write(archive)
				case "/" + bootstrapFixtureVersion + "/" + asset + ".sha256":
					_, _ = fmt.Fprintf(w, "%s  %s\n", digest, asset)
				case "/install.cmd":
					PublicHandler("").ServeHTTP(w, r)
				default:
					http.NotFound(w, r)
				}
			}))
			defer release.Close()
			args := []string{"anonymous", "tcp", "::1", "2222"}
			var userPath []byte
			if runtime.GOOS == "windows" {
				userPath = bootstrapUserPath(t)
				defer func() {
					if !bytes.Equal(userPath, bootstrapUserPath(t)) {
						t.Error("custom install directory changed the user's PATH")
					}
				}()
			}
			if mode == "cmd-pipe" {
				args = nil
			}
			for attempt := 0; attempt < 2; attempt++ {
				if attempt == 1 && mode != "cmd-pipe" {
					args = []string{"launch", "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "localhost", "3000"}
				}
				command := bootstrapCommand(t, mode, root, args)
				command.Env = bootstrapEnvironment(root, release.URL)
				output, err := command.CombinedOutput()
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
					t.Fatalf("attempt %d: error=%v, want exit 23; output=%s", attempt+1, err, output)
				}
				data, err := os.ReadFile(filepath.Join(root, "argv.json"))
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if len(got) != len(args) || (len(args) > 0 && !reflect.DeepEqual(got, args)) {
					t.Fatalf("client argv=%q, want %q", got, args)
				}
				if mode == "powershell-block" && !strings.Contains(string(output), "CALLER_ALIVE") {
					t.Fatal("script block ended its caller")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if requests["/stable.txt"] != 2 || requests["/"+bootstrapFixtureVersion+"/"+asset] != 1 || requests["/"+bootstrapFixtureVersion+"/"+asset+".sha256"] != 1 {
				t.Fatalf("installer must check latest each time and reuse the verified installed release: %v", requests)
			}
			files, err := os.ReadDir(filepath.Join(root, "tmp"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary installer files remain: %v, error=%v", files, err)
			}
			if runtime.GOOS == "windows" && mode != "cmd-pipe" {
				launcher := filepath.Join(root, "bin", "nt.cmd")
				command := exec.Command("cmd.exe", "/d", "/q", "/c", "call", launcher, "launch", "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "localhost", "3000")
				command.Env = bootstrapEnvironment(root, release.URL)
				output, err := command.CombinedOutput()
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
					t.Fatalf("installed launcher error=%v output=%s", err, output)
				}
			}
		})
	}
}

func TestBootstrapRejectsUnsafeReleaseVersionBeforeAssetDownload(t *testing.T) {
	modes := []string{"posix-pipe"}
	if runtime.GOOS == "windows" {
		modes = []string{"cmd-install", "powershell-file"}
	} else if runtime.GOOS != "linux" {
		t.Skip("requires a supported installer OS")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"tmp", "home"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var unexpected bool
			var mu sync.Mutex
			release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/stable.txt" {
					_, _ = io.WriteString(w, "..\n")
					return
				}
				mu.Lock()
				unexpected = true
				mu.Unlock()
				http.NotFound(w, r)
			}))
			defer release.Close()
			command := bootstrapCommand(t, mode, root, []string{"anonymous", "http", "localhost", "3000"})
			command.Env = bootstrapEnvironment(root, release.URL)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("invalid version succeeded: %s", output)
			}
			mu.Lock()
			defer mu.Unlock()
			if unexpected {
				t.Fatalf("invalid version was used to build an asset URL: %s", output)
			}
			if _, err := os.Stat(filepath.Join(root, "argv.json")); !os.IsNotExist(err) {
				t.Fatal("unverified client executed")
			}
		})
	}
}

func TestBootstrapRejectsUnverifiedArchive(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("requires a supported installer OS")
	}
	archive, asset := bootstrapArchive(t)
	modes := []string{"posix-pipe"}
	if runtime.GOOS == "windows" {
		modes = []string{"cmd-install", "powershell-file"}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"tmp", "home"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			release := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/stable.txt":
					_, _ = io.WriteString(w, bootstrapFixtureVersion+"\n")
				case "/" + bootstrapFixtureVersion + "/" + asset:
					_, _ = w.Write(archive)
				case "/" + bootstrapFixtureVersion + "/" + asset + ".sha256":
					_, _ = io.WriteString(w, strings.Repeat("0", 64)+"\n")
				default:
					http.NotFound(w, r)
				}
			}))
			defer release.Close()
			command := bootstrapCommand(t, mode, root, []string{"launch", "nlc_aaaaaaaaaaaaaaaaaaaaaaaaaa.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "localhost", "3000"})
			command.Env = bootstrapEnvironment(root, release.URL)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "checksum verification failed") {
				t.Fatalf("tampered archive was not rejected: error=%v output=%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, "argv.json")); !os.IsNotExist(err) {
				t.Fatal("unverified client executed")
			}
			if _, err := os.Stat(filepath.Join(root, "bin", "nt.cmd")); !os.IsNotExist(err) {
				t.Fatal("unverified client was published")
			}
		})
	}
}

func TestBootstrapCMDDoesNotDeleteAnUnownedTemporaryDirectory(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires cmd.exe")
	}
	for _, mode := range []string{"cmd-install", "cmd-pipe"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			name := "nodelane-tunnel-install-42-42"
			if mode == "cmd-pipe" {
				name = "nodelane-tunnel-42-42-42-42"
			}
			directory := filepath.Join(root, "tmp", name)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(directory, "existing.txt")
			if err := os.WriteFile(marker, []byte("existing owner"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := bootstrapCommand(t, mode, root, nil)
			command.Env = append(bootstrapEnvironment(root, "http://127.0.0.1:1"), "RANDOM=42")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("reserved directory must prevent the bootstrap from continuing: %s", output)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "existing owner" {
				t.Fatal("bootstrap altered an unowned temporary directory")
			}
		})
	}
}

func bootstrapEnvironment(root, release string) []string {
	env := os.Environ()
	if runtime.GOOS == "windows" {
		// Go does not perform PowerShell's child-version module-path normalization.
		env = append(env, "PSModulePath="+filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "Modules")+";"+filepath.Join(os.Getenv("ProgramFiles"), "WindowsPowerShell", "Modules"))
	}
	for key, value := range map[string]string{
		"NT_RELEASE_BASE": release, "NT_INSTALL_URL": release + "/install.cmd", "NT_BIN_DIR": filepath.Join(root, "bin"),
		"LOCALAPPDATA": filepath.Join(root, "local"), "TEMP": filepath.Join(root, "tmp"), "TMP": filepath.Join(root, "tmp"),
		"HOME": filepath.Join(root, "home"), "XDG_DATA_HOME": filepath.Join(root, "data"), "NT_FIXTURE_RECORD": filepath.Join(root, "argv.json"), "NO_COLOR": "1",
	} {
		env = append(env, key+"="+value)
	}
	return env
}

func bootstrapUserPath(t *testing.T) []byte {
	t.Helper()
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[Environment]::GetEnvironmentVariable('Path','User')").Output()
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func bootstrapCommand(t *testing.T, mode, root string, args []string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	name := "run.sh"
	if mode == "cmd-install" {
		name = "install.cmd"
	} else if mode == "cmd-pipe" {
		name = "run.cmd"
	} else if strings.HasPrefix(mode, "powershell") {
		name = "run.ps1"
	}
	script, err := publicAssets.ReadFile("assets/" + name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, script, 0o600); err != nil {
		t.Fatal(err)
	}
	var command *exec.Cmd
	switch mode {
	case "cmd-install":
		command = exec.CommandContext(ctx, "cmd.exe", append([]string{"/d", "/q", "/c", "call", path}, args...)...)
	case "cmd-pipe":
		command = exec.CommandContext(ctx, "cmd.exe", "/d", "/q")
		command.Stdin = bytes.NewReader(script)
	case "powershell-file":
		command = exec.CommandContext(ctx, "powershell.exe", append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}, args...)...)
	case "powershell-block":
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = "'" + arg + "'"
		}
		text := "& ([scriptblock]::Create([IO.File]::ReadAllText('" + strings.ReplaceAll(path, "'", "''") + "'))) -TunnelArguments @(" + strings.Join(quoted, ",") + "); Write-Output 'CALLER_ALIVE'; exit $LASTEXITCODE"
		command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", text)
	default:
		command = exec.CommandContext(ctx, "sh", append([]string{"-s", "--"}, args...)...)
		command.Stdin = bytes.NewReader(script)
	}
	return command
}

func bootstrapArchive(t *testing.T) ([]byte, string) {
	t.Helper()
	root := t.TempDir()
	source := `package main
import ("encoding/json"; "fmt"; "os")
func main() {
 if len(os.Args) == 2 && os.Args[1] == "--version" { fmt.Println("9.9.9-fixture"); return }
 data, _ := json.Marshal(os.Args[1:])
 if err := os.WriteFile(os.Getenv("NT_FIXTURE_RECORD"), data, 0600); err != nil { panic(err) }
 os.Exit(23)
}
`
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "nt")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	command := exec.Command("go", "build", "-o", binary, path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic client: %v: %s", err, output)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
		writer := zip.NewWriter(&output)
		entry, err := writer.Create("nt.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gzipWriter := gzip.NewWriter(&output)
		writer := tar.NewWriter(gzipWriter)
		if err := writer.WriteHeader(&tar.Header{Name: "nt", Mode: 0o700, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return output.Bytes(), "nt_" + bootstrapFixtureVersion + "_" + runtime.GOOS + "_" + runtime.GOARCH + extension
}
