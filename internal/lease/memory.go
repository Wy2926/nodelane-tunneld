package lease

import (
	"context"
	"sync"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/domain"
)

type memoryLease struct {
	clientID  string
	ipKey     string
	resource  string
	expiresAt time.Time
}

type Memory struct {
	mu     sync.Mutex
	leases map[string]memoryLease
}

func NewMemory() *Memory { return &Memory{leases: make(map[string]memoryLease)} }

func (m *Memory) Reserve(_ context.Context, clientID, ipKey, tunnelID, resourceKey string, expiresAt time.Time, maxPerClient, maxPerIP int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune(time.Now())
	clients, ips := 0, 0
	for existingID, item := range m.leases {
		if existingID == tunnelID {
			continue
		}
		if item.resource == resourceKey {
			return domain.ErrConflict
		}
		if item.clientID == clientID {
			clients++
		}
		if item.ipKey == ipKey {
			ips++
		}
	}
	if maxPerClient > 0 && clients >= maxPerClient || maxPerIP > 0 && ips >= maxPerIP {
		return domain.ErrLimitReached
	}
	m.leases[tunnelID] = memoryLease{clientID: clientID, ipKey: ipKey, resource: resourceKey, expiresAt: expiresAt}
	return nil
}

func (m *Memory) Release(_ context.Context, clientID, ipKey, tunnelID, resourceKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.leases[tunnelID]
	if ok && item.clientID == clientID && item.ipKey == ipKey && item.resource == resourceKey {
		delete(m.leases, tunnelID)
	}
	return nil
}

func (m *Memory) Close() error { return nil }

func (m *Memory) prune(now time.Time) {
	for id, item := range m.leases {
		if !item.expiresAt.After(now) {
			delete(m.leases, id)
		}
	}
}
