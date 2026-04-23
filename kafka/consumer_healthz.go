package kafka

import (
	"encoding/json"
	"net/http"
	"time"
)

// ConsumerLagEntry is the per-topic shape returned by /healthz/consumers.
// It matches the Sentiae MEDIUM C7 spec exactly so portal/ops dashboards can
// render a consistent table across services.
type ConsumerLagEntry struct {
	Topic         string    `json:"topic"`
	Partition     int       `json:"partition"`
	Lag           int64     `json:"lag"`
	LastCommit    time.Time `json:"last_commit"`
	LastEventType string    `json:"last_event_type"`

	// Diagnostic extras kept below the required fields.
	GroupID        string `json:"group_id,omitempty"`
	MessagesOK     int64  `json:"messages_ok"`
	MessagesFailed int64  `json:"messages_failed"`
}

// ConsumersHealthResponse is the JSON body returned by ConsumersHealthzHandler.
type ConsumersHealthResponse struct {
	Service       string             `json:"service"`
	Status        string             `json:"status"` // "ok" or "degraded"
	Subscriptions []string           `json:"subscriptions"`
	Consumers     []ConsumerLagEntry `json:"consumers"`
	GeneratedAt   time.Time          `json:"generated_at"`
}

// ConsumersHealthzHandler returns an http.Handler intended to be mounted at
// /healthz/consumers. Each registered KafkaConsumer contributes per-topic
// lag+last-commit rows. Pass multiple consumers when a service runs more
// than one consumer group.
//
// Usage (single consumer):
//
//	mux.Handle("/healthz/consumers", kafka.ConsumersHealthzHandler("ops-service", consumer))
//
// Usage (multiple consumers):
//
//	mux.Handle("/healthz/consumers", kafka.ConsumersHealthzHandler("foundry-service",
//	    sagaConsumer, workerConsumer))
//
// Closes Sentiae MEDIUM C7: uniform consumer-lag surface.
func ConsumersHealthzHandler(service string, consumers ...*KafkaConsumer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ConsumersHealthResponse{
			Service:     service,
			Status:      "ok",
			GeneratedAt: time.Now().UTC(),
		}

		seenSub := map[string]bool{}
		for _, c := range consumers {
			if c == nil {
				continue
			}
			for _, t := range c.SubscribedTypes() {
				if seenSub[t] {
					continue
				}
				seenSub[t] = true
				resp.Subscriptions = append(resp.Subscriptions, t)
			}
			for _, h := range c.Health() {
				entry := ConsumerLagEntry{
					Topic:          h.Topic,
					Lag:            h.Lag,
					LastCommit:     h.LastProcessed,
					LastEventType:  h.LastEventType,
					GroupID:        h.GroupID,
					MessagesOK:     h.MessagesOK,
					MessagesFailed: h.MessagesFailed,
				}
				resp.Consumers = append(resp.Consumers, entry)
				if h.MessagesFailed > 0 || h.Lag > 10_000 {
					resp.Status = "degraded"
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
