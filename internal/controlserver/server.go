package controlserver

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousapi"
	"github.com/Wy2926/nodelane-tunneld/internal/anonymousreconcile"
	"github.com/Wy2926/nodelane-tunneld/internal/bff"
	"github.com/Wy2926/nodelane-tunneld/internal/controladapters"
	"github.com/Wy2926/nodelane-tunneld/internal/controlapi"
	"github.com/Wy2926/nodelane-tunneld/internal/frpanonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpauth"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/Wy2926/nodelane-tunneld/internal/frpmux"
	"github.com/Wy2926/nodelane-tunneld/internal/frppluginhttp"
	"github.com/Wy2926/nodelane-tunneld/internal/frpregistered"
	"github.com/Wy2926/nodelane-tunneld/internal/identity"
	"github.com/Wy2926/nodelane-tunneld/internal/runtimestats"
	"github.com/Wy2926/nodelane-tunneld/internal/server"
	"github.com/Wy2926/nodelane-tunneld/internal/session"
	"github.com/Wy2926/nodelane-tunneld/internal/sourceip"
	"github.com/Wy2926/nodelane-tunneld/internal/store"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	public, plugin    http.Handler
	postgres          *store.ControlPostgres
	redis             *redis.Client
	sessions          *session.RedisStore
	closeOnce         sync.Once
	closeError        error
	maintenanceCancel context.CancelFunc
	maintenanceDone   chan struct{}
}

func Open(ctx context.Context, cfg Config) (*Server, error) { return openWithHTTPClient(ctx, cfg, nil) }

