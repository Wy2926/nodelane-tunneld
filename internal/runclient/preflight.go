package runclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"
)

var ErrLocalUnavailable = errors.New("local_service_unavailable")

type Target struct {
	Protocol  string
	LocalHost string
	LocalPort int
}

// Preflight checks TCP reachability or UDP resolution and socket creation. A
// successful UDP preflight does not assert that the remote service will reply.
func Preflight(ctx context.Context, target Target) error {
	if !validTarget(target) {
		return ErrInvalidRequest
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	network := "tcp"
	if target.Protocol == "udp" {
		network = "udp"
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(target.LocalHost, strconv.Itoa(target.LocalPort)))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return ErrLocalUnavailable
	}
	_ = connection.Close()
	return nil
}

func validTarget(target Target) bool {
	return (target.Protocol == "http" || target.Protocol == "tcp" || target.Protocol == "udp") && validHost(target.LocalHost) && target.LocalPort > 0 && target.LocalPort <= 65535
}
