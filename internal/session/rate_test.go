package session_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wy2926/nodelane-tunneld/internal/session"
)

func TestAllowEnforcesFixedWindowAtomically(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.clock.now = time.Unix(1_700_000_123, 456_000_000).UTC()
	const callers = 32
	const limit = int64(7)
	window := 10 * time.Minute
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	results := make(chan session.RateLimit, callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			result, err := f.store.Allow(ctx, "anonymous-network", "2001:db8::/64", limit, window)
			results <- result
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	allowed := 0
	var resetAt time.Time
	for range callers {
		result, err := <-results, <-errs
		if err != nil {
			t.Fatal(err)
		}
		if result.Allowed {
			allowed++
			if result.RetryAfter != 0 {
				t.Error("allowed request received RetryAfter")
			}
		} else if result.RetryAfter <= 0 || result.RetryAfter > window {
			t.Errorf("denied RetryAfter = %v", result.RetryAfter)
		}
		if result.Remaining < 0 || result.Remaining >= limit {
			t.Errorf("remaining = %d", result.Remaining)
		}
		if resetAt.IsZero() {
			resetAt = result.ResetAt
		} else if !resetAt.Equal(result.ResetAt) {
			t.Error("same fixed window returned different reset boundaries")
		}
	}
	if allowed != int(limit) {
		t.Fatalf("allowed requests = %d, want %d", allowed, limit)
	}
	f.clock.Advance(resetAt.Sub(f.clock.Now()))
	result, err := f.store.Allow(ctx, "anonymous-network", "2001:db8::/64", limit, window)
	if err != nil || !result.Allowed || result.Remaining != limit-1 || !result.ResetAt.Equal(resetAt.Add(window)) {
		t.Fatalf("new fixed window did not reset counter: result=%+v err=%v", result, err)
	}
}

func TestAllowHashesCallerControlledNamesAndSeparatesDimensions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"bucket:*", "subject\nsecret"}, {"bucket:*", "other"}, {"other", "subject\nsecret"}} {
		result, err := f.store.Allow(ctx, pair[0], pair[1], 1, time.Minute)
		if err != nil || !result.Allowed {
			t.Fatalf("independent rate dimension rejected: %+v %v", result, err)
		}
	}
	keys := prefixKeys(t, f.client, f.prefix)
	if len(keys) != 3 {
		t.Fatalf("rate key count = %d, want 3", len(keys))
	}
	for _, key := range keys {
		if strings.Contains(key, "bucket") || strings.Contains(key, "subject") || strings.Contains(key, "secret") || strings.Contains(key, "*") || strings.Contains(key, "\n") {
			t.Fatal("rate limiter embedded caller-controlled input in Redis key")
		}
	}
}

func TestAllowUsesUnambiguousRateDimensionsBeforeHashing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"alpha\x00beta", "gamma"}, {"alpha", "beta\x00gamma"}} {
		result, err := f.store.Allow(ctx, pair[0], pair[1], 1, time.Minute)
		if err != nil || !result.Allowed {
			t.Fatalf("distinct rate dimensions collided: result=%+v err=%v", result, err)
		}
	}
	if keys := prefixKeys(t, f.client, f.prefix); len(keys) != 2 {
		t.Fatalf("distinct dimensions produced %d keys, want 2", len(keys))
	}
}

func TestAllowRejectsUnsafeBoundsWithoutWriting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, input := range []struct {
		bucket  string
		subject string
		limit   int64
		window  time.Duration
	}{
		{"", "subject", 1, time.Second}, {"bucket", "", 1, time.Second},
		{"bucket", "subject", 0, time.Second}, {"bucket", "subject", 1_000_001, time.Second},
		{"bucket", "subject", 1, time.Second - time.Millisecond}, {"bucket", "subject", 1, 24*time.Hour + time.Millisecond},
	} {
		if _, err := f.store.Allow(ctx, input.bucket, input.subject, input.limit, input.window); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("invalid rate input error = %v, want invalid", err)
		}
	}
	if keys := prefixKeys(t, f.client, f.prefix); len(keys) != 0 {
		t.Fatal("invalid rate input wrote Redis keys")
	}
}
