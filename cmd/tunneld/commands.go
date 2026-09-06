package main

import (
	"errors"
	"slices"
)

type serviceCommand uint8

const (
	commandServe serviceCommand = iota
	commandVersion
	commandInitAnonymous
)

func parseServiceCommand(args []string) (serviceCommand, error) {
	switch {
	case len(args) == 0:
		return commandServe, nil
	case slices.Equal(args, []string{"--version"}):
		return commandVersion, nil
	case slices.Equal(args, []string{"anonymous-resources", "init", "--confirm-clean-data-plane"}):
		return commandInitAnonymous, nil
	default:
		return commandServe, errors.New("usage: tunneld [--version | anonymous-resources init --confirm-clean-data-plane]")
	}
}
