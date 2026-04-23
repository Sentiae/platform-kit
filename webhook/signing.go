// Package webhook contains primitives shared by every service that
// dispatches outbound webhook deliveries. Today: HMAC-SHA256 signing
// and exponential-backoff retry scheduling. Each service keeps its
// own delivery-record schema + worker loop; these helpers exist so
// the signing contract and retry cadence stay uniform across the
// platform (§2.4 of the gap-closure plan — the alternative was three
// slightly different HMAC implementations in git-service,
// notification-service, and work-service).
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// ComputeHMACSHA256 returns the hex-encoded HMAC-SHA256 of payload
// keyed on secret. Matches what receivers need to put in the standard
// `X-Sentiae-Signature: sha256=<hex>` header for verification.
func ComputeHMACSHA256(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