func openWithHTTPClient(ctx context.Context, cfg Config, httpClient *http.Client) (_ *Server, resultErr error) {
	publicCA, err := preflight(cfg)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime := &Server{}
	defer func() {
		if resultErr != nil {
			_ = runtime.Close()
		}
	}()
	runtime.redis = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB,
		DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, MaxRetries: -1})
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := runtime.redis.Ping(startup).Err(); err != nil {
		return nil, errors.New("Redis is unavailable; persistent control startup refused")
	}
	provider := &lazyOIDCProvider{lifetime: ctx, options: identity.OIDCOptions{Issuer: cfg.OIDCIssuer, PublicOrigin: cfg.PublicOrigin, APIResource: cfg.OIDCResource,
		WebClientID: cfg.OIDCWebClientID, WebClientSecret: cfg.OIDCWebClientSecret, NativeClientID: cfg.OIDCNativeClientID, HTTPClient: httpClient}}
	runtime.postgres, err = store.OpenControlPostgres(startup, cfg.DatabaseURL, store.ControlOptions{LaunchPepper: cfg.LaunchPepper, RunPepper: cfg.RunPepper, ReplayKey: cfg.ReplayKey})
	if err != nil {
		return nil, errors.New("control PostgreSQL startup failed; a reachable fresh or supported control schema is required")
	}
	runtime.sessions, err = session.NewRedisStore(runtime.redis, cfg.RedisPrefix+":sessions", cfg.SessionKey, time.Now)
	if err != nil {
		return nil, err
	}
	sessions := &sessionRefresher{RedisStore: runtime.sessions, provider: provider, now: time.Now}
	anonymousStore, err := anonymous.NewStore(anonymous.Config{Client: runtime.redis, Prefix: cfg.RedisPrefix + ":anonymous", CredentialPepper: cfg.AnonymousPepper,
		ReplayKey: cfg.AnonymousReplayKey, FenceOwnerToken: cfg.AnonymousFenceToken, Clock: time.Now, Random: rand.Reader, PublicDomain: cfg.PublicDomain,
		TCPPorts: portRange(cfg.TCPPortStart, cfg.TCPPortEnd), UDPPorts: portRange(cfg.UDPPortStart, cfg.UDPPortEnd)})
	if err != nil {
		return nil, err
	}
	ip, err := sourceip.New(cfg.TrustedProxyRanges)
	if err != nil {
		return nil, errors.New("trusted proxy configuration is invalid")
	}
	rate, err := controladapters.NewRateAdapter(runtime.sessions)
	if err != nil {
		return nil, err
	}
	ban, err := controladapters.NewBanAdapter(runtime.postgres, time.Now)
	if err != nil {
		return nil, err
	}
	stats, err := runtimestats.NewClient(runtimestats.Options{Endpoint: cfg.FRPSAdminURL, Username: cfg.FRPSAdminUsername, Password: cfg.FRPSAdminPassword, HTTPClient: httpClient})
	if err != nil {
		return nil, err
	}
	evidence, err := frpevidence.NewClient(frpevidence.Options{Endpoint: cfg.FRPSAdminURL, Username: cfg.FRPSAdminUsername, Password: cfg.FRPSAdminPassword, HTTPClient: httpClient})
	if err != nil {
		return nil, err
	}
	clients, err := newClientObserver(cfg.FRPSAdminURL, cfg.FRPSAdminUsername, cfg.FRPSAdminPassword, httpClient)
	if err != nil {
		return nil, err
	}
	coordinator, err := anonymousreconcile.New(anonymousStore, evidence, nil)
	if err != nil {
		return nil, err
	}
	authenticator, err := bff.NewAPIAuthenticator(sessions, runtime.postgres, provider)
	if err != nil {
		return nil, err
	}
	auth, err := bff.New(bff.Options{PublicOrigin: cfg.PublicOrigin, Provider: provider, Sessions: sessions, LogoutReader: runtime.sessions, Accounts: runtime.postgres, Now: time.Now, Random: rand.Reader})
	if err != nil {
		return nil, err
	}
	registered := &registeredRuns{ControlPostgres: runtime.postgres, observer: evidence, clients: clients}
	control, err := controlapi.New(controlapi.Options{PublicOrigin: cfg.PublicOrigin, PublicDomain: cfg.PublicDomain, Authenticator: authenticator,
		Routes: runtime.postgres, Runs: registered, Stats: stats, SourceIP: ip.RequestIP, Banned: ban.Check, RateLimit: rate.Limit, Now: time.Now})
	if err != nil {
		return nil, err
	}
	anonymousHTTP, err := anonymousapi.New(anonymousapi.Options{Store: &anonymousRuns{Store: anonymousStore, coordinator: coordinator}, SourceIP: ip.RequestIP, Banned: ban.Check})
	if err != nil {
		return nil, err
	}
	authorizer, err := frpauth.New(runtime.postgres, cfg.FRPBandwidth)
	if err != nil {
		return nil, err
	}
	registeredPlugin, err := frpregistered.New(authorizer)
	if err != nil {
		return nil, err
	}
	anonymousPlugin, err := frpanonymous.New(anonymousStore, cfg.FRPBandwidth)
	if err != nil {
		return nil, err
	}
	dispatcher, err := frpmux.New(registeredPlugin, anonymousPlugin)
	if err != nil {
		return nil, err
	}
	plugin, err := frppluginhttp.New(frppluginhttp.Options{Dispatcher: dispatcher})
	if err != nil {
		return nil, err
	}
	runtime.plugin = privatePlugin(plugin)
	runtime.public = newPublicRouter(ClientConfig{
		FRP:  FRPClientConfig{ServerAddr: cfg.FRPServerAddr, ServerPort: cfg.FRPServerPort, TLSServerName: cfg.FRPTLSServerName, TrustedCAPEM: publicCA},
		OIDC: OIDCClientConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCNativeClientID, Resource: cfg.OIDCResource},
	}, auth.Handler(), control.Handler(), anonymousHTTP.Handler(), server.PublicHandler(cfg.ReleaseDir))
	runtime.startMaintenance(ctx)
	return runtime, nil
}

func (s *Server) Handler() http.Handler       { return s.public }
func (s *Server) PluginHandler() http.Handler { return s.plugin }

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.maintenanceCancel != nil {
			s.maintenanceCancel()
			<-s.maintenanceDone
		}
		if s.postgres != nil {
			s.closeError = errors.Join(s.closeError, s.postgres.Close())
		}
		if s.redis != nil {
			s.closeError = errors.Join(s.closeError, s.redis.Close())
		}
	})
	return s.closeError
}

func privatePlugin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		peer, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !peer.IsLoopback() || r.URL.Path != "/internal/frp" || r.URL.RawPath != "" ||
			len(r.Header.Values("X-Real-IP")) != 0 || len(r.Header.Values("X-Forwarded-For")) != 0 || len(r.Header.Values("Forwarded")) != 0 {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func portRange(first, last int) []uint16 {
	ports := make([]uint16, 0, last-first+1)
	for port := first; port <= last; port++ {
		ports = append(ports, uint16(port))
	}
	return ports
}
