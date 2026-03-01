package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/authzed/authzed-go/v1"
	"github.com/authzed/grpcutil"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spiceDBPresharedKey = "testutil-spicedb-key"

// SpiceDBResult holds connection details for a test SpiceDB container.
type SpiceDBResult struct {
	// Endpoint is the gRPC endpoint (e.g. "localhost:12345").
	Endpoint string
	// Token is the preshared key used for authentication.
	Token string
	// Client is a ready-to-use authzed client with experimental APIs.
	Client *authzed.ClientWithExperimental
}

// NewTestSpiceDB starts a temporary SpiceDB container, optionally loads
// a schema from schemaPath (path to a .zed file), and returns connection
// details including a ready-to-use client. The container is terminated via
// t.Cleanup.
//
// Example:
//
//	s := testutil.NewTestSpiceDB(t, "../../permission-service/schema/schema.zed")
//	resp, err := s.Client.CheckPermission(ctx, &v1.CheckPermissionRequest{...})
//
//	// Without schema:
//	s := testutil.NewTestSpiceDB(t, "")
func NewTestSpiceDB(t *testing.T, schemaPath string) SpiceDBResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "authzed/spicedb:latest",
		ExposedPorts: []string{"50051/tcp", "8443/tcp"},
		Cmd:          []string{"serve", "--grpc-preshared-key", spiceDBPresharedKey, "--http-enabled"},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/healthz").WithPort("8443/tcp").WithStatusCodeMatcher(func(status int) bool {
				return status == 200
			}),
		).WithDeadline(60 * time.Second),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("testutil.NewTestSpiceDB: start spicedb container: %v", err)
	}

	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("testutil.NewTestSpiceDB: terminate spicedb container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("testutil.NewTestSpiceDB: get host: %v", err)
	}

	mappedPort, err := ctr.MappedPort(ctx, "50051/tcp")
	if err != nil {
		t.Fatalf("testutil.NewTestSpiceDB: get mapped port: %v", err)
	}

	endpoint := fmt.Sprintf("%s:%s", host, mappedPort.Port())

	client, err := authzed.NewClientWithExperimentalAPIs(
		endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpcutil.WithInsecureBearerToken(spiceDBPresharedKey),
	)
	if err != nil {
		t.Fatalf("testutil.NewTestSpiceDB: create authzed client: %v", err)
	}

	// Load schema if provided.
	if schemaPath != "" {
		schema, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatalf("testutil.NewTestSpiceDB: read schema file %s: %v", schemaPath, err)
		}

		_, err = client.WriteSchema(ctx, &v1.WriteSchemaRequest{
			Schema: string(schema),
		})
		if err != nil {
			t.Fatalf("testutil.NewTestSpiceDB: write schema: %v", err)
		}
	}

	return SpiceDBResult{
		Endpoint: endpoint,
		Token:    spiceDBPresharedKey,
		Client:   client,
	}
}
