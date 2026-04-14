// kafka-replay is an operator tool for replaying CloudEvents from a Kafka topic
// to stdout (or to a supplied webhook). It is a thin wrapper around
// platform-kit/kafka.Replay and uses a partition reader so it does not disturb
// production consumer-group offsets.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sentiae/platform-kit/kafka"
)

func main() {
	var (
		brokers    = flag.String("brokers", "localhost:9092", "Comma-separated Kafka brokers")
		topic      = flag.String("topic", "", "Kafka topic (required)")
		partition  = flag.Int("partition", 0, "Partition to read")
		startOff   = flag.Int64("start-offset", 0, "Start offset (-2=first, -1=last, 0=first)")
		endOff     = flag.Int64("end-offset", 0, "Exclusive end offset (0=read to log-end)")
		startTime  = flag.String("start-time", "", "RFC3339 start time (overrides start-offset)")
		endTime    = flag.String("end-time", "", "RFC3339 end time")
		types      = flag.String("types", "", "Comma-separated event-type whitelist")
		resourceID = flag.String("resource-id", "", "Only replay events whose data.resource_id matches")
		webhook    = flag.String("webhook", "", "POST each event to this URL instead of stdout")
	)
	flag.Parse()

	if *topic == "" {
		fmt.Fprintln(os.Stderr, "error: --topic is required")
		flag.Usage()
		os.Exit(2)
	}

	cfg := kafka.ReplayConfig{
		Brokers:     strings.Split(*brokers, ","),
		Topic:       *topic,
		Partition:   *partition,
		StartOffset: *startOff,
		EndOffset:   *endOff,
		ResourceID:  *resourceID,
		Logger:      slog.Default(),
	}
	if *startTime != "" {
		t, err := time.Parse(time.RFC3339, *startTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --start-time: %v\n", err)
			os.Exit(2)
		}
		cfg.StartTime = t
	}
	if *endTime != "" {
		t, err := time.Parse(time.RFC3339, *endTime)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --end-time: %v\n", err)
			os.Exit(2)
		}
		cfg.EndTime = t
	}
	if *types != "" {
		cfg.EventTypes = strings.Split(*types, ",")
	}

	enc := json.NewEncoder(os.Stdout)
	cfg.Handler = func(ctx context.Context, e kafka.CloudEvent) error {
		if *webhook != "" {
			buf, err := json.Marshal(e)
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, *webhook, bytes.NewReader(buf))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/cloudevents+json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return fmt.Errorf("webhook returned %s", resp.Status)
			}
			return nil
		}
		return enc.Encode(e)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stats, err := kafka.Replay(ctx, cfg)
	fmt.Fprintf(os.Stderr, "replay: read=%d delivered=%d skipped=%d last_offset=%d\n",
		stats.Read, stats.Delivered, stats.Skipped, stats.LastOffset)
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
