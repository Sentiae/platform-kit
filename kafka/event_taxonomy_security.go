package kafka

// Security-domain events (vigil). Registered 2026-07-26 to close
// #three-event-streams-are-rejected-at-the-publisher: vigil published these
// unregistered, so every publish was rejected. Only types with a live
// publish site are registered (plus finding.updated, part of the finding
// lifecycle contract). vigil's DAST/discovery/compliance constants had no
// publisher and were deleted, not registered.
//
// Kept in a sibling file so the headline event_taxonomy.go doesn't grow
// unbounded; Go orders same-package init() functions by file name, so this
// file's init() runs after the registry map is built.

const (
	EventSecurityFindingCreated      = "security.finding.created"
	EventSecurityFindingUpdated      = "security.finding.updated"
	EventSecurityFindingResolved     = "security.finding.resolved"
	EventSecurityFindingSLABreach    = "security.finding.sla_breach"
	EventSecurityAlertCritical       = "security.alert.critical"
	EventSecurityScanStarted         = "security.scan.started"
	EventSecurityScanCompleted       = "security.scan.completed"
	EventSecurityScanFailed          = "security.scan.failed"
	EventSecuritySecretDetected      = "security.secret.detected"
	EventSecurityAssetDiscovered     = "security.asset.discovered"
	EventSecurityAgentOffline        = "security.agent.offline"
	EventSecurityAttackChainDetected = "security.attack_chain.detected"

	// vigil's code-graph refresh fan-out (usecase/graph_refresh.go).
	EventCodeGraphUpdated = "code.graph.updated"
)

// findingProps is the shared metadata property set for the finding-shaped
// events (created / updated / alert.critical) — vigil builds one metadata
// map in IngestFindings and publishes it under whichever type applies.
const findingProps = `"finding_id":{"type":"string","minLength":1},` +
	`"severity":{"type":"string"},` +
	`"title":{"type":"string"},` +
	`"category":{"type":"string"},` +
	`"analysis_type":{"type":"string"},` +
	`"normalized_score":{"type":"number"},` +
	`"source_scanner":{"type":"string"},` +
	`"cves":{"type":"array"},` +
	`"cvss_score":{"type":"number"},` +
	`"epss_score":{"type":"number"},` +
	`"description":{"type":"string"},` +
	`"remediation":{"type":"string"},` +
	`"repository":{"type":"string"},` +
	`"file_path":{"type":"string"},` +
	`"line_start":{"type":"integer"},` +
	`"commit_sha":{"type":"string"}`

