package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/huh/v2"
	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	ntclient "github.com/Wy2926/nodelane-tunneld/internal/client"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

func runArguments(ctx context.Context, args []string, ui *consoleUI, env func(string) (string, bool), deps commandDependencies) error {
	args, language, err := parseLanguageOptions(args, env)
	ui.setLocalizer(language)
	if err != nil {
		return err
	}
	command, err := parseCommand(args, ui)
	if err != nil {
		return err
	}
	switch command.kind {
	case "version":
		_, _ = fmt.Fprintln(ui.out, ntclient.Version)
		return nil
	case "help":
		ui.banner()
		for _, id := range []messageID{msgUsage, msgHelpDescription, msgHelpLanguage, msgHelpLanguageEnvironment} {
			_, _ = fmt.Fprintln(ui.out, ui.text(id))
		}
		return nil
	case "languages":
		_, _ = fmt.Fprintf(ui.out, "%s: %s\n", ui.text(msgSupportedLanguages), supportedLocaleList())
		return nil
	case "menu":
		command, err = chooseCommandMode(ui)
		if errors.Is(err, errTargetFormCanceled) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if command.interactive {
		input, closeInput, err := openInteractiveInput(ui)
		if err != nil {
			return err
		}
		defer closeInput()
		if err := runTargetForm(&command.form, input, ui.out, ui); err != nil {
			if errors.Is(err, errTargetFormCanceled) {
				return nil
			}
			return errors.New(ui.text(msgOperationFailed, "interactive_input_failed"))
		}
		command.target, err = command.form.target(ui)
		if err != nil {
			return err
		}
	}
	ui.banner()
	if err := executeCommand(ctx, command, ui, deps); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return commandError(ui, err)
	}
	return nil
}

func chooseCommandMode(ui *consoleUI) (cliCommand, error) {
	input, closeInput, err := openInteractiveInput(ui)
	if err != nil {
		return cliCommand{}, err
	}
	defer closeInput()
	mode := "anonymous"
	ui.banner()
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().
		Title(ui.text(msgChooseMode)).Options(
		huh.NewOption(ui.text(msgAnonymousMode), "anonymous"),
		huh.NewOption(ui.text(msgAccountMode), "login"),
	).Value(&mode).Height(2))).WithTheme(nodeLaneFormTheme()).WithInput(input).WithOutput(ui.out).WithShowHelp(false)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return cliCommand{}, errTargetFormCanceled
		}
		return cliCommand{}, errors.New(ui.text(msgOperationFailed, "interactive_input_failed"))
	}
	return parseCommand([]string{mode}, ui)
}

func commandError(ui *consoleUI, err error) error {
	if errors.Is(err, errRouteSelection) {
		return errors.New(ui.text(msgRouteSelectionFailed))
	}
	if errors.Is(err, cliauth.ErrNoCredentials) || errors.Is(err, cliauth.ErrAuthorizationExpired) || errors.Is(err, cliauth.ErrAuthorizationRevoked) {
		return errors.New(ui.text(msgLoginRequired))
	}
	code := "operation_failed"
	var apiError *runclient.APIError
	if errors.As(err, &apiError) && apiError != nil {
		code = apiError.Error()
		if apiError.RetryAfter > 0 {
			seconds := int64(apiError.RetryAfter / time.Second)
			if apiError.RetryAfter%time.Second != 0 {
				seconds++
			}
			ui.warning(ui.text(msgRetryAfter, seconds))
		}
	} else {
		for _, known := range []struct {
			err  error
			code string
		}{
			{cliauth.ErrInvalidConfiguration, "authentication_configuration_invalid"},
			{cliauth.ErrCredentialsUnavailable, "credential_store_unavailable"},
			{cliauth.ErrCredentialsMismatch, "credential_binding_mismatch"},
			{cliauth.ErrProviderUnavailable, "identity_provider_unavailable"},
			{cliauth.ErrInvalidResponse, "identity_response_invalid"},
			{cliauth.ErrRevocationUnconfirmed, "revocation_unconfirmed"},
			{cliauth.ErrAuthorizationDenied, "authorization_denied"},
			{cliauth.ErrFileStoreUnsupported, "credential_file_unsupported"},
			{runclient.ErrInvalidConfiguration, "client_configuration_invalid"},
			{runclient.ErrProxyUnsupported, "frp_proxy_unsupported"},
			{runclient.ErrCloseUnconfirmed, "close_unconfirmed"},
			{runclient.ErrLocalUnavailable, "local_service_unavailable"},
			{runclient.ErrConnectDeadline, "connect_deadline_exceeded"},
			{runclient.ErrLeaseExpired, "lease_expired"},
			{runclient.ErrHardExpired, "hard_expired"},
			{runclient.ErrEngineStopped, "engine_stopped"},
			{context.DeadlineExceeded, "request_timeout"},
		} {
			if errors.Is(err, known.err) {
				code = known.code
				break
			}
		}
	}
	return errors.New(ui.text(msgOperationFailed, code))
}
