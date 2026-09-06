package controlserver

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/anonymous"
	"github.com/Wy2926/nodelane-tunneld/internal/frpevidence"
	"github.com/redis/go-redis/v9"
)

var errMaintenanceConfirmation = errors.New("initialization requires --confirm-clean-data-plane during a clean maintenance window")

type freshResourceStore interface {
	PrepareFreshInitialization(context.Context) (anonymous.ResourceFence, error)
	AssertFreshNamespace(context.Context, anonymous.ResourceFence) error
	MarkResourcesVerified(context.Context, anonymous.ResourceFence) (anonymous.ResourceFence, error)
}

type anonymousInventory interface {
	ListAnonymous(context.Context) frpevidence.Inventory
}

func initializeFreshResources(ctx context.Context, confirmed bool, store freshResourceStore, inventory anonymousInventory) error {
	if !confirmed {
		return errMaintenanceConfirmation
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fence, err := store.PrepareFreshInitialization(ctx)
	if err != nil {
		return errRecoveryUnavailable
	}
	if err = store.AssertFreshNamespace(ctx, fence); err != nil {
		return errors.New("anonymous namespace is not fresh; initialization refused")
	}
	observed := inventory.ListAnonymous(ctx)
	if observed.Availability != frpevidence.Available || len(observed.Proxies) != 0 {
		return errors.New("a complete empty anonymous data-plane inventory is required")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, err = store.MarkResourcesVerified(ctx, fence); err != nil {
		return errors.New("anonymous initialization fence changed; verification must be repeated")
	}
	return nil
}

// InitializeAnonymousResources is an explicit operator action. It never clears
// data or restarts frps, and a previously initialized namespace is refused.
func InitializeAnonymousResources(ctx context.Context, cfg Config, confirmed bool) error {
	if !confirmed {
		return errMaintenanceConfirmation
	}
	if _, err := preflight(cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB, MaxRetries: -1, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second})
	defer client.Close()
	store, err := anonymous.NewStore(anonymous.Config{Client: client, Prefix: cfg.RedisPrefix + ":anonymous", CredentialPepper: cfg.AnonymousPepper, ReplayKey: cfg.AnonymousReplayKey, FenceOwnerToken: cfg.AnonymousFenceToken,
		Clock: time.Now, Random: rand.Reader, PublicDomain: cfg.PublicDomain, TCPPorts: portRange(cfg.TCPPortStart, cfg.TCPPortEnd), UDPPorts: portRange(cfg.UDPPortStart, cfg.UDPPortEnd)})
	if err != nil {
		return err
	}
	inventory, err := frpevidence.NewClient(frpevidence.Options{Endpoint: cfg.FRPSAdminURL, Username: cfg.FRPSAdminUsername, Password: cfg.FRPSAdminPassword})
	if err != nil {
		return err
	}
	return initializeFreshResources(ctx, true, store, inventory)
}
