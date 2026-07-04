# platform-kit — the shared Go module

**Pillar:** `docs/program/02-pillars/platform-foundations/` · **Constitution:** root `CLAUDE.md` (binding)

The one shared Go module every backend service imports (`github.com/sentiae/platform-kit`): config, errors, logger, kafka, middleware/interceptor, entitlement + quota, health, testutil, and more. It is a **library, not a service** — no `main` (except two CLI tools under `cmd/`), no DB of its own, no business logic. Its job is to make cross-cutting concerns identical everywhere so they can't drift. This pillar exists to **de-duplicate**, so the highest rule here is: a change ripples to ~23 consuming services — keep packages leaf-shaped, backward-compatible within a release, and free of any one service's domain logic.

> Status reality (pillar §11): most of this module is **reuse (built, keep)**. Two things matter for CP0 — the **`platform-kit/otel` helper is NET-NEW** (does not exist today; 7 services copy their own `pkg/telemetry`), and the **entitlement + quota framework already EXISTS and is live** (do NOT redesign it — the compute meters only add two *keys*).

---

## Package map — the real tree, annotated

```
platform-kit/
├── config/          # Load(target, opts) + MustLoad — Viper, APP_ prefix, profiles; vault.go (VaultClient), secret_loader, hot_reload
├── errors/          # Register(sentinel, httpStatus, grpcCode) + ToHTTP/ToGRPC (RFC7807 + status.Error). The §16 boundary.
├── logger/          # New(cfg)*slog.Logger + FromContext(ctx).Info(...) + NewContext. Context keys: trace_id/span_id/request_id/user_id.
├── kafka/           # Publisher/Consumer (CloudEvents 1.0), event_taxonomy, schema_registry/validator, dlq, replay, healthz, noop
├── middleware/      # HTTP (Chi): auth, authheaders, permission, ratelimit, cors, recovery, logging, requestid, chain, quota_check
├── interceptor/     # gRPC: UnaryAuth/Recovery/Logging/Metrics + NewChain(cfg). metrics.go = MetricsCollector (backend-pluggable).
├── entitlement/     # LIVE framework: Entitlement consts, HashSet/Set, Resolver (Redis/Memo/Static), RequireHTTP/RequireUnary, model_selection, tool_gating
├── quota/           # Client — thin HTTP client to identity-service's org quota-check endpoint (Action{AddUser,AddRepo,Deploy,AddSpec,TestRun})
├── authjwt/         # JWT validator (RS256/JWKS)
├── health/          # Checker interface + DatabaseChecker/RedisChecker/KafkaChecker + Handler (the /healthz builder)
├── testutil/        # testcontainers: NewTestDB/NewTestRedis/NewTestKafka/NewTestSpiceDB + FakeJWT/TestRSAKey + assertions
├── validation/  dsl/  audit/  webhook/{,inbound}/  comments/  foundry/  nudgeevents/
├── embedding/       # tiered vector store (pgvector/s3/cold), cache, activity tiering
├── retention/  region/  timetravel/  temporal/  dbmetrics/  debug/(pprof)  atlas/  llm/(provider + providers/{gateway,openai_compat})
├── cmd/{kafka-topics-gen,kafka-replay}/main.go    # the ONLY mains in the module
├── Makefile.include                                # included by each service's Makefile — NOT a standalone Makefile
└── go.mod          # module root.  (NO Makefile, NO README, NO otel/ package)
```

There is **no `otel/` or `telemetry/` package** here today (verified). All `go.opentelemetry.io/*` entries in `go.mod` are `// indirect`.

## Dependency direction (library rules)

This module is a **leaf dependency** — services import it, it never imports a service. Within it:
- Packages are independent utilities; avoid a package importing another platform-kit package unless it's a clear layering (`middleware/quota_check.go` → `quota`, `interceptor/auth.go` → `middleware`). No import cycles, no "god" package.
- No service-specific domain types. If a helper only one service needs, it belongs in that service's `pkg/`, not here.
- Backward compatibility **within a release**: a breaking signature change is a coordinated bump across ~23 `go.mod`s. Prefer additive changes; if you must break, say so loudly (root §22 forbids compat shims, so the break must be clean + coordinated, not shimmed).

## How to add or change a shared package

1. **Search first (root §32).** A helper likely exists — grep the module before adding one. Match the existing package's style (ctx-first, typed keys, table-driven tests).
2. **New package** = new top-level dir with a `doc.go`-style package comment and tests. Keep it single-purpose.
3. **Test with real infra** where relevant via `testutil` (testcontainers), not mocks of infra. Every package here ships with `_test.go` next to source — match that bar.
4. **Consumers pick it up** only when their `go.mod` updates (see pitfall on replace vs pin). A change here is inert until a service bumps.
5. If it's a contract-carrying helper (kafka envelope, entitlement gate, the future otel init), reconcile it with `01-architecture/integration-contracts.md` + `event-catalog.md` before landing.

## Ports & events this module carries helpers for

