// Package temporal is the single Temporal SDK import surface for the platform.
//
// # Boundary rule
//
// Temporal SDK imports (go.temporal.io/sdk/*) are restricted to:
//   - This package (platform-kit/temporal)
//   - Per-service workflow packages: <service>/internal/temporal/{workflows,activities}
//
// Domain (internal/domain) and use case (internal/usecase) layers MUST NOT import
// the SDK. Activities are thin wrappers that call use cases through interfaces;
// the use cases themselves remain SDK-free Go functions.
//
// This boundary is enforced by depguard rules in each service's .golangci.yml.
//
// # Why narrow scope
//
// Temporal owns scheduler + retry + saga + replay debugging. Adopting it
// platform-wide as a "body" creates lock-in. Wrapping our domain code as
// activities lets us swap Temporal for a custom scheduler in ~1-2 weeks if
// needed; activities and use cases stay untouched.
//
// # Usage
//
//	cli, err := temporal.NewClient(ctx, temporal.Config{
//	    HostPort:  cfg.Temporal.HostPort,
//	    Namespace: "default",
//	    Logger:    logger.FromContext(ctx),
//	})
//	defer cli.Close()
//
//	w := temporal.NewWorker(cli, "nudge-engine-tq", temporal.WorkerOptions{})
//	w.RegisterWorkflow(workflows.CronProbe)
//	w.RegisterActivity(activities.RunProbe)
//	if err := w.Start(); err != nil { ... }
//	defer w.Stop()
package temporal
