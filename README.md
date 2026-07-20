# platform-kit

Shared Go libraries for the Sentiae fleet (logger, errors, config, kafka, otel,
outbox, posture, tenantdb, interceptor, grpcserver, …). Consumed by every
service as a **versioned, Athens-served module** — never a local `replace`.

> Authority: this repo is the single source of truth for the shared libraries.
> Each service pins a released tag (e.g. `v0.3.1`) and fetches it through the
> self-hosted Athens Go proxy (`http://10.0.10.20:3000`). A local
> `replace github.com/sentiae/platform-kit => ../platform-kit` in a service
> go.mod is drift and is rejected by the fleet drift gate
> (`infrastructure/scripts/tidy-fleet.sh --verify`).

## Releasing

A platform-kit change reaches the fleet by **cutting an immutable tag** and
fanning it out — never by editing a consumer's `replace`. The procedure:

1. **Land the change on `main`** in this repo (its own tests green). Do NOT edit
   a published tag — tags are immutable; a fix is always a new tag.
2. **Tag the release** and push it:
   ```sh
   git tag v0.3.2 && git push origin v0.3.2
   ```
   Athens serves the tag on first fetch (immutable, append-only cache). Semantic
   versioning: patch = additive/back-compat, minor = new surface, no majors
   pre-1.0 (breaking changes are minor bumps here and each is called out).
3. **Fan out to every consumer** with the tidy tool (routes through Athens, drops
   any stale `replace`, proves each still builds):
   ```sh
   PK_VER=v0.3.2 infrastructure/scripts/tidy-fleet.sh <service> [<service>...]
   ```
   Commit each service's `go.mod`/`go.sum` bump in **its own repo** (one commit
   per repo — the fleet is many modules, not a monorepo).
4. **Verify the whole fleet is clean** (read-only drift gate — mutates nothing):
   ```sh
   infrastructure/scripts/tidy-fleet.sh --verify
   ```
   It auto-discovers every platform-kit consumer, runs the static no-replace
   guard, and `go build -mod=readonly` (so an incomplete `go.sum` or a needed
   `replace` FAILS rather than being silently repaired). Must print
   `FLEET VERIFY: PASS`. In CI this runs clean-clone on the S9 self-hosted runner.
5. **Redeploy every bumped service that is deployed** in the *same effort* — a
   committed bump that is not deployed is drift between source and the running
   binary. `scripts/deploy.sh -- <service>...`, then confirm healthy.

### Local multi-module development

For editing platform-kit and a consumer together without cutting a tag, use an
**uncommitted** `go.work` at the checkout root (`go work init ./platform-kit
./<service>`). `go.work` is git-ignored and never committed — the committed
source always pins a released tag, so a clean clone (and CI) resolves through
Athens with no workspace. The tidy tool forces `GOWORK=off` so it always acts on
the individual module regardless of any active workspace.
