package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	ui := newConsoleUI(os.Stdout, os.Stderr)
	if err := run(ui); err != nil {
		ui.failure(err.Error())
		os.Exit(1)
	}
}

func run(ui *consoleUI) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runArguments(ctx, os.Args[1:], ui, environmentLookup, defaultCommandDependencies(ui))
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
