package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Wy2926/nodelane-tunneld/internal/controlserver"
)

func TestRuntimeRefusesMissingPersistentConfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := run(context.Background(), controlserver.Config{}, logger)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("runtime accepted missing persistent storage: %v", err)
	}
}
