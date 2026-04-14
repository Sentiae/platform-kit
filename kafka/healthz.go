package kafka

import (
	"encoding/json"
	"net/http"
	"time"
)

// HealthzResponse is the JSON body returned by HealthzHandler.
type HealthzResponse struct {
	Status          string        `json:"status"`           // "ok" or "degraded"
	Service         string        `json:"service"`          // producer source name (if set)
	RegisteredTypes int           `json:"registered_types"` // total count in the taxonomy
	Subscriptions   []string      `json:"subscriptions"`    // event types this consumer listens to
	Topics          []topicHealth `json:"topics"`           // per-topic lag + last processed
	GeneratedAt     time.Time     `json:"generated_at"`
}

// HealthzHandler returns an http.Handler that reports this consumer's
// per-topic lag, last processed event, and the subscribed event types.
// Each service can mount this at /healthz/events:
//
//	mux.Handle("/healthz/events", kafka.HealthzHandler(consumer, "my-service"))
//
// If consumer is nil, the handler still serves the registry metadata so
// publish-only services can expose a healthz too.
func HealthzHandler(consumer *KafkaConsumer, service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := HealthzResponse{
			Status:          "ok",
			Service:         service,
			RegisteredTypes: len(AllEvents()),
			GeneratedAt:     time.Now().UTC(),
		}

		if consumer != nil {
			resp.Subscriptions = consumer.SubscribedTypes()
			resp.Topics = consumer.Health()

			// Degrade if any topic has >0 failures since last reset, or lag
			// beyond a basic threshold. This is a conservative floor — callers
			// can layer on their own SLOs.
			for _, t := range resp.Topics {
				if t.MessagesFailed > 0 || t.Lag > 10_000 {
					resp.Status = "degraded"
					break
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if resp.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}
