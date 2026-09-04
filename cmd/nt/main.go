package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	ntclient "github.com/Wy2926/nodelane-tunneld/internal/client"
)

func main() {
	ui := newConsoleUI(os.Stdout, os.Stderr)
	if err := run(ui); err != nil {
		ui.failure(err.Error())
		os.Exit(1)
	}
}

func run(ui *consoleUI) error {
	args, localizer, err := parseLanguageOptions(os.Args[1:], environmentLookup)
	ui.setLocalizer(localizer)
	if err != nil {
		return err
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(ui.out, ntclient.Version)
		return nil
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(ui.out, ui.text(msgUsage))
		fmt.Fprintln(ui.out, ui.text(msgHelpDescription))
		fmt.Fprintln(ui.out, ui.text(msgHelpLanguage))
		fmt.Fprintln(ui.out, ui.text(msgHelpLanguageEnvironment))
		return nil
	}
	if len(args) == 1 && args[0] == "languages" {
		fmt.Fprintf(ui.out, "%s: %s\n", ui.text(msgSupportedLanguages), supportedLocaleList())
		return nil
	}

	values, interactive, err := prepareTarget(args, ui)
	if err != nil {
		return err
	}
	if interactive {
		input, closeInput, openErr := openInteractiveInput(ui)
		if openErr != nil {
			return openErr
		}
		defer closeInput()
		err = runTargetForm(&values, input, ui.out, ui)
		if errors.Is(err, errTargetFormCanceled) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	target, err := values.target(ui)
	if err != nil {
		return err
	}

	endpoint := net.JoinHostPort(target.host, strconv.Itoa(target.port))
	ui.step(ui.text(msgCheckLocalService, target.protocol, endpoint))
	if target.protocol != "udp" {
		if err := checkLocalTCP(target.host, target.port); err != nil {
			return fmt.Errorf("%s", ui.text(msgLocalServiceUnavailable, endpoint, err))
		}
	}

	apiURL := os.Getenv("NT_API_URL")
	if apiURL == "" {
		apiURL = "https://tunnel.nodelane.net/api/v1"
	}
	api := ntclient.NewAPI(apiURL)
	credentialsPath, err := ntclient.CredentialsPath()
	if err != nil {
		return fmt.Errorf("%s", ui.text(msgLocateCredentialsFailed, err))
	}
	credentials, err := ntclient.LoadCredentials(credentialsPath)
	if errors.Is(err, os.ErrNotExist) {
		ui.step(ui.text(msgRegisteringDevice))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		credentials, err = api.Register(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s", ui.text(msgRegisterDeviceFailed, err))
		}
		if err := ntclient.SaveCredentials(credentialsPath, credentials); err != nil {
			return fmt.Errorf("%s", ui.text(msgSaveCredentialsFailed, err))
		}
	} else if err != nil {
		return fmt.Errorf("%s", ui.text(msgLoadCredentialsFailed, err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ui.step(ui.text(msgRequestingTunnel))
	tunnel, err := api.CreateTunnel(ctx, credentials, target.protocol, target.port, ntclient.Version)
	if err != nil {
		return fmt.Errorf("%s", ui.text(msgCreateTunnelFailed, err))
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cleanupCancel()
		_ = api.DeleteTunnel(cleanupCtx, credentials, tunnel.ID)
	}()

	monitor, err := startTrafficMonitor(ctx, target.protocol, target.host, target.port, ui)
	if err != nil {
		return err
	}
	defer monitor.Close()
	caFile := os.Getenv("NT_CA_FILE")
	frpLogLevel := os.Getenv("NT_FRP_LOG_LEVEL")
	frpConfig := (ntclient.FRPConfig{
		ClientID: credentials.ClientID, Tunnel: tunnel, LocalPort: monitor.port,
		CAFile: caFile, ProxyURL: os.Getenv("NT_FRP_PROXY_URL"), LogLevel: frpLogLevel,
	}).TOML()

	logs := newTailBuffer(64 << 10)
	var frpOutput io.Writer = logs
	if os.Getenv("NT_FRP_LOG") == "1" || frpLogLevel == "debug" || frpLogLevel == "trace" {
		frpOutput = io.MultiWriter(logs, os.Stderr)
	}
	embeddedFRP, err := ntclient.NewEmbeddedFRPClient(frpConfig, frpOutput)
	if err != nil {
		return err
	}
	frpCtx, stopFRP := context.WithCancel(ctx)
	defer stopFRP()
	ui.step(ui.text(msgConnectingEdge))
	waitCh := make(chan error, 1)
	go func() { waitCh <- embeddedFRP.Run(frpCtx) }()

	ready := false
	deadline := time.NewTimer(12 * time.Second)
	poll := time.NewTicker(300 * time.Millisecond)
	defer deadline.Stop()
	defer poll.Stop()
	for !ready {
		select {
		case err := <-waitCh:
			if ctx.Err() != nil {
				return nil
			}
			return frpExitError(err, logs.String(), ui)
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			stopFRP()
			return fmt.Errorf("%s", ui.text(msgConnectTimeout, tail(logs.String(), 1800)))
		case <-poll.C:
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
			status, statusErr := api.GetTunnel(statusCtx, credentials, tunnel.ID)
			statusCancel()
			if statusErr == nil && status.Status == "online" {
				ready = true
			}
		}
	}

	ui.success(ui.text(msgTunnelConnected))
	ui.banner()
	ui.detail(ui.text(msgClientLabel), credentials.ClientID)
	ui.detail(ui.text(msgLocalAddressLabel), fmt.Sprintf("%s://%s", target.protocol, endpoint))
	ui.highlightedDetail(ui.text(msgPublicAddressLabel), tunnel.PublicURL)
	ui.detail(ui.text(msgExpiresAtLabel), tunnel.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))
	if target.protocol == "http" {
		ui.instruction(ui.text(msgHTTPRequestInstruction))
	} else {
		ui.instruction(ui.text(msgTrafficInstruction))
	}

	var statsTicker *time.Ticker
	var stats <-chan time.Time
	if target.protocol == "tcp" || target.protocol == "udp" {
		statsTicker = time.NewTicker(time.Second)
		defer statsTicker.Stop()
		stats = statsTicker.C
		ui.stats(target.protocol, monitor.Snapshot())
		defer ui.endStats()
	}

	for {
		select {
		case err := <-waitCh:
			if ctx.Err() != nil {
				return nil
			}
			return frpExitError(err, logs.String(), ui)
		case <-stats:
			ui.stats(target.protocol, monitor.Snapshot())
		case <-ctx.Done():
			return nil
		}
	}
}

func argumentsNeedPrompt(args []string) bool {
	return len(args) < 3
}

type tunnelTarget struct {
	protocol string
	host     string
	port     int
}

func validProtocol(value string) bool {
	return value == "http" || value == "tcp" || value == "udp"
}

func parsePort(value string, ui *consoleUI) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New(ui.text(msgInvalidPort))
	}
	return port, nil
}

func parseLocalHost(value string, ui *consoleUI) (string, error) {
	host := strings.TrimSpace(value)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if host == "" || strings.ContainsAny(host, "/\\?#") || strings.ContainsFunc(host, func(character rune) bool {
		return character <= ' '
	}) {
		return "", errors.New(ui.text(msgInvalidLocalAddress))
	}
	if strings.Contains(host, ":") {
		ip := host
		if zone := strings.LastIndexByte(ip, '%'); zone >= 0 {
			ip = ip[:zone]
		}
		if net.ParseIP(ip) == nil {
			return "", errors.New(ui.text(msgInvalidLocalAddress))
		}
	}
	return host, nil
}

func checkLocalTCP(host string, port int) error {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 900*time.Millisecond)
	if err != nil {
		return err
	}
	return connection.Close()
}

func frpExitError(err error, logs string, ui *consoleUI) error {
	if err == nil {
		return errors.New(ui.text(msgFRPStoppedUnexpectedly))
	}
	return fmt.Errorf("%s", ui.text(msgFRPStopped, err, tail(logs, 1800)))
}

func tail(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[len(value)-max:]
}

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{data: make([]byte, 0, max), max: max}
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(data)
	if len(data) >= b.max {
		b.data = append(b.data[:0], data[len(data)-b.max:]...)
		return written, nil
	}
	overflow := len(b.data) + len(data) - b.max
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, data...)
	return written, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
