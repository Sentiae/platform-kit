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

// EveAgentID is the platform's single well-known AI-agent principal: the actor
// attributed to content Eve authors (chat replies, page/body edits, autonomous
// turns). Like SystemUserID it is a real, seeded principal rather than a
// sentinel -- the evidence spine requires every message/edit author to resolve
// to a principal, and Eve's UUID was previously written as messages.author_id
// (conversation-service) and as a body-edit actor (bff) with NO backing row, so
// the author resolved to nobody. That is the same "an actor that is not a
// principal" failure SystemUserID exists to prevent (C3: one principal
// namespace; agents are rows, not per-service conventions).
//
// The value intentionally matches conversation-service's pre-existing EVE actor
// (the one persisted in live messages.author_id), unifying the two competing
// constants (conversation's ...e7e0 and bff's 0e7e0000-...) onto ONE canonical
// id rather than minting a third.
//
// Eve is a principal, NOT a D-177 probe-agent enrollment: D-177 credentials
// untrusted binaries on (eventually) customer hardware via per-agent Vault-PKI
// certs; Eve runs inside the platform trust boundary as a feature. When the C3
// principals registry lands, this id becomes a kind=agent row; this seed is
// forward-compatible with it.
//
// The row backing this id is seeded by identity-service (SeedEvePrincipal, NULL
// password_hash so login is impossible, eve@sentiae.invalid). This constant and
// that seed are two halves of one fact: change neither alone.
var EveAgentID = uuid.MustParse("00000000-0000-0000-0000-00000000e7e0")
