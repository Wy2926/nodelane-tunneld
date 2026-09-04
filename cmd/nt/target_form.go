package main

import (
	"errors"
	"io"
	"strings"

	"charm.land/huh/v2"
)

var errTargetFormCanceled = errors.New("target form canceled")

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

func validateOptionalHost(value string, ui *consoleUI) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	_, err := parseLocalHost(value, ui)
	return err
}

func validatePortText(value string, ui *consoleUI) error {
	_, err := parsePort(value, ui)
	return err
}

func buildTargetForm(values *targetFormValues, ui *consoleUI) *huh.Form {
	protocol := huh.NewSelect[string]().
		Key("protocol").
		Title(ui.text(msgChooseProtocol)).
		Description(ui.text(msgProtocolNavigation)).
		Options(
			huh.NewOption("HTTP", "http"),
			huh.NewOption("TCP", "tcp"),
			huh.NewOption("UDP", "udp"),
		).
		Value(&values.Protocol).
		Height(3)
	host := huh.NewInput().
		Key("host").
		Title(ui.text(msgLocalAddressPrompt)).
		Description(ui.text(msgLocalAddressDefaultHelp)).
		Placeholder("localhost").
		Value(&values.Host).
		Validate(func(value string) error { return validateOptionalHost(value, ui) })
	port := huh.NewInput().
		Key("port").
		Title(ui.text(msgPortPrompt)).
		Description(ui.text(msgPortRangeHelp)).
		Value(&values.Port).
		CharLimit(5).
		Validate(func(value string) error { return validatePortText(value, ui) })

	form := huh.NewForm(huh.NewGroup(protocol, host, port)).
		WithAccessible(false).
		WithShowErrors(true).
		WithShowHelp(false)
	for field := targetProtocol; field < values.Focus; field++ {
		form.NextField()
	}
	return form
}

func runTargetForm(values *targetFormValues, input io.Reader, output io.Writer, ui *consoleUI) error {
	err := buildTargetForm(values, ui).
		WithInput(input).
		WithOutput(output).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errTargetFormCanceled
	}
	return err
}