// init injects the security-domain events into the shared registry at
// package init. Must not touch registryMu here — init() in
// event_taxonomy.go builds the registry serially before any goroutine can
// observe it, and this init() runs after because Go orders same-package
// init() functions by file name.
func init() {
	extras := []RegisteredEvent{
		{
			Type:        EventSecurityFindingCreated,
			Domain:      "security",
			Description: "vigil ingested a new security finding",
			Owner:       "vigil-service",
			Schema: dataSchema("security.finding.created",
				[]string{"finding_id", "severity"}, findingProps),
		},
		{
			Type:        EventSecurityFindingUpdated,
			Domain:      "security",
			Description: "An existing security finding changed (re-scan, enrichment, triage)",
			Owner:       "vigil-service",
			Schema: dataSchema("security.finding.updated",
				[]string{"finding_id"}, findingProps),
		},
		{
			Type:        EventSecurityFindingResolved,
			Domain:      "security",
			Description: "A security finding transitioned to a resolved status",
			Owner:       "vigil-service",
			Schema: dataSchema("security.finding.resolved",
				[]string{"finding_id"},
				`"finding_id":{"type":"string","minLength":1},`+
					`"old_status":{"type":"string"},`+
					`"new_status":{"type":"string"},`+
					`"resolution":{"type":"string"},`+
					`"note":{"type":"string"}`),
		},
		{
			Type:        EventSecurityFindingSLABreach,
			Domain:      "security",
			Description: "A security finding blew through its remediation SLA",
			Owner:       "vigil-service",
			Schema: dataSchema("security.finding.sla_breach",
				[]string{"finding_id"},
				`"finding_id":{"type":"string","minLength":1},`+
					`"severity":{"type":"string"},`+
					`"days_overdue":{"type":"integer"},`+
					`"sla_deadline":{"type":"string"},`+
					`"title":{"type":"string"}`),
		},
		{
			Type:        EventSecurityAlertCritical,
			Domain:      "security",
			Description: "A critical-severity finding was ingested (drives ops' auto-spec creation)",
			Owner:       "vigil-service",
			Schema: dataSchema("security.alert.critical",
				[]string{"finding_id", "severity"}, findingProps),
		},
		{
			Type:        EventSecurityScanStarted,
			Domain:      "security",
			Description: "A vigil scan started",
			Owner:       "vigil-service",
			Schema: dataSchema("security.scan.started",
				[]string{"scan_id"},
				`"scan_id":{"type":"string","minLength":1},`+
					`"scan_type":{"type":"string"},`+
					`"target":{"type":"string"}`),
		},
		{
			Type:        EventSecurityScanCompleted,
			Domain:      "security",
			Description: "A vigil scan completed",
			Owner:       "vigil-service",
			Schema: dataSchema("security.scan.completed",
				[]string{"scan_id"},
				`"scan_id":{"type":"string","minLength":1},`+
					`"scan_type":{"type":"string"},`+
					`"target":{"type":"string"},`+
					`"findings_new":{"type":"integer"},`+
					`"findings_total":{"type":"integer"},`+
					`"duration_ms":{"type":"integer"}`),
		},
		{
			Type:        EventSecurityScanFailed,
			Domain:      "security",
			Description: "A vigil scan failed",
			Owner:       "vigil-service",
			Schema: dataSchema("security.scan.failed",
				[]string{"scan_id"},
				`"scan_id":{"type":"string","minLength":1},`+
					`"scan_type":{"type":"string"},`+
					`"target":{"type":"string"},`+
					`"error":{"type":"string"}`),
		},
		{
			Type:        EventSecuritySecretDetected,
			Domain:      "security",
			Description: "A secret was detected in scanned source",
			Owner:       "vigil-service",
			Schema: dataSchema("security.secret.detected",
				[]string{"finding_id"},
				`"finding_id":{"type":"string","minLength":1},`+
					`"secret_type":{"type":"string"},`+
					`"verified":{"type":"boolean"},`+
					`"repository":{"type":"string"},`+
					`"file_path":{"type":"string"},`+
					`"commit_sha":{"type":"string"}`),
		},
		{
			Type:        EventSecurityAssetDiscovered,
			Domain:      "security",
			Description: "A vigil agent reported a newly discovered asset",
			Owner:       "vigil-service",
			Schema: dataSchema("security.asset.discovered",
				[]string{"agent_id"},
				`"agent_id":{"type":"string","minLength":1},`+
					`"hostname":{"type":"string"},`+
					`"type":{"type":"string"},`+
					`"version":{"type":"string"}`),
		},
		{
			Type:        EventSecurityAgentOffline,
			Domain:      "security",
			Description: "A vigil agent stopped heartbeating and was marked offline",
			Owner:       "vigil-service",
			Schema: dataSchema("security.agent.offline",
				[]string{"agent_id"},
				`"agent_id":{"type":"string","minLength":1},`+
					`"tenant_id":{"type":"string"},`+
					`"last_seen_at":{"type":"string"}`),
		},
		{
			Type:        EventSecurityAttackChainDetected,
			Domain:      "security",
			Description: "vigil correlated findings into an exploitable attack chain",
			Owner:       "vigil-service",
			Schema: dataSchema("security.attack_chain.detected",
				nil,
				`"description":{"type":"string"},`+
					`"severity":{"type":"string"},`+
					`"likelihood":{"type":"string"},`+
					`"finding_count":{"type":"integer"},`+
					`"steps":{"type":"integer"}`),
		},
		{
			Type:        EventCodeGraphUpdated,
			Domain:      "code",
			Description: "vigil refreshed the code graph for a repository at a commit",
			Owner:       "vigil-service",
			Schema: dataSchema("code.graph.updated",
				[]string{"repository_id", "commit_sha"},
				`"repository_id":{"type":"string","minLength":1},`+
					`"commit_sha":{"type":"string","minLength":1},`+
					`"changed_files":{"type":"array"}`),
		},
	}
	for _, e := range extras {
		registry[e.Type] = e
	}
}
