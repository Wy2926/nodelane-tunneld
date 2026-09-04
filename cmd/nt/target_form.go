package main

import (
	"errors"
	"strings"
)

type targetField uint8

const (
	targetProtocol targetField = iota
	targetHost
	targetPort
)

type targetFormValues struct {
	Protocol string
	Host     string
	Port     string
	Focus    targetField
}

func prepareTarget(args []string, ui *consoleUI) (targetFormValues, bool, error) {
	values := targetFormValues{Protocol: "http", Port: "3000"}
	if len(args) > 3 {
		return targetFormValues{}, false, errors.New(ui.text(msgUsage))
	}

	if len(args) > 0 {
		protocol := strings.ToLower(strings.TrimSpace(args[0]))
		if validProtocol(protocol) {
			values.Protocol = protocol
		} else if len(args) == 3 {
			values.Protocol = protocol
		} else {
			values.Focus = targetProtocol
		}
	}
	if len(args) > 1 {
		values.Host = strings.TrimSpace(args[1])
	}
	if len(args) > 2 {
		values.Port = strings.TrimSpace(args[2])
	}

	if len(args) < 3 {
		switch len(args) {
		case 0:
			values.Focus = targetProtocol
		case 1:
			if validProtocol(strings.ToLower(strings.TrimSpace(args[0]))) {
				values.Focus = targetHost
			}
		case 2:
			if !validProtocol(strings.ToLower(strings.TrimSpace(args[0]))) {
				values.Focus = targetProtocol
			} else if values.Host != "" {
				if _, err := parseLocalHost(values.Host, ui); err != nil {
					values.Focus = targetHost
				} else {
					values.Focus = targetPort
				}
			} else {
				values.Focus = targetPort
			}
		}
		return values, true, nil
	}

	if _, err := values.target(ui); err != nil {
		return targetFormValues{}, false, err
	}
	return values, false, nil
}

func (values targetFormValues) target(ui *consoleUI) (tunnelTarget, error) {
	protocol := strings.ToLower(strings.TrimSpace(values.Protocol))
	if !validProtocol(protocol) {
		return tunnelTarget{}, errors.New(ui.text(msgInvalidProtocol))
	}
	host := strings.TrimSpace(values.Host)
	if host == "" {
		host = "localhost"
	}
	host, err := parseLocalHost(host, ui)
	if err != nil {
		return tunnelTarget{}, err
	}
	port, err := parsePort(values.Port, ui)
	if err != nil {
		return tunnelTarget{}, err
	}
	return tunnelTarget{protocol: protocol, host: host, port: port}, nil
}
