package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// ReplayHandler processes a single CloudEvent during a replay run. Returning
// an error aborts the replay immediately.
type ReplayHandler func(ctx context.Context, event CloudEvent) error

// ReplayConfig parameters the Replay function.
//
// Offset controls:
//   - StartOffset: absolute offset to begin replaying from. Use -1 (kafka.LastOffset)
//     or -2 (kafka.FirstOffset) for well-known positions. Default: FirstOffset.
//   - EndOffset:   exclusive upper bound. Zero means "no upper bound" (replay
//     all available messages and stop at the log-end).
//
// Time controls:
//   - StartTime:   if non-zero, overrides StartOffset by seeking to the first
//     message whose timestamp is >= StartTime.
//   - EndTime:     if non-zero, stop once a message's timestamp is > EndTime.
//
// Filtering:
//   - EventTypes:  optional whitelist. When non-empty only matching events are
//     forwarded to Handler (others are skipped silently).
//   - ResourceID:  optional exact-match filter on EventData.ResourceID.
type ReplayConfig struct {
	Brokers     []string
	Topic       string
	Partition   int
	StartOffset int64
	EndOffset   int64
	StartTime   time.Time
	EndTime     time.Time
	EventTypes  []string
	ResourceID  string
	Handler     ReplayHandler
	Logger      *slog.Logger
}

// ReplayStats summarizes what a replay run did.
type ReplayStats struct {
	Read       int   // messages read from Kafka
	Delivered  int   // messages passed to the handler
	Skipped    int   // messages filtered out
	LastOffset int64 // offset of the last message read
}

// Replay reads messages from the configured topic/partition and invokes
// Handler for each message matching the filters. It blocks until the end
// condition is reached or ctx is cancelled.
//
// Unlike the Consumer type, Replay does not use consumer groups — it reads
// directly from the specified partition so replays are deterministic and
// do not disturb production offsets.
func Replay(ctx context.Context, cfg ReplayConfig) (ReplayStats, error) {
	if len(cfg.Brokers) == 0 {
		return ReplayStats{}, fmt.Errorf("kafka replay: at least one broker is required")
	}
	if cfg.Topic == "" {
		return ReplayStats{}, fmt.Errorf("kafka replay: topic is required")
	}
	if cfg.Handler == nil {
		return ReplayStats{}, fmt.Errorf("kafka replay: handler is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:   cfg.Brokers,
		Topic:     cfg.Topic,
		Partition: cfg.Partition,
		MinBytes:  1,
		MaxBytes:  10e6,
	})
	defer reader.Close()

	// Seek: prefer StartTime, fall back to StartOffset.
	if !cfg.StartTime.IsZero() {
		if err := reader.SetOffsetAt(ctx, cfg.StartTime); err != nil {
			return ReplayStats{}, fmt.Errorf("seek to time %s: %w", cfg.StartTime, err)
		}
	} else {
		start := cfg.StartOffset
		if start == 0 {
			start = kafka.FirstOffset
		}
		if err := reader.SetOffset(start); err != nil {
			return ReplayStats{}, fmt.Errorf("seek to offset %d: %w", start, err)
		}
	}

	typeSet := make(map[string]bool, len(cfg.EventTypes))
	for _, t := range cfg.EventTypes {
		typeSet[t] = true
	}

	var stats ReplayStats
	for {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}

		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return stats, nil
			}
			// A read error at the log-end is normal when replay is bounded;
			// surface it to the caller in case they care.
			return stats, fmt.Errorf("replay read: %w", err)
		}

		stats.Read++
		stats.LastOffset = msg.Offset

		if cfg.EndOffset > 0 && msg.Offset >= cfg.EndOffset {
			return stats, nil
		}
		if !cfg.EndTime.IsZero() && msg.Time.After(cfg.EndTime) {
			return stats, nil
		}

		var event CloudEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			logger.Warn("replay: skipping malformed event",
				"offset", msg.Offset,
				"error", err,
			)
			stats.Skipped++
			continue
		}

		if len(typeSet) > 0 && !typeSet[event.Type] {
			stats.Skipped++
			continue
		}
		if cfg.ResourceID != "" {
			// Decode EventData just-in-time to apply resource filter.
			var data EventData
			_ = json.Unmarshal(event.Data, &data)
			if !strings.EqualFold(data.ResourceID, cfg.ResourceID) {
				stats.Skipped++
				continue
			}
		}

		if err := cfg.Handler(ctx, event); err != nil {
			return stats, fmt.Errorf("replay handler (offset %d): %w", msg.Offset, err)
		}
		stats.Delivered++
	}
}
