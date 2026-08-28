package kafka

// Node-registry domain events (DESIGN node-as-repository §2/§4.1, Phase 1).
// Sibling file so the headline registry keeps its foreign in-flight hunks
// untouched; same init() merge as event_taxonomy_conversation.go (init order
// = filename order).

const (
	EventNodeVersionIngested      = "node.version.ingested"
	EventNodeVersionRejected      = "node.version.rejected"
	EventNodeVersionPublished     = "node.version.published"
	EventDeliveryNodeBundleBuilt  = "delivery.node_bundle.built"
	EventDeliveryNodeBundleFailed = "delivery.node_bundle.failed"
)

func init() {
	extras := []RegisteredEvent{
		{
			Type:        EventNodeVersionIngested,
			Domain:      "node",
			Description: "A node version tag was ingested: manifest valid, source archived; a bundle build is requested.",
			Owner:       "node-service",
			Schema: dataSchema(
				"node.version.ingested",
				[]string{"qualified_name", "semver", "repo_ref", "commit_sha", "tag_sha", "source_digest", "implementations"},
				`"qualified_name":{"type":"string","minLength":1},`+
					`"semver":{"type":"string","pattern":"^[0-9]+\\.[0-9]+\\.[0-9]+$"},`+
					`"repo_ref":{"type":"string","minLength":1},`+
					`"commit_sha":{"type":"string","pattern":"^[0-9a-f]{40}$"},`+
					`"tag_sha":{"type":"string","pattern":"^[0-9a-f]{40}$"},`+
					`"source_digest":{"type":"string","pattern":"^[0-9a-f]{64}$"},`+
					`"implementations":{"type":"array","items":{"type":"string"}},`+
					`"tier":{"type":"string"}`,
			),
		},
		{
			Type:        EventNodeVersionRejected,
			Domain:      "node",
			Description: "A node version was refused (immutability conflict, invalid manifest, or a failed build).",
			Owner:       "node-service",
			Schema: dataSchema(
				"node.version.rejected",
				[]string{"qualified_name", "semver", "repo_ref", "reason"},
				`"qualified_name":{"type":"string","minLength":1},`+
					`"semver":{"type":"string"},`+
					`"repo_ref":{"type":"string","minLength":1},`+
					`"reason":{"type":"string","enum":["version_conflict","manifest_missing","manifest_invalid","manifest_name_mismatch","archive_too_large","build_failed"]},`+
					`"detail":{"type":"string"},`+
					`"commit_sha":{"type":"string"},`+
					`"tag_sha":{"type":"string"}`,
			),
		},
		{
			Type:        EventNodeVersionPublished,
			Domain:      "node",
			Description: "Every declared implementation of a node version has a bundle; the version is placeable.",
			Owner:       "node-service",
			Schema: dataSchema(
				"node.version.published",
				[]string{"qualified_name", "semver", "commit_sha", "source_digest", "bundles"},
				`"qualified_name":{"type":"string","minLength":1},`+
					`"semver":{"type":"string"},`+
					`"commit_sha":{"type":"string"},`+
					`"source_digest":{"type":"string"},`+
					`"bundles":{"type":"array","items":{"type":"object","properties":{"language":{"type":"string"},"image_ref":{"type":"string"},"digest":{"type":"string"}},"required":["language","image_ref","digest"]}}`,
			),
		},
		{
			Type:        EventDeliveryNodeBundleBuilt,
			Domain:      "delivery",
			Description: "delivery built one implementation bundle of a node version (Phase 2 producer; registered now so Phase 2 needs no taxonomy change).",
			Owner:       "delivery-service",
			Schema: dataSchema(
				"delivery.node_bundle.built",
				[]string{"qualified_name", "semver", "language", "image_ref", "digest"},
				`"qualified_name":{"type":"string","minLength":1},`+
					`"semver":{"type":"string"},`+
					`"language":{"type":"string","enum":["go","typescript"]},`+
					`"image_ref":{"type":"string","minLength":1},`+
					`"digest":{"type":"string","minLength":1},`+
					`"source_digest":{"type":"string"},`+
					`"commit_sha":{"type":"string"},`+
					`"signature_digest":{"type":"string"},`+
					`"scan_id":{"type":"string"}`,
			),
		},
		// "infrastructure" is NOT a pipeline stage like the seven that precede
		// it: delivery emits it at DLQ time, when a bundle build failed on the
		// ENVIRONMENT rather than on the author's source. node-service records
		// it as a rejection notification WITHOUT failing the version, so the
		// version stays reversible once the environment is repaired. It is kept
		// last so the pipeline steps stay in pipeline order.
		{
			Type:        EventDeliveryNodeBundleFailed,
			Domain:      "delivery",
			Description: "delivery could not produce one implementation bundle of a node version; the version cannot be published.",
			Owner:       "delivery-service",
			Schema: dataSchema(
				"delivery.node_bundle.failed",
				[]string{"qualified_name", "semver", "language", "step", "detail"},
				`"qualified_name":{"type":"string","minLength":1},`+
					`"semver":{"type":"string"},`+
					`"language":{"type":"string","enum":["go","typescript"]},`+
					`"step":{"type":"string","enum":["resolve_org","fetch_source","unpack_source","manifest","build","conformance","scan","infrastructure"]},`+
					`"detail":{"type":"string","minLength":1},`+
					`"source_digest":{"type":"string"},`+
					`"commit_sha":{"type":"string"}`,
			),
		},
	}
	for _, e := range extras {
		registry[e.Type] = e
	}
}
