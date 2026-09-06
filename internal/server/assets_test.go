package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseFilesAreServedWhenConfigured(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "stable.txt"), []byte("0.1.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := PublicHandler(directory)
	request := httptest.NewRequest(http.MethodGet, "/releases/stable.txt", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "0.1.1\n" {
		t.Fatalf("release response status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestFrontendServesLocalizedPagesAndSEOFiles(t *testing.T) {
	handler := PublicHandler("")

	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: `<html lang="en" dir="ltr">`},
		{path: "/zh-cn/", contentType: "text/html", contains: `<html lang="zh-CN" dir="ltr">`},
		{path: "/robots.txt", contentType: "text/plain", contains: "Sitemap: https://tunnel.nodelane.net/sitemap.xml"},
		{path: "/sitemap.xml", contentType: "text/xml", contains: `hreflang="en"`},
		{path: "/nodelane-mark.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-mark-96.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-mark-192.png", contentType: "image/png", contains: ""},
		{path: "/nodelane-tunnel-og.png", contentType: "image/png", contains: ""},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("response status=%d", response.Code)
			}
			if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
				t.Fatalf("Content-Type=%q, want prefix %q", contentType, test.contentType)
			}
			if test.contains != "" && !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("response did not contain %q", test.contains)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/missing-page", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing frontend route status=%d, want 404", response.Code)
	}

	assets, err := fs.Glob(publicAssets, "assets/web/assets/*.css")
	if err != nil || len(assets) == 0 {
		t.Fatalf("find embedded frontend assets: matches=%v error=%v", assets, err)
	}
	request = httptest.NewRequest(http.MethodGet, strings.TrimPrefix(assets[0], "assets/web"), nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("frontend asset response status=%d", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "immutable") {
		t.Fatalf("frontend asset Cache-Control=%q, want immutable", cacheControl)
	}

	for _, script := range []struct {
		path     string
		contains string
	}{
		{path: "/run.sh", contains: "nt_"},
		{path: "/run.ps1", contains: "nt_"},
		{path: "/run.cmd", contains: "install.cmd"},
		{path: "/install.cmd", contains: "nt_"},
	} {
		scriptPath := script.path
		request = httptest.NewRequest(http.MethodGet, scriptPath, nil)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d, want 200", scriptPath, response.Code)
		}
		if robots := response.Header().Get("X-Robots-Tag"); robots != "noindex, nofollow" {
			t.Fatalf("%s X-Robots-Tag=%q, want noindex, nofollow", scriptPath, robots)
		}
		body := response.Body.String()
		if !strings.Contains(body, script.contains) || strings.Contains(body, "ft_") {
			t.Fatalf("%s does not contain %q or references retired assets", scriptPath, script.contains)
		}
		if scriptPath == "/run.sh" && strings.ContainsRune(body, '\r') {
			t.Fatalf("%s contains carriage returns; POSIX shell scripts must use LF line endings", scriptPath)
		}
	}
}

func TestEmbeddedPOSIXBootstrapUsesUnixLineEndings(t *testing.T) {
	data, err := publicAssets.ReadFile("assets/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(data, '\r') {
		t.Fatal("embedded assets/run.sh contains carriage returns; keep the source file LF-only")
	}
}

