package testutil

import (
	"context"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// RedisResult holds connection details for a test Redis container.
type RedisResult struct {
	// URI is the connection string (e.g. "redis://localhost:12345").
	URI string
	// Host is the container host (e.g. "localhost").
	Host string
	// Port is the mapped port as a string (e.g. "12345").
	Port string
}

// NewTestRedis starts a temporary Redis 7 container and returns its connection
// details. The container is terminated via t.Cleanup.
//
// Example:
//
//	r := testutil.NewTestRedis(t)
//	client := redis.NewClient(&redis.Options{Addr: r.Host + ":" + r.Port})
func NewTestRedis(t *testing.T) RedisResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("testutil.NewTestRedis: start redis container: %v", err)
	}

	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("testutil.NewTestRedis: terminate redis container: %v", err)
		}
	})

	uri, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("testutil.NewTestRedis: get connection string: %v", err)
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("testutil.NewTestRedis: get host: %v", err)
	}

	mappedPort, err := ctr.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatalf("testutil.NewTestRedis: get mapped port: %v", err)
	}

	return RedisResult{
		URI:  uri,
		Host: host,
		Port: mappedPort.Port(),
	}
}
