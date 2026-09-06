package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/Wy2926/nodelane-tunneld/internal/cliauth"
	"github.com/Wy2926/nodelane-tunneld/internal/runclient"
)

type commandAPI interface {
	Bootstrap(context.Context) (runclient.BootstrapConfig, error)
	Routes(context.Context, string) ([]runclient.Route, error)
	Start(context.Context, string, string, string) (runclient.Run, error)
	Redeem(context.Context, string, string) (runclient.Run, error)
	Allocate(context.Context, string, string, string, int, string) (runclient.Run, error)
	Heartbeat(context.Context, string, string) (runclient.HeartbeatResult, error)
	Stop(context.Context, string, string) (runclient.Run, error)
}

type accountSession interface {
	Login(context.Context, func(cliauth.DeviceCode) error) error
	AccessToken(context.Context) (string, error)
}

var errRouteSelection = errors.New("route_selection_failed")

type commandDependencies struct {
	api               func() (commandAPI, error)
	account           func(context.Context, runclient.OIDCConfig) (accountSession, error)
	logout            func(context.Context) error
	openBrowser       func(context.Context, string) error
	preflight         func(context.Context, runclient.Target) error
	validateBootstrap func(runclient.BootstrapConfig) error
	installationID    func() (string, error)
	requestKey        func() (string, error)
	run               func(context.Context, commandAPI, runclient.BootstrapConfig, runclient.Run, runclient.Target) error
}

func executeCommand(ctx context.Context, command cliCommand, ui *consoleUI, deps commandDependencies) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch command.kind {
	case "logout":
		if err := deps.logout(ctx); err != nil {
			return err
		}
		ui.success(ui.text(msgLoggedOut))
		return nil
	case "login", "routes", "anonymous", "start", "launch":
	default:
		return errors.New(ui.text(msgUsage))
	}
	target := runclient.Target{Protocol: command.target.protocol, LocalHost: command.target.host, LocalPort: command.target.port}
	isRun := command.kind == "anonymous" || command.kind == "start" || command.kind == "launch"
	if isRun {
		if err := deps.preflight(ctx, target); err != nil {
			return err
		}
	}
	api, err := deps.api()
	if err != nil {
		return err
	}
	bootstrap, err := api.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if isRun {
		if err := deps.validateBootstrap(bootstrap); err != nil {
			return err
		}
	}
	var token string
	if command.kind == "login" || command.kind == "routes" || command.kind == "start" {
		account, err := deps.account(ctx, bootstrap.OIDC)
		if err != nil {
			return err
		}
		if command.kind == "login" {
			err = account.Login(ctx, func(device cliauth.DeviceCode) error {
				ui.detail(ui.text(msgLoginURL), safeConsoleField(device.VerificationURI, 4096))
				ui.highlightedDetail(ui.text(msgLoginCode), safeConsoleField(device.UserCode, 64))
				if !device.Expiry.IsZero() {
					ui.detail(ui.text(msgExpiresAtLabel), device.Expiry.Local().Format("2006-01-02 15:04:05 MST"))
				}
				if deps.openBrowser != nil && ctx.Err() == nil {
					target := device.VerificationURIComplete
					if target == "" {
						target = device.VerificationURI
					}
					openCtx, cancel := context.WithTimeout(ctx, deviceBrowserTimeout)
					_ = deps.openBrowser(openCtx, target)
					cancel()
				}
				return ctx.Err()
			})
			if err != nil {
				return err
			}
			ui.success(ui.text(msgLoggedIn))
			return nil
		}
		token, err = account.AccessToken(ctx)
		if err != nil {
			return err
		}
	}
	if command.kind == "routes" {
		routes, err := api.Routes(ctx, token)
		if err != nil {
			return err
		}
		printRoutes(ui, routes)
		return nil
	}
	var run runclient.Run
	switch command.kind {
	case "anonymous":
		installation, err := deps.installationID()
		if err != nil {
			return err
		}
		key, err := deps.requestKey()
		if err != nil {
			return err
		}
		run, err = api.Allocate(ctx, installation, target.Protocol, target.LocalHost, target.LocalPort, key)
		if err != nil {
			return err
		}
	case "launch":
		key, err := deps.requestKey()
		if err != nil {
			return err
		}
		run, err = api.Redeem(ctx, command.launchCode, key)
		if err != nil {
			return err
		}
	case "start":
		routeID := command.route
		if !strings.HasPrefix(routeID, "rte_") {
			routes, err := api.Routes(ctx, token)
			if err != nil {
				return err
			}
			matches := 0
			for _, route := range routes {
				if route.Subdomain == command.route && route.Status == "active" && route.Protocol == "http" {
					routeID = route.ID
					matches++
				}
			}
			if matches != 1 {
				return errRouteSelection
			}
		}
		key, err := deps.requestKey()
		if err != nil {
			return err
		}
		run, err = api.Start(ctx, routeID, token, key)
		if err != nil {
			return err
		}
	}
	return deps.run(ctx, api, bootstrap, run, target)
}

func printRoutes(ui *consoleUI, routes []runclient.Route) {
	if len(routes) == 0 {
		ui.detail(ui.text(msgRoutesLabel), ui.text(msgNoRoutes))
		return
	}
	writer := tabwriter.NewWriter(ui.out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", ui.text(msgRouteIDLabel), ui.text(msgPublicAddressLabel), ui.text(msgStateLabel))
	for _, route := range routes {
		state := route.Status
		if route.CurrentRun != nil {
			state = route.CurrentRun.Status
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", safeConsoleField(route.ID, 64), safeConsoleField(route.PublicURL, 2048), safeConsoleField(state, 32))
	}
	_ = writer.Flush()
}
