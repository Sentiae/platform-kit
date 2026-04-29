package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/client"
)

// Config controls Temporal client connection + namespace.
type Config struct {
	// HostPort is the Temporal frontend address (e.g. "localhost:7233").
	HostPort string

	// Namespace partitions workflows + activities. Default "default".
	Namespace string

	// Identity tags this client in Temporal UI (defaults to <hostname>@<pid>).
	Identity string

	// Logger receives Temporal SDK diagnostic output. Required.
	Logger *slog.Logger

	// DialTimeout caps initial connection establishment. Default 10s.
	DialTimeout time.Duration
}

// NewClient dials Temporal and returns an SDK client. The caller owns the
// returned client and must call Close on shutdown.
//
// The client is configured with our standard logger adapter; OTel propagation
// is handled per-workflow via interceptors registered on the worker.
func NewClient(ctx context.Context, cfg Config) (client.Client, error) {
	if cfg.HostPort == "" {
		return nil, fmt.Errorf("temporal: HostPort is required")
	}
	if cfg.Logger == nil {
		return nil, fmt.Errorf("temporal: Logger is required")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	opts := client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Identity:  cfg.Identity,
		Logger:    newSDKLogger(cfg.Logger),
	}

	cli, err := client.DialContext(dialCtx, opts)
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", cfg.HostPort, err)
	}
	return cli, nil
}
