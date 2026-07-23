package grpcclient

import (
	"context"
	"log/slog"

	"github.com/sentiae/platform-kit/config"
	"github.com/sentiae/platform-kit/spiffe"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

// NewMeshSource builds the one shared SPIFFE X509 source a service uses for its
// outbound mesh dials, applying the fail-closed posture centrally so the ~17
// call sites stop hand-rolling it (each copy today: spiffe.NewSource fails →
// log "degrade to insecure" → leave a nil source, which silently plaintext-dials
// even under strict — the #pulse-outbound-mtls-fail-open disease).
//
// Posture (mirrors grpcserver.New / grpcclient.Dial, D-162a L2):
//
//   - off/empty  — no source is needed; returns (nil, nil).
//   - strict     — a workload-API/source failure returns (nil, error) so the
//     CONTAINER refuses to boot at source construction with one clear message
//     (the outbound analog of refuse-to-serve). A strict service must not come
//     up with a nil source that would silently plaintext-dial.
//   - permissive — a workload-API/source failure returns (nil, nil) plus a LOUD
//     warn: the service boots and its dials degrade to plaintext-with-warn via
//     Dial (the declared escape hatch — a SPIRE hiccup must be visible, not
//     wedge the service).
//
// The returned source (when non-nil) is owned by the caller and must be Closed
// on shutdown. On success under strict/permissive the caller passes it as
// Config.Source to Dial.
//
// The 17 hand-rolled blocks of the shape
//
//	if pkconfig.MTLSMode() != pkconfig.MTLSModeOff {
//	    src, err := spiffe.NewSource(context.Background())
//	    if err != nil { logger.Error("...degrade to insecure: %v", err); return }
//	    c.mtlsSource = src
//	}
//
// collapse to one call:
//
//	src, err := grpcclient.NewMeshSource(ctx, pkconfig.MTLSMode())
//	if err != nil { return err } // strict + unreachable workload API: refuse to boot
//	c.mtlsSource = src           // nil under off, or under permissive when SPIRE is down
func NewMeshSource(ctx context.Context, mode string) (*workloadapi.X509Source, error) {
	if mode == "" {
		mode = config.MTLSModeOff
	}
	if mode == config.MTLSModeOff {
		return nil, nil
	}

	src, err := spiffe.NewSource(ctx)
	if err != nil {
		if mode == config.MTLSModeStrict {
			// FAIL-CLOSED: strict but the workload API is unreachable. Refuse to
			// boot rather than come up with a nil source that plaintext-dials.
			slog.Default().Error("grpcclient: strict mTLS but SPIFFE source construction failed; refusing to boot",
				"mode", mode, "err", err)
			return nil, err
		}
		// permissive: the declared escape hatch — boot, but loudly, and let each
		// dial degrade to plaintext-with-warn via Dial's nil-source path.
		slog.Default().Warn("grpcclient: permissive mTLS but SPIFFE source construction failed; boot continues, outbound dials degrade to plaintext",
			"mode", mode, "err", err)
		return nil, nil
	}
	return src, nil
}
