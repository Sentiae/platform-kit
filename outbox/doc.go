// Package outbox is the platform's shared transactional-outbox library
// (T-HARDEN Wave 3, D-162b). It consolidates the per-service outbox copies onto
// one proven shape — permission-service's status ladder (pending → sending →
// sent / dead) with a real next_retry_at column and a per-row max_retries — and
// adds the D-162b keystone: VALIDATE-AT-APPEND.
//
// Append validates the event type and the CloudEvent data payload against the
// registered taxonomy (platform-kit/kafka) BEFORE inserting the row, inside the
// caller's business transaction. An unregistered event type or a schema-invalid
// payload returns an error, so the caller's transaction rolls back and the bad
// event never persists. That is what would have caught the audit break at first
// commit instead of silently parking poison rows.
//
// A service adopts this package by swapping its publisher for the OutboxPublisher
// (zero call-site changes — it implements kafka.Publisher) and running a Drainer
// in the background. Because each service owns its own tx-context key, the
// concrete GormRepo takes the adopter's dbFromCtx resolver so Append joins the
// adopter's transaction transparently.
package outbox
