package client

import (
	"context"
	"fmt"
	"io"

	frpclient "github.com/fatedier/frp/client"
	frpconfig "github.com/fatedier/frp/pkg/config"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	"github.com/fatedier/frp/pkg/policy/security"
	frplog "github.com/fatedier/frp/pkg/util/log"
	frpversion "github.com/fatedier/frp/pkg/util/version"
	frpgoliblog "github.com/fatedier/golib/log"
)

// EmbeddedFRPClient runs the upstream frp client service inside nt. No frpc
// executable is extracted or launched on the user's machine.
type EmbeddedFRPClient struct {
	service *frpclient.Service
}

func NewEmbeddedFRPClient(configText string, logOutput io.Writer) (*EmbeddedFRPClient, error) {
	if frpversion.Full() != EmbeddedFRPVersion {
		return nil, fmt.Errorf("embedded frp version is %s, want %s", frpversion.Full(), EmbeddedFRPVersion)
	}

	var parsed v1.ClientConfig
	if err := frpconfig.LoadConfigure([]byte(configText), &parsed, true, "toml"); err != nil {
		return nil, fmt.Errorf("parse embedded frp config: %w", err)
	}
	common := &parsed.ClientCommonConfig
	proxyURLConfigured := common.Transport.ProxyURL != ""
	if err := common.Complete(); err != nil {
		return nil, fmt.Errorf("complete embedded frp config: %w", err)
	}

	proxies := make([]v1.ProxyConfigurer, 0, len(parsed.Proxies))
	for _, proxy := range parsed.Proxies {
		proxies = append(proxies, proxy.ProxyConfigurer)
	}
	visitors := make([]v1.VisitorConfigurer, 0, len(parsed.Visitors))
	for _, visitor := range parsed.Visitors {
		visitors = append(visitors, visitor.VisitorConfigurer)
	}

	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll(proxies, visitors); err != nil {
		return nil, fmt.Errorf("load embedded frp config: %w", err)
	}
	aggregator := source.NewAggregator(configSource)
	validatedProxies, validatedVisitors, err := aggregator.Load()
	if err != nil {
		return nil, fmt.Errorf("load embedded frp sources: %w", err)
	}
	validatedProxies, validatedVisitors = frpconfig.FilterClientConfigurers(common, validatedProxies, validatedVisitors)
	validatedProxies = frpconfig.CompleteProxyConfigurers(validatedProxies)
	validatedVisitors = frpconfig.CompleteVisitorConfigurers(validatedVisitors)
	unsafeFeatures := security.NewUnsafeFeatures(nil)
	warning, err := validation.ValidateAllClientConfig(common, validatedProxies, validatedVisitors, unsafeFeatures)
	if err != nil {
		return nil, fmt.Errorf("validate embedded frp config: %w", err)
	}

	configureEmbeddedFRPLogger(logOutput, common.Log.Level)
	if warning != nil {
		frplog.Warnf("embedded frp config warning: %v", warning)
	}

	options := frpclient.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: aggregator,
		UnsafeFeatures:         unsafeFeatures,
	}
	if !proxyURLConfigured {
		// The standalone client was deliberately launched without lowercase
		// http_proxy unless NT_FRP_PROXY_URL was set. Preserve that behavior
		// without mutating the parent process environment.
		options.ConnectorCreator = func(ctx context.Context, cfg *v1.ClientCommonConfig) frpclient.Connector {
			cloned := *cfg
			cloned.Transport = cfg.Transport
			cloned.Transport.ProxyURL = ""
			return frpclient.NewConnector(ctx, &cloned)
		}
	}
	service, err := frpclient.NewService(options)
	if err != nil {
		return nil, fmt.Errorf("create embedded frp client: %w", err)
	}
	return &EmbeddedFRPClient{service: service}, nil
}

func configureEmbeddedFRPLogger(output io.Writer, levelName string) {
	if output == nil {
		output = io.Discard
	}
	level, err := frpgoliblog.ParseLevel(levelName)
	if err != nil {
		level = frpgoliblog.InfoLevel
	}
	frplog.Logger = frpgoliblog.New(
		frpgoliblog.WithCaller(true),
		frpgoliblog.AddCallerSkip(1),
		frpgoliblog.WithLevel(level),
		frpgoliblog.WithOutput(output),
	)
}

func (client *EmbeddedFRPClient) Run(ctx context.Context) error {
	return client.service.Run(ctx)
}
