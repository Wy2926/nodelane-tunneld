package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
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