platform-kit doesn't *own* ports/events — it provides the **helpers services use to honor them**:
- **Kafka envelope / event taxonomy** — `kafka/event.go`, `event_taxonomy*.go`, `schema_registry.go` implement the CloudEvents 1.0 shape + `sentiae.<svc>.<aggregate>.<event>` naming that `event-catalog.md` mandates.
- **Entitlement + quota (invariant I14)** — `entitlement/*` (the ents-hash resolver + gates) and `quota/client.go` are the **live** framework the compute meters wire INTO. `event-catalog.md` adds `sentiae.platform.compute.metered` (`{org_id, kind:build_minutes|microvm_seconds, amount}`); the **net-new** work is two meter *keys* seeded in `identity-service/domain/plan_seeds.go`, **not** a new framework here.
- **P8 (TelemetrySink)** — the **future `platform-kit/otel` helper** (net-new, D-011) is the DI seam every service instruments through: `otel.Init(cfg)` → trace + meter providers + auto-instrument HTTP/gRPC + pipe `otelslog` into `logger`. Reuse anchors already here: `interceptor/metrics.go` (`MetricsCollector`) and `health/`.

## Service-specific rules & pitfalls

1. **NO shared otel package exists — building it is net-new (CP0 build task #2).** Seven services each copy their own `pkg/telemetry`: **codegen, composition, conversation, deployment, identity, knowledge, vigil**. Consolidate ONE `platform-kit/otel` helper, then migrate those 7 off their copies (and delete the copies — root §30.22). Do not add an 8th copy in a service; add the shared package.
2. **The entitlement + quota framework is LIVE — do NOT redesign it.** `entitlement/entitlement.go` (HashSet/Set), `entitlement/resolver.go` (Redis/Memo/Static), `entitlement/middleware.go` (`RequireHTTP`/`RequireUnary`/`LoadSetMiddleware`), `quota/client.go`, `middleware/quota_check.go` are all built and used. The compute meters (`build.compute.minutes`, `microvm.seconds`) are **new keys**, not a new framework (invariant I11 — one owner). Adding an entitlement const: add it here **and** seed it into `identity-service` plan seeds before any endpoint gates on it (per the package doc).
3. **A change here ripples to ~23 services.** Treat every exported signature as a shared contract. Never put a single service's business logic in platform-kit.
4. **`replace` vs pinned pseudo-version is inconsistent.** ~10 service `go.mod`s use `replace github.com/sentiae/platform-kit => ../platform-kit` (local, picks up changes immediately); the rest **pin a pseudo-version** (e.g. `llm-gateway-service`) and only see a change after `go get`. When you change platform-kit, know which consumers auto-pick-up and which need a bump — otherwise "it builds here" hides a stale consumer.
5. **Logger API reality ≠ the constitution's example.** Root §15 shows package-level `logger.Info(ctx, "msg", ...)`. The **actual** API is `logger.New(cfg) *slog.Logger` + `logger.NewContext(ctx, l)` + `logger.FromContext(ctx).Info("msg", ...)` — there is **no package-level `logger.Info`**. Context correlation fields (`trace_id`/`span_id`/`request_id`/`user_id`) are auto-injected by the context handler; the future `otel` helper is what wires `otelslog` in. Call `FromContext(ctx)`; don't reach for a package-level `logger.Info`.
6. **No Makefile / no README.** Only `Makefile.include`, which each service's Makefile includes. The only build artifacts are the two CLI tools under `cmd/` (`kafka-topics-gen`, `kafka-replay`); everything else is library code.
7. **Observability substrate is net-new infra (not code here).** `docker-compose.yml` deploys **zero** observability infra today. Target backends: **logs → ClickHouse, metrics → VictoriaMetrics, traces → ClickHouse, Grafana** single pane; **Loki is DROPPED** (D-019). platform-kit's role is the vendor-neutral OTLP seam (the `otel` helper), never a backend SDK inline.

## External dependencies

Library-level, pulled transitively into consumers: Viper (`config`), `hashicorp/vault/api` (`config/vault.go`), `segmentio/kafka-go` (`kafka`), `golang-jwt/jwt/v5` (`authjwt`), `prometheus/client_golang` (metrics), `authzed/authzed-go` (SpiceDB via `middleware/permission`), `go.temporal.io/sdk` (`temporal`), `minio-go` (`embedding`/`retention` S3), `testcontainers-go` (`testutil`), `go.opentelemetry.io/*` (indirect today — becomes direct when the `otel` package lands).

## Make targets

No standalone Makefile. From the module root:

```
go build ./...
go test ./...                 # unit
go test -tags=integration ./...   # testcontainers (needs Docker) — testutil.NewTestDB/Redis/Kafka/SpiceDB
go vet ./...
```

`Makefile.include` provides the shared targets services inherit (mockery config lives in `mockery.yaml.template`). The `cmd/kafka-topics-gen` + `cmd/kafka-replay` tools build as normal Go binaries.
