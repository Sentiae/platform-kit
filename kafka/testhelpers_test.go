package kafka

import (
	"errors"
	"io"
	"log/slog"

	kafkago "github.com/segmentio/kafka-go"
)

var errTest = errors.New("test error")

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func fakeKafkaMsg(topic string, value []byte, offset int64) kafkago.Message {
	return kafkago.Message{
		Topic:  topic,
		Value:  value,
		Offset: offset,
		Key:    []byte("key"),
	}
}