func TestCMDPipeBootstrapPreservesInstallerExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires cmd.exe and Windows curl.exe")
	}
	handler := PublicHandler("")
	request := httptest.NewRequest(http.MethodGet, "/run.cmd", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("run.cmd status=%d", response.Code)
	}

	temporary := t.TempDir()
	installer := filepath.Join(temporary, "fake-install.cmd")
	if err := os.WriteFile(installer, []byte("@echo off\r\nexit /b 23\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installerURL := "file:///" + strings.ReplaceAll(filepath.ToSlash(installer), " ", "%20")
	command := exec.CommandContext(context.Background(), "cmd.exe", "/d", "/q")
	command.Stdin = strings.NewReader(response.Body.String())
	command.Env = append(os.Environ(), "NT_INSTALL_URL="+installerURL, "TEMP="+temporary, "TMP="+temporary)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 23 {
		t.Fatalf("bootstrap error=%v output=%q, want exit code 23", err, output.String())
	}
}

func TestCMDInstallerReachesControlledReleaseFailure(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires cmd.exe and Windows inbox tools")
	}
	script, err := publicAssets.ReadFile("assets/install.cmd")
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	scriptPath := filepath.Join(temporary, "install.cmd")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(context.Background(), "cmd.exe", "/d", "/q", "/c", scriptPath)
	command.Env = append(os.Environ(),
		"NT_RELEASE_BASE=http://127.0.0.1:1",
		"NT_BIN_DIR="+filepath.Join(temporary, "bin"),
		"LOCALAPPDATA="+filepath.Join(temporary, "local"),
		"TEMP="+temporary,
		"TMP="+temporary,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err == nil {
		t.Fatalf("installer unexpectedly succeeded: %q", output.String())
	}
	if !strings.Contains(output.String(), "Unable to download the latest release version.") {
		t.Fatalf("installer did not reach controlled download failure: %q", output.String())
	}
	if strings.Contains(output.String(), "NodeLane Tunnel installation failed.") {
		t.Fatalf("installer took the generic early-failure path: %q", output.String())
	}
}

func TestCMDInstallerInstallsVerifiedClientFromPathsWithSpaces(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires cmd.exe and Windows inbox tools")
	}
	const version = "9.9.9-test"
	root := filepath.Join(t.TempDir(), "install root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	clientSource := filepath.Join(root, "client.go")
	clientBinary := filepath.Join(root, "nt.exe")
	source := `package main
import ("fmt"; "os")
func main() { if len(os.Args) > 1 && os.Args[1] == "--version" { fmt.Println("9.9.9-test") } }
`
	if err := os.WriteFile(clientSource, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(context.Background(), "go", "build", "-o", clientBinary, clientSource)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake client: %v: %s", err, output)
	}

	assetName := "nt_" + version + "_windows_amd64.zip"
	if strings.EqualFold(os.Getenv("PROCESSOR_ARCHITECTURE"), "ARM64") {
		assetName = "nt_" + version + "_windows_arm64.zip"
	}
	archivePath := filepath.Join(root, assetName)
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	entry, err := zipWriter.Create("nt.exe")
	if err != nil {
		t.Fatal(err)
	}
	clientData, err := os.ReadFile(clientBinary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(clientData); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	archiveData, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(archiveData))
	release := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/stable.txt":
			_, _ = io.WriteString(writer, version+"\n")
		case "/" + version + "/" + assetName:
			_, _ = writer.Write(archiveData)
		case "/" + version + "/" + assetName + ".sha256":
			_, _ = fmt.Fprintf(writer, "%s  %s\n", digest, assetName)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer release.Close()

	script, err := publicAssets.ReadFile("assets/install.cmd")
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(root, "install.cmd")
	if err := os.WriteFile(scriptPath, script, 0o600); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, "temporary files")
	binDirectory := filepath.Join(root, "command bin")
	localData := filepath.Join(root, "local data")
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		command := exec.CommandContext(context.Background(), "cmd.exe", "/d", "/q", "/c", "install.cmd --version")
		command.Dir = root
		command.Env = append(os.Environ(),
			"NT_RELEASE_BASE="+release.URL,
			"NT_BIN_DIR="+binDirectory,
			"LOCALAPPDATA="+localData,
			"TEMP="+temporary,
			"TMP="+temporary,
		)
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Run(); err != nil {
			t.Fatalf("installer attempt %d failed: %v output=%q", attempt, err, output.String())
		}
		if !strings.Contains(output.String(), version) {
			t.Fatalf("installer attempt %d output does not include client version: %q", attempt, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(binDirectory, "nt.cmd")); err != nil {
		t.Fatalf("launcher not installed: %v", err)
	}
	current, err := os.ReadFile(filepath.Join(localData, "nodelane", "tunnel", "current"))
	if err != nil || !strings.Contains(string(current), version) {
		t.Fatalf("current client pointer=%q error=%v", current, err)
	}
}
