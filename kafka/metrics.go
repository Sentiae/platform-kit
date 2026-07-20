package kafka

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// messagesDeadLetteredTotal counts poison messages durably routed to a
// dead-letter topic, labelled by SOURCE topic and consumer group. It is
// registered on the default Prometheus registry at package init, so every
// KafkaConsumer in the process shares it. It rides the existing OTLP export
// path via the Prometheus bridge that otel.Init installs — there is no second
// scrape plane. A CounterVec creates no series until WithLabelValues is first
// called, so a consumer that never dead-letters exports nothing (a silent
// zero-valued counter would be indistinguishable from a healthy one).
var messagesDeadLetteredTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kafka_messages_dead_lettered_total",
		Help: "Total Kafka messages routed to a dead-letter topic, by source topic and consumer group.",
	},
	[]string{"topic", "group"},
)
