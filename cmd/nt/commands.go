package main

import (
	"errors"
	"strings"

	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/routes"
)

type cliCommand struct {
	kind        string
	target      tunnelTarget
	form        targetFormValues
	interactive bool
	route       string
	launchCode  string
}

func (c cliCommand) String() string   { return c.kind }
func (c cliCommand) GoString() string { return c.kind }

func parseCommand(args []string, ui *consoleUI) (cliCommand, error) {
	invalid := errors.New(ui.text(msgUsage))
	if len(args) == 0 {
		return cliCommand{kind: "menu"}, nil
	}
	command := cliCommand{kind: args[0]}
	switch command.kind {
	case "help", "--help", "-h":
		command.kind = "help"
	case "version", "--version":
		command.kind = "version"
	}
	switch command.kind {
	case "help", "version", "languages", "login", "logout", "routes":
		if len(args) != 1 {
			return cliCommand{}, invalid
		}
	case "anonymous":
		values, interactive, err := prepareTarget(args[1:], ui)
		if err != nil {
			return cliCommand{}, err
		}
		command.form, command.interactive = values, interactive
		if !interactive {
			command.target, err = values.target(ui)
			if err != nil {
				return cliCommand{}, err
			}
		}
	case "start", "launch":
		if len(args) != 4 {
			return cliCommand{}, invalid
		}
		if command.kind == "launch" {
			if _, err := identity.ParseLaunchCredential(args[1]); err != nil {
				return cliCommand{}, invalid
			}
			command.launchCode = args[1]
		} else {
			if !validRouteSelector(args[1]) {
				return cliCommand{}, invalid
			}
			command.route = args[1]
		}
		values := targetFormValues{Protocol: "http", Host: args[2], Port: args[3]}
		var err error
		command.target, err = values.target(ui)
		if err != nil {
			return cliCommand{}, err
		}
	default:
		return cliCommand{}, invalid
	}
	return command, nil
}

func validRouteSelector(value string) bool {
	if !strings.HasPrefix(value, "rte_") {
		return routes.ValidateSubdomain(value) == nil
	}
	if len(value) != 30 {
		return false
	}
	for _, character := range value[4:] {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}
