// Package principal names the platform's well-known, non-human principals.
//
// It is deliberately a leaf: it imports nothing but github.com/google/uuid, so
// that any layer can name a system actor without dragging transport, config or
// logging along with it. platform-kit/tenant already models the *request*
// caller (tenant.Principal); this package is the complementary, much smaller
// idea -- actors that exist independently of any request.
package principal

import "github.com/google/uuid"

// SystemUserID is the platform's single well-known system principal: the actor
// attributed to records the platform originates with no human behind them
// (slash-command replies, spec file ingest, remediation sagas, autonomous
// loops).
//
// Why a real, seeded principal and not a sentinel: the evidence spine requires
// every actor to resolve to a principal. A NULL author breaks message
// provenance, and a zero UUID would be an actor that looks real but is not --
// the "absence becomes a plausible value" failure this platform keeps paying
// for. One named principal that actually exists is the honest answer.
//
// The row backing this id is seeded by
// identity-service/migrations/000028_seed_system_user.up.sql. This constant and
// that migration are the two halves of one fact: change neither alone.
//
// The value intentionally matches work-service's pre-existing system actor
// rather than minting a competing convention (D-162b: ONE well-known system
// user).
//
// Domain layers cannot import this package: §6 restricts internal/domain to
// stdlib, uuid and its own package, and no domain in the fleet imports
// platform-kit. work-service/internal/domain therefore MIRRORS this value as a
// literal, pinned by TestSystemUserID_MatchesPlatformPrincipal in
// work-service/internal/domain/spec_system_user_test.go -- that test fails the
// build if the two diverge. Consumers outside a domain layer (use cases,
// adapters) should import this constant directly rather than add a mirror.
var SystemUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
