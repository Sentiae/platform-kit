// Package nudgeevents defines the wire schemas + typed payloads for Eve nudge
// state-transition CloudEvents emitted by foundry-service.
//
// Topics (CloudEvent type field):
//   - sentiae.foundry.nudge.delivered
//   - sentiae.foundry.nudge.seen
//   - sentiae.foundry.nudge.acted
//   - sentiae.foundry.nudge.dismissed
//   - sentiae.foundry.nudge.suppressed
//   - sentiae.foundry.nudge.expired
//   - sentiae.foundry.nudge.superseded
//
// Each event carries a CloudEvents 1.0 envelope plus a typed `data` payload
// describing the state transition. Schemas are JSON Schema and registered with
// the platform schema registry on foundry-service boot via Register.
//
// Consumers (BFF, notification-service, audit-service, ClickHouse Kafka
// engine) decode by `type` field and unmarshal `data` into the matching Go
// struct here.
package nudgeevents
