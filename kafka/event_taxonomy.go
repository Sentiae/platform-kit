package kafka

// Package kafka's event_taxonomy.go is the single source of truth for every
// platform-wide event. Producers MUST publish only event types registered
// here; consumers validate against the same registry before dispatch.
//
// Schemas are intentionally permissive on Metadata: the CloudEvent envelope
// (EventData) has fixed fields and callers are free to add contextual
// metadata. Each entry documents the required fields producers MUST supply.
//
// Naming: bare event types here are the ones supplied to publisher.Publish
// (no "sentiae." prefix). Full topic names — what's on the wire — are
// "sentiae.{domain}.{resource}" (see FullTopic).
//
// When adding a new event:
//  1. Add the constant to the right domain block.
//  2. Add a RegisteredEvent entry with a meaningful JSON schema.
//  3. Run `go test ./...` in platform-kit — the registry integrity test
//     makes sure every constant has a registered schema.

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ---------- Work domain --------------------------------------------------

const (
	EventWorkSpecCreated       = "work.spec.created"
	EventWorkSpecUpdated       = "work.spec.updated"
	EventWorkSpecDeleted       = "work.spec.deleted"
	EventWorkSpecStatusChanged = "work.spec.status_changed"

	EventWorkFeatureCreated       = "work.feature.created"
	EventWorkFeatureUpdated       = "work.feature.updated"
	EventWorkFeatureDeleted       = "work.feature.deleted"
	EventWorkFeatureStatusChanged = "work.feature.status_changed"
	EventWorkFeatureEventCreated  = "work.feature.event_created"

	EventWorkSignalIngested   = "work.signal.ingested"
	EventWorkSignalResolved   = "work.signal.resolved"
	EventWorkSignalClassified = "work.signal.classified"

	EventWorkAcceptanceCriterionStatusChanged = "work.acceptance_criterion.status_changed"
	EventWorkAcceptanceCriterionCreated       = "work.acceptance_criterion.created"

	// Aliases used by work-service publisher (kept as separate registrations
	// so producer code doesn't need renaming and consumers can subscribe to
	// either form).
	EventWorkCriterionCreated       = "work.criterion.created"
	EventWorkCriterionCompleted     = "work.criterion.completed"
	EventWorkCriterionStatusChanged = "work.criterion.status_changed"

	EventWorkSpecLinkedToNode        = "work.spec.linked_to_node"
	EventWorkSpecUnlinkedFromNode    = "work.spec.unlinked_from_node"
	EventWorkSpecLinkedToFeature     = "work.spec.linked_to_feature"
	EventWorkSpecUnlinkedFromFeature = "work.spec.unlinked_from_feature"

	EventWorkSignalReceived       = "work.signal.received"
	EventWorkFeatureHealthChanged = "work.feature.health_changed"

	EventWorkExperimentCreated   = "work.experiment.created"
	EventWorkExperimentStarted   = "work.experiment.started"
	EventWorkExperimentCompleted = "work.experiment.completed"

	EventWorkItemAssigned      = "work.item.assigned"
	EventWorkItemUnassigned    = "work.item.unassigned"
	EventWorkItemStatusChanged = "work.item.status_changed"
	EventWorkItemCommented     = "work.item.commented"
	EventWorkItemMentioned     = "work.item.mentioned"

	EventWorkProjectCreated     = "work.project.created"
	EventWorkProjectUpdated     = "work.project.updated"
	EventWorkProjectDeleted     = "work.project.deleted"
	EventWorkProjectActivityLog = "work.project.activity_log"

	EventWorkContentCreated = "work.content.created"
	EventWorkContentUpdated = "work.content.updated"
	EventWorkContentDeleted = "work.content.deleted"

	// Hierarchical spec decomposition
	EventWorkSpecChildCreated = "work.spec.child_created"
	EventWorkSpecDecomposed   = "work.spec.decomposed"

	// Feature metrics (usage, code-health, cost attribution)
	EventWorkFeatureMetricsUpdated    = "work.feature.metrics_updated"
	EventWorkFeatureCodeHealthUpdated = "work.feature.code_health_updated"
	EventWorkFeatureCostUpdated       = "work.feature.cost_updated"

	// Tier-3 autonomous-loop signaling (§19.3). work-service emits
	// decomposition_triggered after sending a decompose_spec request to
	// foundry-service so auditing, Pulse, and cooldowns have a single
	// per-attempt record.
	EventWorkSpecDecompositionTriggered = "work.spec.decomposition_triggered"
)

// ---------- Analytics domain ---------------------------------------------

const (
	// EventAnalyticsUsageRecorded is emitted by portal/frontend telemetry each
	// time a user takes an action against a feature. The work-service usage
	// aggregator consumes this stream to roll it up into DAU/MAU counters.
	EventAnalyticsUsageRecorded = "analytics.usage.recorded"
)

// ---------- Git domain ---------------------------------------------------

const (
	EventGitRepositoryCreated = "git.repository.created"
	EventGitRepositoryUpdated = "git.repository.updated"
	EventGitRepositoryDeleted = "git.repository.deleted"

	EventGitBranchCreated = "git.branch.created"
	EventGitBranchDeleted = "git.branch.deleted"

	EventGitPush = "git.push.received"

	EventGitCommitAuthored = "git.commit.authored"

	EventGitSessionStarted   = "git.session.started"
	EventGitSessionCompleted = "git.session.completed"
	EventGitSessionCancelled = "git.session.cancelled"

	EventGitPRCreated         = "git.pr.created"
	EventGitPRUpdated         = "git.pr.updated"
	EventGitPRMerged          = "git.pr.merged"
	EventGitPRClosed          = "git.pr.closed"
	EventGitPRReviewSubmitted = "git.pr.review_submitted"

	EventGitAIReviewRequested = "git.ai_review.requested"
	EventGitAIReviewCompleted = "git.ai_review.completed"

	EventGitReleaseCreated = "git.release.created"
	EventGitDocStale       = "git.doc.stale"

	// Symbol index lifecycle — published when git-service finishes (or
	// incrementally updates) the symbol index for a repository. Canvas
	// subscribes to trigger a graph refresh (§7 audit gap #4).
	EventGitSymbolIndexUpdated = "git.symbol_index.updated"
)

// ---------- Ops domain ---------------------------------------------------

const (
	EventOpsDeploymentStarted    = "ops.deployment.started"
	EventOpsDeploymentCompleted  = "ops.deployment.completed"
	EventOpsDeploymentPromoted   = "ops.deployment.promoted"
	EventOpsDeploymentRolledBack = "ops.deployment.rolled_back"
	EventOpsDeploymentCorrelated = "ops.deployment.correlated"

	EventOpsAlertTriggered    = "ops.alert.triggered"
	EventOpsAlertDiagnosed    = "ops.alert.diagnosed"
	EventOpsAlertAcknowledged = "ops.alert.acknowledged"
	EventOpsAlertResolved     = "ops.alert.resolved"

	EventOpsIncidentOpened         = "ops.incident.opened"
	EventOpsIncidentResolved       = "ops.incident.resolved"
	EventOpsIncidentWorkItemLinked = "ops.incident.work_item_linked"
	EventOpsIncidentChannelCreated = "ops.incident.channel_created"

	EventOpsProbeFailure   = "ops.probe.failure"
	EventOpsProbeRecovered = "ops.probe.recovered"
	EventOpsProbeResult    = "ops.probe.result"

	EventOpsDeliveryGatePassed = "ops.delivery_gate.passed"
	EventOpsDeliveryGateFailed = "ops.delivery_gate.failed"

	EventOpsPipelineStarted   = "ops.pipeline.started"
	EventOpsPipelineCompleted = "ops.pipeline.completed"
	EventOpsPipelineFailed    = "ops.pipeline.failed"

	EventOpsFeatureFlagCreated     = "ops.feature_flag.created"
	EventOpsFeatureFlagUpdated     = "ops.feature_flag.updated"
	EventOpsFeatureFlagStale       = "ops.feature_flag.stale_detected"
	EventOpsFeatureFlagRolloutStep = "ops.feature_flag.rollout_step"

	EventOpsSLOBurnAlert          = "ops.slo.burn_alert"
	EventOpsSecurityScanCompleted = "ops.security_scan.completed"
	EventOpsAutoRollback          = "ops.deploy.auto_rollback"
	EventOpsAutoscalingScale      = "ops.autoscaling.scale"
	EventOpsConfigDriftDetected   = "ops.config_drift.detected"
	EventOpsConfigDriftReconciled = "ops.config_drift.reconciled"
	EventOpsTestTriggered         = "ops.test.triggered"
	EventOpsMonitoringAnomaly     = "ops.monitoring.anomaly"

	// ---- ops.deploy.* aliases (DeploymentUseCase / DeployExecutor emit
	// these names in addition to the canonical ops.deployment.* events).
	EventOpsDeployCreated        = "ops.deploy.created"
	EventOpsDeployStarted        = "ops.deploy.started"
	EventOpsDeployInProgress     = "ops.deploy.in_progress"
	EventOpsDeployCompleted      = "ops.deploy.completed"
	EventOpsDeployFailed         = "ops.deploy.failed"
	EventOpsDeployRolledBack     = "ops.deploy.rolled_back"
	EventOpsDeployRollbackFailed = "ops.deploy.rollback_failed"
	EventOpsDeployPromoted       = "ops.deploy.promoted"
	EventOpsDeploymentFailed     = "ops.deployment.failed"

	// Tier-3 autonomous-loop signalling (§19.3). Emitted by the
	// quality-gate evaluator consumer when every required delivery_gate
	// attached to a deployment clears after a runtime.test.completed
	// event. Distinct from ops.deploy.requested (which is the legacy
	// bridge event) so the canonical CloudEvent names used by the §19.3
	// loop are unambiguously ops.deployment.*.
	EventOpsDeploymentRequested = "ops.deployment.requested"

	// ---- additional ops.alert.* events
	EventOpsAlertFired    = "ops.alert.fired"
	EventOpsAlertSilenced = "ops.alert.silenced"

	// ---- additional ops.incident.* events
	EventOpsIncidentCreated           = "ops.incident.created"
	EventOpsIncidentSeverityChanged   = "ops.incident.severity_changed"
	EventOpsIncidentStatusChanged     = "ops.incident.status_changed"
	EventOpsIncidentPostMortemCreated = "ops.incident.post_mortem_created"

	// ---- additional ops.probe.* events
	EventOpsProbeFailed       = "ops.probe.failed"
	EventOpsProbeResultSignal = "ops.probe.result_signal"

	// ---- additional ops.delivery_gate.* events
	EventOpsDeliveryGatePassing    = "ops.delivery_gate.passing"
	EventOpsDeliveryGateFailing    = "ops.delivery_gate.failing"
	EventOpsDeliveryGateOverridden = "ops.delivery_gate.overridden"

	// ---- additional ops.pipeline.* events
	EventOpsPipelineCancelled = "ops.pipeline.cancelled"

	// ---- ops.self_healing
	EventOpsSelfHealingTriggered = "ops.self_healing.triggered"

	// ---- ops.preview.* (preview environments)
	EventOpsPreviewCreated = "ops.preview.created"
	EventOpsPreviewUpdated = "ops.preview.updated"
	EventOpsPreviewDeleted = "ops.preview.deleted"

	// ---- ops.cost.* (cost analytics)
	EventOpsCostAnomaly         = "ops.cost.anomaly"
	EventOpsCostBudgetThreshold = "ops.cost.budget_threshold"

	// ---- ops.compliance.*
	EventOpsComplianceContinuousReport = "ops.compliance.continuous_report"

	// ---- ops.monitoring.*
	EventOpsMonitoringPredictiveAnomaly = "ops.monitoring.predictive_anomaly"

	// ---- ops.service.* (service catalog lifecycle)
	EventOpsServiceCreated          = "ops.service.created"
	EventOpsServiceUpdated          = "ops.service.updated"
	EventOpsServiceDeleted          = "ops.service.deleted"
	EventOpsServiceLifecycleChanged = "ops.service.lifecycle_changed"
	EventOpsServiceDiscovered       = "ops.service.discovered"

	// ---- ops.target.* (deploy targets)
	EventOpsTargetCreated = "ops.target.created"
	EventOpsTargetUpdated = "ops.target.updated"
	EventOpsTargetDeleted = "ops.target.deleted"

	// ---- ops.test.* (test orchestration)
	EventOpsTestPostMergeTriggered = "ops.test.post_merge_triggered"
	EventOpsTestOnCommitTriggered  = "ops.test.on_commit_triggered"
	EventOpsTestOnSessionTriggered = "ops.test.on_session_triggered"

	// ---- ops.admin.* (admin / impersonation audit)
	EventOpsAdminImpersonationStarted = "ops.admin.impersonation.started"
)

// ---------- Canvas domain ------------------------------------------------

const (
	EventCanvasCreated = "canvas.canvas.created"
	EventCanvasUpdated = "canvas.canvas.updated"
	EventCanvasDeleted = "canvas.canvas.deleted"

	EventCanvasNodeCreated   = "canvas.node.created"
	EventCanvasNodeUpdated   = "canvas.node.updated"
	EventCanvasNodeDeleted   = "canvas.node.deleted"
	EventCanvasNodeCertified = "canvas.node.certified"
	EventCanvasNodeGenerated = "canvas.node.generated"
	EventCanvasNodePublished = "canvas.node.published"
	EventCanvasNodeLinked    = "canvas.node.linked"
	EventCanvasNodeUnlinked  = "canvas.node.unlinked"

	EventCanvasEdgeCreated = "canvas.edge.created"
	EventCanvasEdgeDeleted = "canvas.edge.deleted"

	EventCanvasGroupCreated = "canvas.group.created"
	EventCanvasGroupUpdated = "canvas.group.updated"
	EventCanvasGroupDeleted = "canvas.group.deleted"

	EventCanvasDebugSessionCreated   = "canvas.debug.session.created"
	EventCanvasDebugSessionStarted   = "canvas.debug.session.started"
	EventCanvasDebugSessionPaused    = "canvas.debug.session.paused"
	EventCanvasDebugSessionCancelled = "canvas.debug.session.cancelled"
	EventCanvasDebugSessionFailed    = "canvas.debug.session.failed"
	EventCanvasDebugSessionCompleted = "canvas.debug.session.completed"
	EventCanvasDebugStepCompleted    = "canvas.debug.step.completed"

	EventCanvasAPIGenerated   = "canvas.api.generated"
	EventCanvasAPIDeployed    = "canvas.api.deployed"
	EventCanvasAPIStopWarning = "canvas.api.stop_warning"
	EventCanvasAPIStopped     = "canvas.api.stopped"
	EventCanvasAPIDeleted     = "canvas.api.deleted"

	EventCanvasWorkflowCreated          = "canvas.workflow.created"
	EventCanvasWorkflowScheduledTrigger = "canvas.workflow.scheduled_trigger"

	EventCanvasCodegenCompleted = "canvas.codegen.completed"

	EventCanvasRuntimeDeployed   = "canvas.runtime.deployed"
	EventCanvasRuntimeUndeployed = "canvas.runtime.undeployed"

	EventCanvasSyncEnabled          = "canvas.sync.enabled"
	EventCanvasSyncCompleted        = "canvas.sync.completed"
	EventCanvasSyncConflictDetected = "canvas.sync.conflict_detected"
	EventCanvasSyncConflictResolved = "canvas.sync.conflict_resolved"
	EventCanvasSyncStatusChanged    = "canvas.sync.status_changed"
	EventCanvasSyncCanvasToCodeReq  = "canvas.sync.canvas_to_code.requested"
	EventCanvasSyncCodeToCanvasReq  = "canvas.sync.code_to_canvas.requested"

	EventCanvasMarketplacePublisherConnected = "canvas.marketplace.publisher.connected"
	EventCanvasMarketplacePurchaseCreated    = "canvas.marketplace.purchase.created"
	EventCanvasMarketplacePurchaseCompleted  = "canvas.marketplace.purchase.completed"
	EventCanvasMarketplacePayoutRequested    = "canvas.marketplace.payout.requested"
	EventCanvasMarketplacePayoutCompleted    = "canvas.marketplace.payout.completed"
	EventCanvasMarketplacePaymentSucceeded   = "canvas.marketplace.payment.succeeded"
	EventCanvasMarketplaceAccountUpdated     = "canvas.marketplace.account.updated"
	EventCanvasMarketplaceWebhookUnhandled   = "canvas.marketplace.webhook.unhandled"
	EventCanvasMarketplacePublished          = "canvas.marketplace.published"

	EventCanvasSecretCreated = "canvas.secret.created"
	EventCanvasSecretUpdated = "canvas.secret.updated"
	EventCanvasSecretDeleted = "canvas.secret.deleted"

	EventCanvasCompositeNodeCreated = "canvas.composite_node.created"
	EventCanvasNodeDependencyAdded  = "canvas.node.dependency.added"
	EventCanvasEjectCompleted       = "canvas.eject.completed"

	// Repository import lifecycle (canvas-service repo_import_usecase).
	EventCanvasImportProgress  = "canvas.import.progress"
	EventCanvasImportCompleted = "canvas.import.completed"
	EventCanvasImportFailed    = "canvas.import.failed"

	// External proxy lifecycle (canvas-service external_proxy_usecase).
	EventCanvasExternalProxyCreated = "canvas.external_proxy.created"

	// Edge comment lifecycle (§7 audit gap #2)
	EventCanvasEdgeCommentCreated  = "canvas.edge_comment.created"
	EventCanvasEdgeCommentUpdated  = "canvas.edge_comment.updated"
	EventCanvasEdgeCommentDeleted  = "canvas.edge_comment.deleted"
	EventCanvasEdgeCommentResolved = "canvas.edge_comment.resolved"
)

// ---------- Comments domain (cross-cutting "Comments Everywhere") --------
//
// These events carry the polymorphic Comment envelope that the BFF's
// CommentPubSub dispatches to GraphQL Subscription clients. Producers today
// are ops-service (incident/deployment), work-service (work_item/feature/
// spec), and canvas-service (canvas_node). Topic name: "sentiae.comments.events".

const (
	EventCommentCreated = "comments.events.created"
	EventCommentUpdated = "comments.events.updated"
	EventCommentDeleted = "comments.events.deleted"
)

// ---------- Chat domain (conversation-service) --------------------------
//
// conversation-service publishes per-message and per-channel events that
// downstream consumers (foundry BuildPlanIgnitor, chat fan-out, BFF
// subscriptions) react to. Topic name: "sentiae.chat.events".

const (
	EventChatMessageCreated  = "chat.message.created"
	EventChatMessageUpdated  = "chat.message.updated"
	EventChatMessageDeleted  = "chat.message.deleted"
	EventChatChannelCreated  = "chat.channel.created"
	EventChatChannelUpdated  = "chat.channel.updated"
	EventChatChannelDeleted  = "chat.channel.deleted"
	EventChatReactionAdded   = "chat.reaction.added"
	EventChatReactionRemoved = "chat.reaction.removed"
	EventChatTypingStarted   = "chat.typing.started"
	EventChatTypingStopped   = "chat.typing.stopped"
	EventChatPresenceUpdated = "chat.presence.updated"
	EventChatVoiceState      = "chat.voice.state.updated"
	EventChatEveEvent        = "chat.eve.event"
)

// ---------- Presence domain (cross-cutting co-presence) -----------------
//
// Published by bff-service on every BroadcastPresence mutation so sibling
// BFF replicas can fan the fresh snapshot out to their own GraphQL
// subscribers. Topic name: "sentiae.presence.events".

const (
	EventPresenceUpdated = "presence.events.updated"
)

// ---------- Git domain (legacy "sentiae.git.*" event types) ---------------
//
// git-service emits event types that begin with the "sentiae." prefix
// (predates the platform-kit naming convention). Other services
// (work-service, ops-service) subscribe to these names directly. Until the
// producers and all consumers are migrated to the canonical "git.*" names,
// register the legacy names here so schema validation accepts them.

const (
	EventGitLegacyRepositoryCreated  = "sentiae.git.repository.created"
	EventGitLegacyRepositoryImported = "sentiae.git.repository.imported"
	EventGitLegacyRepositoryForked   = "sentiae.git.repository.forked"

	EventGitLegacyPush = "sentiae.git.push"

	EventGitLegacyCommitCreated = "sentiae.git.commit.created"

	EventGitLegacyPRCreated          = "sentiae.git.pr.created"
	EventGitLegacyPRUpdated          = "sentiae.git.pr.updated"
	EventGitLegacyPRMerged           = "sentiae.git.pr.merged"
	EventGitLegacyPRClosed           = "sentiae.git.pr.closed"
	EventGitLegacyPRReviewRequested  = "sentiae.git.pr.review_requested"
	EventGitLegacyPRApproved         = "sentiae.git.pr.approved"
	EventGitLegacyPRChangesRequested = "sentiae.git.pr.changes_requested"
	EventGitLegacyPRReviewSubmitted  = "sentiae.git.pr.review.submitted"

	EventGitLegacyBranchCreated          = "sentiae.git.branch.created"
	EventGitLegacyBranchDeleted          = "sentiae.git.branch.deleted"
	EventGitLegacyBranchProtectedChanged = "sentiae.git.branch.protected_changed"

	EventGitLegacySessionCreated   = "sentiae.git.session.created"
	EventGitLegacySessionCodeReady = "sentiae.git.session.code_ready"
	EventGitLegacySessionMerged    = "sentiae.git.session.merged"
	EventGitLegacySessionClosed    = "sentiae.git.session.closed"

	EventGitLegacyAIReviewCompleted = "sentiae.git.ai_review.completed"
	EventGitLegacyReleaseCreated    = "sentiae.git.release.created"
	EventGitLegacyDocStale          = "sentiae.git.doc.stale"
)

// ---------- Data domain --------------------------------------------------

const (
	EventDataQueryExecuted   = "data.query.executed"
	EventDataQueryTranslated = "data.query.translated"
	EventDataQueryFailed     = "data.query.failed"

	EventDataSourceCreated   = "data.data_source.created"
	EventDataSourceUpdated   = "data.data_source.updated"
	EventDataSourceDeleted   = "data.data_source.deleted"
	EventDataSourceConnected = "data.data_source.connected"

	EventDataDashboardCreated = "data.dashboard.created"
	EventDataDashboardUpdated = "data.dashboard.updated"
	EventDataDashboardDeleted = "data.dashboard.deleted"
	EventDataDashboardShared  = "data.dashboard.shared"

	EventDataSemanticFieldCreated = "data.semantic_field.created"
	EventDataSemanticFieldUpdated = "data.semantic_field.updated"

	EventDataDashboardAlertTriggered = "data.dashboard.alert_triggered"
	EventDataDashboardEmbedded       = "data.dashboard.embedded"
	EventDataSourceSampled           = "data.data_source.sampled"
)

// ---------- Runtime domain -----------------------------------------------

const (
	EventRuntimeExecutionStarted   = "runtime.execution.started"
	EventRuntimeExecutionCompleted = "runtime.execution.completed"
	EventRuntimeExecutionFailed    = "runtime.execution.failed"

	EventRuntimeTestStarted   = "runtime.test.started"
	EventRuntimeTestCompleted = "runtime.test.completed"
	EventRuntimeTestFailed    = "runtime.test.failed"

	// Requested by sagas / orchestrators when they need a runtime
	// execution. runtime-service (or an adapter) picks this up and emits
	// the started / completed / failed variants above.
	EventRuntimeExecutionRequested = "runtime.execution.requested"

	// Checkpoint scheduling: emitted whenever the runtime takes an
	// automatic checkpoint of a long-running VM. Consumers (operations,
	// living-platform) use these to draw "save" markers on the timeline.
	EventRuntimeCheckpointCreated  = "runtime.checkpoint.created"
	EventRuntimeCheckpointRestored = "runtime.checkpoint.restored"

	// Regression test generation: ops emits this when it captures a
	// production trace it wants converted into an automated reproducer.
	// Runtime consumes the event, calls foundry to synthesize the test,
	// and persists a TestRun template linked back to the trace.
	EventOpsTraceCapturedForRegression = "ops.trace.captured_for_regression"
	EventRuntimeRegressionTestCreated  = "runtime.regression_test.created"
)

// ---------- Saga domain --------------------------------------------------
//
// Sagas are event-driven orchestrations spanning multiple services. Each
// saga owns its state machine and emits lifecycle events that observers
// (UI dashboards, the living-platform map, audit log) subscribe to.

const (
	// code_test_deploy saga (Tier 2, Flow 2A): triggered by
	// `sentiae.git.session.code_ready`, runs tests → evaluates the
	// delivery gate → creates a deployment. Owned by ops-service.
	EventSagaCodeTestDeployStarted             = "saga.code_test_deploy.started"
	EventSagaCodeTestDeployTestsRequested      = "saga.code_test_deploy.tests_requested"
	EventSagaCodeTestDeployTestsCompleted      = "saga.code_test_deploy.tests_completed"
	EventSagaCodeTestDeployGateEvaluated       = "saga.code_test_deploy.gate_evaluated"
	EventSagaCodeTestDeployDeploymentRequested = "saga.code_test_deploy.deployment_requested"
	EventSagaCodeTestDeployCompleted           = "saga.code_test_deploy.completed"
	EventSagaCodeTestDeployFailed              = "saga.code_test_deploy.failed"
)

// ---------- Foundry domain -----------------------------------------------

const (
	EventFoundryAgentStarted   = "foundry.agent.started"
	EventFoundryAgentCompleted = "foundry.agent.completed"
	EventFoundryAgentFailed    = "foundry.agent.failed"

	EventFoundryApprovalRequested = "foundry.approval.requested"
	EventFoundryApprovalGranted   = "foundry.approval.granted"
	EventFoundryApprovalRejected  = "foundry.approval.rejected"

	EventFoundryProposalCreated  = "foundry.proposal.created"
	EventFoundryProposalAccepted = "foundry.proposal.accepted"
	EventFoundryProposalRejected = "foundry.proposal.rejected"

	EventFoundryDiagnosisStarted   = "foundry.diagnosis.started"
	EventFoundryDiagnosisCompleted = "foundry.diagnosis.completed"

	// Genesis security scanning (blocks publish on HIGH/CRITICAL findings).
	EventFoundryGenesisSecurityScanCompleted = "foundry.genesis.security_scan_completed"
	EventFoundryGenesisSecurityBlocked       = "foundry.genesis.security_blocked"

	// Reactive rule throttling (triggers dropped due to cooldown or rate-limit).
	EventFoundryReactiveThrottled = "foundry.reactive.throttled"

	// Eve agentic parallel tool execution (for transparency in the UI).
	EventFoundryEveToolCallsParallelStart = "foundry.eve.tool_calls_parallel_start"
	EventFoundryEveToolCallsParallelEnd   = "foundry.eve.tool_calls_parallel_end"

	// Agent memory freshness rescoring (by background worker).
	EventFoundryMemoryStalenessRescored = "foundry.memory.staleness_rescored"

	// Tier-3 autonomous-loop signalling (§19.3). These close the gap
	// between work/canvas and the foundry agents that drive decomposition
	// and code generation so the loops are event-driven end-to-end.
	EventFoundryDecomposeSpecRequested = "foundry.decompose_spec.requested"
	EventFoundryCodeGenStarted         = "foundry.code_gen.started"
	EventFoundryCodeGenCompleted       = "foundry.code_gen.completed"
	EventFoundryCodeGenFailed          = "foundry.code_gen.failed"
)

// ---------- Saga domain --------------------------------------------------
//
// Sagas are long-running, event-driven orchestrations that span multiple
// services. Each saga emits structured lifecycle events so operators (and
// downstream sagas) can observe progress. The self-healing saga chains
// MONITOR→ALERT→DIAGNOSE→FIX by reacting to ops.alert.fired and delegating
// implementation to the spec-driven saga.

const (
	// Self-healing saga (Tier-2 Flow 2D).
	EventSagaSelfHealingStarted          = "saga.self_healing.started"
	EventSagaSelfHealingDiagnosed        = "saga.self_healing.diagnosed"
	EventSagaSelfHealingApprovalRequired = "saga.self_healing.approval_required"
	EventSagaSelfHealingSpecCreated      = "saga.self_healing.spec_created"
	EventSagaSelfHealingDeployed         = "saga.self_healing.deployed"
	EventSagaSelfHealingVerified         = "saga.self_healing.verified"
	EventSagaSelfHealingCompleted        = "saga.self_healing.completed"
	EventSagaSelfHealingFailed           = "saga.self_healing.failed"
	EventSagaSelfHealingRolledBack       = "saga.self_healing.rolled_back"
)

// Spec-driven saga (Tier-2 Flow 2B: SPEC -> CANVAS -> CODE -> TEST).
// These events are emitted by foundry-service's SpecDrivenDevSagaCoordinator
// as it advances the state machine across work/canvas/git/runtime services.

const (
	EventSagaSpecDrivenStarted        = "saga.spec_driven.started"
	EventSagaSpecDrivenDecomposed     = "saga.spec_driven.decomposed"
	EventSagaSpecDrivenCanvasCreated  = "saga.spec_driven.canvas_created"
	EventSagaSpecDrivenSessionCreated = "saga.spec_driven.session_created"
	EventSagaSpecDrivenCodeWritten    = "saga.spec_driven.code_written"
	EventSagaSpecDrivenTestingStarted = "saga.spec_driven.testing_started"
	EventSagaSpecDrivenRetryTriggered = "saga.spec_driven.retry_triggered"
	EventSagaSpecDrivenCompleted      = "saga.spec_driven.completed"
	EventSagaSpecDrivenFailed         = "saga.spec_driven.failed"
)

// Import-analyze-canvas saga (Tier-2 Flow 2C: IMPORT -> ANALYZE -> CANVAS).
// Emitted by canvas-service's ImportAnalyzeCanvasSagaCoordinator as it
// bulk-creates nodes, infers edges, and runs layout after git-service
// finishes import analysis. Bare name (after "sentiae." is stripped by the
// publisher); full topic becomes sentiae.canvas.saga.import_analyze_canvas.*.
const (
	EventSagaImportAnalyzeCanvasStarted   = "canvas.saga.import_analyze_canvas.started"
	EventSagaImportAnalyzeCanvasNodes     = "canvas.saga.import_analyze_canvas.nodes_created"
	EventSagaImportAnalyzeCanvasEdges     = "canvas.saga.import_analyze_canvas.edges_inferred"
	EventSagaImportAnalyzeCanvasLayout    = "canvas.saga.import_analyze_canvas.layout_applied"
	EventSagaImportAnalyzeCanvasCompleted = "canvas.saga.import_analyze_canvas.completed"
	EventSagaImportAnalyzeCanvasFailed    = "canvas.saga.import_analyze_canvas.failed"
)

// NL-query saga (Tier-2 Flow 2E: NL question -> data -> auto-viz -> canvas).
// Emitted by foundry-service's NLQuerySagaUseCase.
const (
	EventSagaNLQueryStarted    = "saga.nl_query.started"
	EventSagaNLQueryTranslated = "saga.nl_query.translated"
	EventSagaNLQueryExecuted   = "saga.nl_query.executed"
	EventSagaNLQueryRendered   = "saga.nl_query.rendered"
	EventSagaNLQueryCompleted  = "saga.nl_query.completed"
	EventSagaNLQueryFailed     = "saga.nl_query.failed"
)

// ---------- Pulse domain (flow visualization) ---------------------------

// Pulse-service watches every saga.* event and publishes these
// higher-level flow lifecycle events. Consumers that want a single
// digestible "something is happening cross-service" stream can subscribe
// to pulse.flow.* instead of every individual saga topic.
const (
	EventPulseFlowCreated       = "pulse.flow.created"
	EventPulseFlowStepStarted   = "pulse.flow.step_started"
	EventPulseFlowStepCompleted = "pulse.flow.step_completed"
	EventPulseFlowCompleted     = "pulse.flow.completed"
	EventPulseFlowFailed        = "pulse.flow.failed"
	EventPulseFlowReplayed      = "pulse.flow.replayed"

	// EventAuditReplayExecuted is emitted by pulse-service when an admin
	// replays a batch of audited events via POST /audit/replay. Downstream
	// consumers can distinguish replays from live traffic by this type.
	EventAuditReplayExecuted = "audit.replay.executed"
)

// ---------- Identity domain ---------------------------------------------

const (
	EventIdentityUserRegistered      = "identity.user.registered"
	EventIdentityUserUpdated         = "identity.user.updated"
	EventIdentityUserDeleted         = "identity.user.deleted"
	EventIdentityUserLoggedIn        = "identity.user.logged_in"
	// EventIdentityUserFirstLogin fires exactly once per user, on the
	// first successful login. Consumed by foundry's reactive rule engine
	// to dispatch Eve's "What would you like to build today?" welcome.
	EventIdentityUserFirstLogin      = "identity.user.first_login"
	EventIdentityUserPresenceChanged = "identity.user.presence_changed"

	EventIdentityOrganizationCreated = "identity.organization.created"
	EventIdentityOrganizationUpdated = "identity.organization.updated"
	EventIdentityOrganizationDeleted = "identity.organization.deleted"

	EventIdentityTeamCreated = "identity.team.created"
	EventIdentityTeamUpdated = "identity.team.updated"
	EventIdentityTeamDeleted = "identity.team.deleted"

	EventIdentityMemberAdded       = "identity.member.added"
	EventIdentityMemberRemoved     = "identity.member.removed"
	EventIdentityMemberRoleChanged = "identity.member.role_changed"

	EventIdentityInvitationCreated  = "identity.invitation.created"
	EventIdentityInvitationAccepted = "identity.invitation.accepted"
	EventIdentityInvitationRevoked  = "identity.invitation.revoked"
)

// RegisteredEvent describes a platform event's shape and ownership.
type RegisteredEvent struct {
	Type        string // bare event type (e.g., "work.spec.created")
	Domain      string // logical grouping (work, git, ops, ...)
	Description string // human-readable purpose
	Owner       string // service that owns and emits the event
	Schema      string // JSON Schema for the CloudEvent.data payload
}

// FullTopic returns the on-the-wire topic for this event with the given prefix.
func (e RegisteredEvent) FullTopic(prefix string) string {
	if prefix == "" {
		prefix = "sentiae"
	}
	return topicFromEventType(prefix, e.Type)
}

// Schemas share a common EventData envelope. dataSchema() is a small helper
// that stitches a payload-metadata fragment into the standard envelope.
// If requiredMetadata is non-empty, "metadata" itself becomes a required
// top-level field (so publishers can't drop the whole object to bypass the
// per-field requirement).
func dataSchema(description string, requiredMetadata []string, metadataProps string) string {
	req := `"resource_type","resource_id","timestamp"`
	if len(requiredMetadata) > 0 {
		req += `,"metadata"`
	}

	meta := `"metadata": {"type":"object"`
	if metadataProps != "" {
		meta += `,"properties":{` + metadataProps + `}`
	}
	if len(requiredMetadata) > 0 {
		meta += `,"required":[`
		for i, r := range requiredMetadata {
			if i > 0 {
				meta += ","
			}
			meta += `"` + r + `"`
		}
		meta += `]`
	}
	meta += `}`

	return `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "` + description + `",
  "type": "object",
  "required": [` + req + `],
  "properties": {
    "actor_id": {"type":"string"},
    "actor_type": {"type":"string"},
    "resource_type": {"type":"string","minLength":1},
    "resource_id": {"type":"string","minLength":1},
    "organization_id": {"type":"string"},
    "team_id": {"type":"string"},
    "timestamp": {"type":"string"},
    ` + meta + `
  }
}`
}

// registeredEvents is the canonical registry. init() also fills the map index.
var registeredEvents = []RegisteredEvent{
	// Work ---------------------------------------------------------------
	{EventWorkSpecCreated, "work", "A product spec was created", "work-service",
		dataSchema("work.spec.created", []string{"title", "status"}, `"title":{"type":"string"},"status":{"type":"string"},"priority":{"type":"string"},"project_id":{"type":"string"},"autonomous":{"type":"boolean"},"source":{"type":"string"}`)},
	{EventWorkSpecUpdated, "work", "A product spec was updated", "work-service",
		dataSchema("work.spec.updated", nil, `"changes":{"type":"object"}`)},
	{EventWorkSpecDeleted, "work", "A product spec was deleted", "work-service",
		dataSchema("work.spec.deleted", nil, "")},
	{EventWorkSpecStatusChanged, "work", "A product spec's lifecycle status changed", "work-service",
		dataSchema("work.spec.status_changed", []string{"status"}, `"title":{"type":"string"},"status":{"type":"string"},"completion_pct":{"type":"number"},"health_status":{"type":"string"}`)},

	{EventWorkFeatureCreated, "work", "A feature was created", "work-service",
		dataSchema("work.feature.created", []string{"name", "status"}, `"name":{"type":"string"},"status":{"type":"string"},"project_id":{"type":"string"}`)},
	{EventWorkFeatureUpdated, "work", "A feature was updated", "work-service",
		dataSchema("work.feature.updated", nil, `"changes":{"type":"object"}`)},
	{EventWorkFeatureDeleted, "work", "A feature was deleted", "work-service",
		dataSchema("work.feature.deleted", nil, "")},
	{EventWorkFeatureStatusChanged, "work", "A feature's lifecycle status changed", "work-service",
		dataSchema("work.feature.status_changed", []string{"status"}, `"name":{"type":"string"},"status":{"type":"string"},"health_status":{"type":"string"}`)},
	{EventWorkFeatureEventCreated, "work", "A feature timeline event was recorded", "work-service",
		dataSchema("work.feature.event_created", []string{"feature_id", "kind"}, `"feature_id":{"type":"string"},"kind":{"type":"string"},"summary":{"type":"string"}`)},

	{EventWorkSignalIngested, "work", "A product signal was ingested", "work-service",
		dataSchema("work.signal.ingested", []string{"category", "status"}, `"provider_id":{"type":"string"},"category":{"type":"string"},"status":{"type":"string"},"summary":{"type":"string"},"metric_name":{"type":"string"}`)},
	{EventWorkSignalResolved, "work", "A signal was resolved", "work-service",
		dataSchema("work.signal.resolved", []string{"resolution"}, `"resolution":{"type":"string"}`)},
	{EventWorkSignalClassified, "work", "A signal was classified", "work-service",
		dataSchema("work.signal.classified", []string{"category"}, `"category":{"type":"string"},"confidence":{"type":"number"}`)},

	{EventWorkAcceptanceCriterionStatusChanged, "work", "Acceptance criterion status changed", "work-service",
		dataSchema("work.acceptance_criterion.status_changed", []string{"new_status"}, `"spec_id":{"type":"string"},"old_status":{"type":"string"},"new_status":{"type":"string"}`)},
	{EventWorkAcceptanceCriterionCreated, "work", "Acceptance criterion created", "work-service",
		dataSchema("work.acceptance_criterion.created", []string{"spec_id"}, `"spec_id":{"type":"string"},"text":{"type":"string"}`)},

	{EventWorkExperimentCreated, "work", "An experiment was created", "work-service",
		dataSchema("work.experiment.created", []string{"name"}, `"name":{"type":"string"},"hypothesis":{"type":"string"}`)},
	{EventWorkExperimentStarted, "work", "An experiment started", "work-service",
		dataSchema("work.experiment.started", nil, `"name":{"type":"string"}`)},
	{EventWorkExperimentCompleted, "work", "An experiment completed", "work-service",
		dataSchema("work.experiment.completed", []string{"outcome"}, `"outcome":{"type":"string"},"variant_winner":{"type":"string"}`)},

	{EventWorkItemAssigned, "work", "A work item was assigned", "work-service",
		dataSchema("work.item.assigned", []string{"assignee_id"}, `"assignee_id":{"type":"string"}`)},
	{EventWorkItemUnassigned, "work", "A work item was unassigned", "work-service",
		dataSchema("work.item.unassigned", nil, `"former_assignee_id":{"type":"string"}`)},
	{EventWorkItemStatusChanged, "work", "A work item's status changed", "work-service",
		dataSchema("work.item.status_changed", []string{"status"}, `"status":{"type":"string"}`)},
	{EventWorkItemCommented, "work", "A work item was commented on", "work-service",
		dataSchema("work.item.commented", []string{"comment_id"}, `"comment_id":{"type":"string"}`)},
	{EventWorkItemMentioned, "work", "A user was mentioned in a work item", "work-service",
		dataSchema("work.item.mentioned", []string{"mentioned_user_id"}, `"mentioned_user_id":{"type":"string"}`)},

	{EventWorkProjectCreated, "work", "A project was created", "work-service",
		dataSchema("work.project.created", []string{"name"}, `"name":{"type":"string"}`)},
	{EventWorkProjectUpdated, "work", "A project was updated", "work-service",
		dataSchema("work.project.updated", nil, `"changes":{"type":"object"}`)},
	{EventWorkProjectDeleted, "work", "A project was deleted", "work-service",
		dataSchema("work.project.deleted", nil, "")},
	{EventWorkProjectActivityLog, "work", "Project activity was logged", "work-service",
		dataSchema("work.project.activity_log", []string{"action"}, `"action":{"type":"string"}`)},

	{EventWorkContentCreated, "work", "Content was created", "work-service",
		dataSchema("work.content.created", []string{"title", "content_type"}, `"title":{"type":"string"},"content_type":{"type":"string"},"project_id":{"type":"string"}`)},
	{EventWorkContentUpdated, "work", "Content was updated", "work-service",
		dataSchema("work.content.updated", []string{"title"}, `"title":{"type":"string"},"content_type":{"type":"string"},"version":{"type":"integer"}`)},
	{EventWorkContentDeleted, "work", "Content was deleted", "work-service",
		dataSchema("work.content.deleted", nil, `"project_id":{"type":"string"}`)},

	// Git ----------------------------------------------------------------
	{EventGitRepositoryCreated, "git", "A repository was created", "git-service",
		dataSchema("git.repository.created", []string{"name"}, `"name":{"type":"string"},"default_branch":{"type":"string"},"url":{"type":"string"}`)},
	{EventGitRepositoryUpdated, "git", "A repository was updated", "git-service",
		dataSchema("git.repository.updated", nil, `"changes":{"type":"object"}`)},
	{EventGitRepositoryDeleted, "git", "A repository was deleted", "git-service",
		dataSchema("git.repository.deleted", nil, "")},

	{EventGitBranchCreated, "git", "A branch was created", "git-service",
		dataSchema("git.branch.created", []string{"branch", "repository_id"}, `"branch":{"type":"string"},"repository_id":{"type":"string"},"from_ref":{"type":"string"}`)},
	{EventGitBranchDeleted, "git", "A branch was deleted", "git-service",
		dataSchema("git.branch.deleted", []string{"branch", "repository_id"}, `"branch":{"type":"string"},"repository_id":{"type":"string"}`)},

	{EventGitPush, "git", "A push was received", "git-service",
		dataSchema("git.push.received", []string{"branch", "repository_id", "commits"}, `"branch":{"type":"string"},"repository_id":{"type":"string"},"commits":{"type":"array"},"before_sha":{"type":"string"},"after_sha":{"type":"string"}`)},

	{EventGitCommitAuthored, "git", "A commit was authored", "git-service",
		dataSchema("git.commit.authored", []string{"sha", "author"}, `"sha":{"type":"string"},"author":{"type":"string"},"message":{"type":"string"},"repository_id":{"type":"string"}`)},

	{EventGitSessionStarted, "git", "A coding session started", "git-service",
		dataSchema("git.session.started", []string{"session_id"}, `"session_id":{"type":"string"},"user_id":{"type":"string"},"branch":{"type":"string"}`)},
	{EventGitSessionCompleted, "git", "A coding session completed", "git-service",
		dataSchema("git.session.completed", []string{"session_id"}, `"session_id":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventGitSessionCancelled, "git", "A coding session was cancelled", "git-service",
		dataSchema("git.session.cancelled", []string{"session_id"}, `"session_id":{"type":"string"},"reason":{"type":"string"}`)},

	{EventGitPRCreated, "git", "A pull request was created", "git-service",
		dataSchema("git.pr.created", []string{"pr_number", "repository_id"}, `"pr_number":{"type":"integer"},"title":{"type":"string"},"repository_id":{"type":"string"},"source_branch":{"type":"string"},"target_branch":{"type":"string"}`)},
	{EventGitPRUpdated, "git", "A pull request was updated", "git-service",
		dataSchema("git.pr.updated", []string{"pr_number"}, `"pr_number":{"type":"integer"},"changes":{"type":"object"}`)},
	{EventGitPRMerged, "git", "A pull request was merged", "git-service",
		dataSchema("git.pr.merged", []string{"pr_number"}, `"pr_number":{"type":"integer"},"merge_sha":{"type":"string"}`)},
	{EventGitPRClosed, "git", "A pull request was closed", "git-service",
		dataSchema("git.pr.closed", []string{"pr_number"}, `"pr_number":{"type":"integer"}`)},
	{EventGitPRReviewSubmitted, "git", "A PR review was submitted", "git-service",
		dataSchema("git.pr.review_submitted", []string{"pr_number", "reviewer_id", "state"}, `"pr_number":{"type":"integer"},"reviewer_id":{"type":"string"},"state":{"type":"string"}`)},

	{EventGitAIReviewRequested, "git", "An AI code review was requested", "git-service",
		dataSchema("git.ai_review.requested", []string{"pr_number"}, `"pr_number":{"type":"integer"},"model":{"type":"string"}`)},
	{EventGitAIReviewCompleted, "git", "An AI code review completed", "git-service",
		dataSchema("git.ai_review.completed", []string{"pr_number"}, `"pr_number":{"type":"integer"},"findings_count":{"type":"integer"},"verdict":{"type":"string"}`)},

	{EventGitReleaseCreated, "git", "A release was created", "git-service",
		dataSchema("git.release.created", []string{"tag"}, `"tag":{"type":"string"},"title":{"type":"string"}`)},
	{EventGitDocStale, "git", "A document was flagged as stale", "git-service",
		dataSchema("git.doc.stale", []string{"path"}, `"path":{"type":"string"},"reason":{"type":"string"}`)},
	{EventGitSymbolIndexUpdated, "git", "Symbol index was refreshed", "git-service",
		dataSchema("git.symbol_index.updated", []string{"repository_id"}, `"repository_id":{"type":"string"},"commit_sha":{"type":"string"},"symbols":{"type":"array"}`)},

	// Ops ----------------------------------------------------------------
	{EventOpsDeploymentStarted, "ops", "A deployment was started", "ops-service",
		dataSchema("ops.deployment.started", nil, `"service":{"type":"string"},"service_id":{"type":"string"},"environment":{"type":"string"},"environment_id":{"type":"string"},"deployment_id":{"type":"string"},"version":{"type":"string"},"commit_sha":{"type":"string"},"status":{"type":"string"}`)},
	{EventOpsDeploymentCompleted, "ops", "A deployment completed", "ops-service",
		dataSchema("ops.deployment.completed", []string{"status"}, `"status":{"type":"string"},"environment_id":{"type":"string"},"environment":{"type":"string"},"service":{"type":"string"},"service_id":{"type":"string"},"deployment_id":{"type":"string"},"duration_ms":{"type":"integer"},"commit_sha":{"type":"string"},"version":{"type":"string"},"target_id":{"type":"string"},"target_type":{"type":"string"}`)},
	{EventOpsDeploymentPromoted, "ops", "A deployment was promoted", "ops-service",
		dataSchema("ops.deployment.promoted", []string{"service", "from_env", "to_env"}, `"service":{"type":"string"},"from_env":{"type":"string"},"to_env":{"type":"string"}`)},
	{EventOpsDeploymentRolledBack, "ops", "A deployment was rolled back", "ops-service",
		dataSchema("ops.deployment.rolled_back", nil, `"service":{"type":"string"},"service_id":{"type":"string"},"environment":{"type":"string"},"environment_id":{"type":"string"},"deployment_id":{"type":"string"},"reason":{"type":"string"},"status":{"type":"string"}`)},
	{EventOpsDeploymentCorrelated, "ops", "Deployment correlated to metric change", "ops-service",
		dataSchema("ops.deployment.correlated", []string{"deployment_id"}, `"deployment_id":{"type":"string"},"metric":{"type":"string"},"impact":{"type":"string"}`)},

	{EventOpsAlertTriggered, "ops", "An alert was triggered", "ops-service",
		dataSchema("ops.alert.triggered", []string{"severity", "service"}, `"severity":{"type":"string"},"service":{"type":"string"},"summary":{"type":"string"}`)},
	{EventOpsAlertDiagnosed, "ops", "An alert was diagnosed", "ops-service",
		dataSchema("ops.alert.diagnosed", []string{"alert_id"}, `"alert_id":{"type":"string"},"root_cause":{"type":"string"},"confidence":{"type":"number"}`)},
	{EventOpsAlertAcknowledged, "ops", "An alert was acknowledged", "ops-service",
		dataSchema("ops.alert.acknowledged", []string{"alert_id", "acknowledged_by"}, `"alert_id":{"type":"string"},"acknowledged_by":{"type":"string"}`)},
	{EventOpsAlertResolved, "ops", "An alert was resolved", "ops-service",
		dataSchema("ops.alert.resolved", []string{"alert_id"}, `"alert_id":{"type":"string"},"resolution":{"type":"string"}`)},

	{EventOpsIncidentOpened, "ops", "An incident was opened", "ops-service",
		dataSchema("ops.incident.opened", []string{"severity", "title"}, `"severity":{"type":"string"},"title":{"type":"string"}`)},
	{EventOpsIncidentResolved, "ops", "An incident was resolved", "ops-service",
		dataSchema("ops.incident.resolved", nil, `"resolution":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventOpsIncidentWorkItemLinked, "ops", "A work item was linked to an incident", "ops-service",
		dataSchema("ops.incident.work_item_linked", []string{"incident_id", "work_item_id"}, `"incident_id":{"type":"string"},"work_item_id":{"type":"string"}`)},
	{EventOpsIncidentChannelCreated, "ops", "An incident channel was created", "ops-service",
		dataSchema("ops.incident.channel_created", []string{"incident_id", "channel"}, `"incident_id":{"type":"string"},"channel":{"type":"string"}`)},

	{EventOpsProbeFailure, "ops", "A probe failed", "ops-service",
		dataSchema("ops.probe.failure", []string{"probe_id"}, `"probe_id":{"type":"string"},"error":{"type":"string"}`)},
	{EventOpsProbeRecovered, "ops", "A probe recovered", "ops-service",
		dataSchema("ops.probe.recovered", []string{"probe_id"}, `"probe_id":{"type":"string"}`)},
	{EventOpsProbeResult, "ops", "A probe produced a result", "ops-service",
		dataSchema("ops.probe.result", []string{"probe_id", "status"}, `"probe_id":{"type":"string"},"status":{"type":"string"},"latency_ms":{"type":"integer"}`)},

	{EventOpsDeliveryGatePassed, "ops", "A delivery gate passed", "ops-service",
		dataSchema("ops.delivery_gate.passed", []string{"gate", "pipeline_id"}, `"gate":{"type":"string"},"pipeline_id":{"type":"string"}`)},
	{EventOpsDeliveryGateFailed, "ops", "A delivery gate failed", "ops-service",
		dataSchema("ops.delivery_gate.failed", []string{"gate", "pipeline_id", "reason"}, `"gate":{"type":"string"},"pipeline_id":{"type":"string"},"reason":{"type":"string"}`)},

	{EventOpsPipelineStarted, "ops", "A pipeline started", "ops-service",
		dataSchema("ops.pipeline.started", []string{"pipeline_id"}, `"pipeline_id":{"type":"string"},"trigger":{"type":"string"}`)},
	{EventOpsPipelineCompleted, "ops", "A pipeline completed", "ops-service",
		dataSchema("ops.pipeline.completed", []string{"pipeline_id"}, `"pipeline_id":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventOpsPipelineFailed, "ops", "A pipeline failed", "ops-service",
		dataSchema("ops.pipeline.failed", []string{"pipeline_id", "error"}, `"pipeline_id":{"type":"string"},"error":{"type":"string"}`)},

	{EventOpsFeatureFlagCreated, "ops", "A feature flag was created", "ops-service",
		dataSchema("ops.feature_flag.created", []string{"flag"}, `"flag":{"type":"string"}`)},
	{EventOpsFeatureFlagUpdated, "ops", "A feature flag was updated", "ops-service",
		dataSchema("ops.feature_flag.updated", []string{"flag"}, `"flag":{"type":"string"},"changes":{"type":"object"}`)},
	{EventOpsFeatureFlagStale, "ops", "A stale feature flag was detected", "ops-service",
		dataSchema("ops.feature_flag.stale_detected", []string{"flag"}, `"flag":{"type":"string"},"stale_since":{"type":"string"}`)},
	{EventOpsFeatureFlagRolloutStep, "ops", "A flag rollout advanced", "ops-service",
		dataSchema("ops.feature_flag.rollout_step", []string{"flag", "percentage"}, `"flag":{"type":"string"},"percentage":{"type":"number"}`)},

	{EventOpsSLOBurnAlert, "ops", "SLO error budget burn alert", "ops-service",
		dataSchema("ops.slo.burn_alert", []string{"slo_id"}, `"slo_id":{"type":"string"},"burn_rate":{"type":"number"}`)},
	{EventOpsSecurityScanCompleted, "ops", "A security scan completed", "ops-service",
		dataSchema("ops.security_scan.completed", []string{"scan_id"}, `"scan_id":{"type":"string"},"findings":{"type":"integer"},"severity":{"type":"string"}`)},
	{EventOpsAutoRollback, "ops", "An automatic rollback was triggered", "ops-service",
		dataSchema("ops.deploy.auto_rollback", []string{"service"}, `"service":{"type":"string"},"reason":{"type":"string"}`)},
	{EventOpsAutoscalingScale, "ops", "An autoscaling action was taken", "ops-service",
		dataSchema("ops.autoscaling.scale", []string{"service", "direction"}, `"service":{"type":"string"},"direction":{"type":"string"},"replicas":{"type":"integer"}`)},
	{EventOpsConfigDriftDetected, "ops", "Config drift detected", "ops-service",
		dataSchema("ops.config_drift.detected", []string{"service"}, `"service":{"type":"string"},"keys":{"type":"array"}`)},
	{EventOpsConfigDriftReconciled, "ops", "Config drift reconciled", "ops-service",
		dataSchema("ops.config_drift.reconciled", []string{"service"}, `"service":{"type":"string"}`)},
	{EventOpsTestTriggered, "ops", "A test suite was triggered", "ops-service",
		dataSchema("ops.test.triggered", []string{"suite", "trigger"}, `"suite":{"type":"string"},"trigger":{"type":"string"}`)},
	{EventOpsMonitoringAnomaly, "ops", "A monitoring anomaly was detected", "ops-service",
		dataSchema("ops.monitoring.anomaly", []string{"metric"}, `"metric":{"type":"string"},"value":{"type":"number"},"expected":{"type":"number"}`)},

	// Canvas -------------------------------------------------------------
	{EventCanvasCreated, "canvas", "A canvas was created", "canvas-service",
		dataSchema("canvas.canvas.created", []string{"name"}, `"name":{"type":"string"},"project_id":{"type":"string"}`)},
	{EventCanvasUpdated, "canvas", "A canvas was updated", "canvas-service",
		dataSchema("canvas.canvas.updated", nil, `"changes":{"type":"object"}`)},
	{EventCanvasDeleted, "canvas", "A canvas was deleted", "canvas-service",
		dataSchema("canvas.canvas.deleted", nil, "")},

	{EventCanvasNodeCreated, "canvas", "A canvas node was created", "canvas-service",
		dataSchema("canvas.node.created", []string{"canvas_id", "node_type"}, `"canvas_id":{"type":"string"},"node_type":{"type":"string"},"name":{"type":"string"}`)},
	{EventCanvasNodeUpdated, "canvas", "A canvas node was updated", "canvas-service",
		dataSchema("canvas.node.updated", []string{"canvas_id"}, `"canvas_id":{"type":"string"},"changes":{"type":"object"}`)},
	{EventCanvasNodeDeleted, "canvas", "A canvas node was deleted", "canvas-service",
		dataSchema("canvas.node.deleted", []string{"canvas_id"}, `"canvas_id":{"type":"string"}`)},
	{EventCanvasNodeCertified, "canvas", "A canvas node was certified", "canvas-service",
		dataSchema("canvas.node.certified", []string{"canvas_id"}, `"canvas_id":{"type":"string"},"certification_level":{"type":"string"}`)},
	{EventCanvasNodeGenerated, "canvas", "A canvas node was generated by AI", "canvas-service",
		dataSchema("canvas.node.generated", []string{"canvas_id"}, `"canvas_id":{"type":"string"},"model":{"type":"string"}`)},
	{EventCanvasNodePublished, "canvas", "A canvas node was published to marketplace", "canvas-service",
		dataSchema("canvas.node.published", []string{"marketplace_id"}, `"marketplace_id":{"type":"string"},"version":{"type":"string"}`)},
	{EventCanvasNodeLinked, "canvas", "A canvas node was linked to a spec or feature", "canvas-service",
		dataSchema("canvas.node.linked", []string{"node_id"}, `"node_id":{"type":"string"},"spec_id":{"type":"string"},"feature_id":{"type":"string"},"link_type":{"type":"string"}`)},
	{EventCanvasNodeUnlinked, "canvas", "A canvas node was unlinked from a spec or feature", "canvas-service",
		dataSchema("canvas.node.unlinked", []string{"node_id"}, `"node_id":{"type":"string"},"spec_id":{"type":"string"},"feature_id":{"type":"string"}`)},

	{EventCanvasEdgeCreated, "canvas", "A canvas edge was created", "canvas-service",
		dataSchema("canvas.edge.created", []string{"source", "target"}, `"canvas_id":{"type":"string"},"source":{"type":"string"},"target":{"type":"string"}`)},
	{EventCanvasEdgeDeleted, "canvas", "A canvas edge was deleted", "canvas-service",
		dataSchema("canvas.edge.deleted", []string{"canvas_id"}, `"canvas_id":{"type":"string"}`)},

	{EventCanvasGroupCreated, "canvas", "A canvas group was created", "canvas-service",
		dataSchema("canvas.group.created", []string{"canvas_id", "name"}, `"canvas_id":{"type":"string"},"name":{"type":"string"}`)},
	{EventCanvasGroupUpdated, "canvas", "A canvas group was updated", "canvas-service",
		dataSchema("canvas.group.updated", []string{"canvas_id"}, `"canvas_id":{"type":"string"},"changes":{"type":"object"}`)},
	{EventCanvasGroupDeleted, "canvas", "A canvas group was deleted", "canvas-service",
		dataSchema("canvas.group.deleted", []string{"canvas_id"}, `"canvas_id":{"type":"string"}`)},

	{EventCanvasDebugSessionCreated, "canvas", "A debug session was created", "canvas-service",
		dataSchema("canvas.debug.session.created", nil, `"canvas_id":{"type":"string"}`)},
	{EventCanvasDebugSessionStarted, "canvas", "A debug session started", "canvas-service",
		dataSchema("canvas.debug.session.started", nil, `"canvas_id":{"type":"string"}`)},
	{EventCanvasDebugSessionPaused, "canvas", "A debug session paused", "canvas-service",
		dataSchema("canvas.debug.session.paused", nil, `"canvas_id":{"type":"string"},"reason":{"type":"string"}`)},
	{EventCanvasDebugSessionCancelled, "canvas", "A debug session was cancelled", "canvas-service",
		dataSchema("canvas.debug.session.cancelled", nil, `"canvas_id":{"type":"string"}`)},
	{EventCanvasDebugSessionFailed, "canvas", "A debug session failed", "canvas-service",
		dataSchema("canvas.debug.session.failed", []string{"error"}, `"canvas_id":{"type":"string"},"error":{"type":"string"}`)},
	{EventCanvasDebugSessionCompleted, "canvas", "A debug session completed", "canvas-service",
		dataSchema("canvas.debug.session.completed", nil, `"canvas_id":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventCanvasDebugStepCompleted, "canvas", "A debug step completed", "canvas-service",
		dataSchema("canvas.debug.step.completed", nil, `"canvas_id":{"type":"string"},"node_id":{"type":"string"}`)},

	{EventCanvasAPIGenerated, "canvas", "A canvas API was generated", "canvas-service",
		dataSchema("canvas.api.generated", nil, `"canvas_id":{"type":"string"},"api_name":{"type":"string"}`)},
	{EventCanvasAPIDeployed, "canvas", "A canvas API was deployed", "canvas-service",
		dataSchema("canvas.api.deployed", nil, `"endpoint":{"type":"string"},"api_name":{"type":"string"}`)},
	{EventCanvasAPIStopWarning, "canvas", "Canvas API approaching stop", "canvas-service",
		dataSchema("canvas.api.stop_warning", nil, `"reason":{"type":"string"}`)},
	{EventCanvasAPIStopped, "canvas", "A canvas API was stopped", "canvas-service",
		dataSchema("canvas.api.stopped", nil, `"reason":{"type":"string"}`)},
	{EventCanvasAPIDeleted, "canvas", "A canvas API was deleted", "canvas-service",
		dataSchema("canvas.api.deleted", nil, "")},

	{EventCanvasWorkflowCreated, "canvas", "A canvas workflow was created", "canvas-service",
		dataSchema("canvas.workflow.created", nil, `"name":{"type":"string"}`)},
	{EventCanvasWorkflowScheduledTrigger, "canvas", "A canvas workflow was triggered by schedule", "canvas-service",
		dataSchema("canvas.workflow.scheduled_trigger", nil, `"schedule":{"type":"string"}`)},

	{EventCanvasCodegenCompleted, "canvas", "Canvas code generation completed", "canvas-service",
		dataSchema("canvas.codegen.completed", nil, `"canvas_id":{"type":"string"},"language":{"type":"string"},"files":{"type":"integer"}`)},

	{EventCanvasRuntimeDeployed, "canvas", "Canvas runtime deployment", "canvas-service",
		dataSchema("canvas.runtime.deployed", nil, `"canvas_id":{"type":"string"},"deployment_id":{"type":"string"}`)},
	{EventCanvasRuntimeUndeployed, "canvas", "Canvas runtime undeployed", "canvas-service",
		dataSchema("canvas.runtime.undeployed", nil, `"canvas_id":{"type":"string"}`)},

	{EventCanvasSyncEnabled, "canvas", "Canvas sync was enabled", "canvas-service",
		dataSchema("canvas.sync.enabled", nil, `"canvas_id":{"type":"string"},"repo":{"type":"string"}`)},
	{EventCanvasSyncCompleted, "canvas", "Canvas sync completed", "canvas-service",
		dataSchema("canvas.sync.completed", nil, `"canvas_id":{"type":"string"},"direction":{"type":"string"}`)},
	{EventCanvasSyncConflictDetected, "canvas", "Canvas sync conflict detected", "canvas-service",
		dataSchema("canvas.sync.conflict_detected", nil, `"canvas_id":{"type":"string"},"path":{"type":"string"}`)},
	{EventCanvasSyncConflictResolved, "canvas", "Canvas sync conflict resolved", "canvas-service",
		dataSchema("canvas.sync.conflict_resolved", nil, `"canvas_id":{"type":"string"},"resolution":{"type":"string"}`)},
	{EventCanvasSyncStatusChanged, "canvas", "Canvas sync status changed", "canvas-service",
		dataSchema("canvas.sync.status_changed", nil, `"canvas_id":{"type":"string"},"status":{"type":"string"}`)},
	{EventCanvasSyncCanvasToCodeReq, "canvas", "Canvas-to-code sync requested", "canvas-service",
		dataSchema("canvas.sync.canvas_to_code.requested", nil, `"canvas_id":{"type":"string"}`)},
	{EventCanvasSyncCodeToCanvasReq, "canvas", "Code-to-canvas sync requested", "canvas-service",
		dataSchema("canvas.sync.code_to_canvas.requested", nil, `"canvas_id":{"type":"string"}`)},

	{EventCanvasMarketplacePublisherConnected, "canvas", "Marketplace publisher connected", "canvas-service",
		dataSchema("canvas.marketplace.publisher.connected", nil, `"publisher_id":{"type":"string"}`)},
	{EventCanvasMarketplacePurchaseCreated, "canvas", "Marketplace purchase created", "canvas-service",
		dataSchema("canvas.marketplace.purchase.created", nil, `"purchase_id":{"type":"string"}`)},
	{EventCanvasMarketplacePurchaseCompleted, "canvas", "Marketplace purchase completed", "canvas-service",
		dataSchema("canvas.marketplace.purchase.completed", nil, `"purchase_id":{"type":"string"}`)},
	{EventCanvasMarketplacePayoutRequested, "canvas", "Marketplace payout requested", "canvas-service",
		dataSchema("canvas.marketplace.payout.requested", nil, `"payout_id":{"type":"string"},"amount":{"type":"number"}`)},
	{EventCanvasMarketplacePayoutCompleted, "canvas", "Marketplace payout completed", "canvas-service",
		dataSchema("canvas.marketplace.payout.completed", nil, `"payout_id":{"type":"string"}`)},
	{EventCanvasMarketplacePaymentSucceeded, "canvas", "Marketplace payment succeeded", "canvas-service",
		dataSchema("canvas.marketplace.payment.succeeded", nil, `"intent_id":{"type":"string"}`)},
	{EventCanvasMarketplaceAccountUpdated, "canvas", "Marketplace account updated", "canvas-service",
		dataSchema("canvas.marketplace.account.updated", nil, "")},
	{EventCanvasMarketplaceWebhookUnhandled, "canvas", "Unhandled marketplace webhook", "canvas-service",
		dataSchema("canvas.marketplace.webhook.unhandled", nil, `"event":{"type":"string"}`)},
	{EventCanvasMarketplacePublished, "canvas", "Canvas item published to marketplace", "canvas-service",
		dataSchema("canvas.marketplace.published", nil, `"item_id":{"type":"string"}`)},

	{EventCanvasSecretCreated, "canvas", "A canvas secret was created", "canvas-service",
		dataSchema("canvas.secret.created", nil, `"name":{"type":"string"}`)},
	{EventCanvasSecretUpdated, "canvas", "A canvas secret was updated", "canvas-service",
		dataSchema("canvas.secret.updated", nil, `"name":{"type":"string"}`)},
	{EventCanvasSecretDeleted, "canvas", "A canvas secret was deleted", "canvas-service",
		dataSchema("canvas.secret.deleted", nil, `"name":{"type":"string"}`)},

	{EventCanvasCompositeNodeCreated, "canvas", "A composite node was created", "canvas-service",
		dataSchema("canvas.composite_node.created", nil, `"canvas_id":{"type":"string"}`)},
	{EventCanvasNodeDependencyAdded, "canvas", "A node dependency was added", "canvas-service",
		dataSchema("canvas.node.dependency.added", nil, `"dependency_id":{"type":"string"}`)},
	{EventCanvasEjectCompleted, "canvas", "Canvas eject completed", "canvas-service",
		dataSchema("canvas.eject.completed", nil, `"project":{"type":"string"}`)},

	{EventCanvasEdgeCommentCreated, "canvas", "An edge comment was created", "canvas-service",
		dataSchema("canvas.edge_comment.created", []string{"canvas_id", "edge_id"}, `"canvas_id":{"type":"string"},"edge_id":{"type":"string"},"comment_id":{"type":"string"},"author_id":{"type":"string"}`)},
	{EventCanvasEdgeCommentUpdated, "canvas", "An edge comment was updated", "canvas-service",
		dataSchema("canvas.edge_comment.updated", []string{"comment_id"}, `"canvas_id":{"type":"string"},"edge_id":{"type":"string"},"comment_id":{"type":"string"}`)},
	{EventCanvasEdgeCommentDeleted, "canvas", "An edge comment was deleted", "canvas-service",
		dataSchema("canvas.edge_comment.deleted", []string{"comment_id"}, `"canvas_id":{"type":"string"},"edge_id":{"type":"string"},"comment_id":{"type":"string"}`)},
	{EventCanvasEdgeCommentResolved, "canvas", "An edge comment was resolved", "canvas-service",
		dataSchema("canvas.edge_comment.resolved", []string{"comment_id"}, `"canvas_id":{"type":"string"},"edge_id":{"type":"string"},"comment_id":{"type":"string"},"resolved":{"type":"boolean"}`)},

	// Comments (cross-cutting "Comments Everywhere" surface) -------------
	{EventCommentCreated, "comments", "A polymorphic comment was created", "multi",
		dataSchema("comments.events.created", nil, `"id":{"type":"string"},"resourceType":{"type":"string"},"resourceId":{"type":"string"},"authorId":{"type":"string"},"body":{"type":"string"},"threadId":{"type":"string"},"metadata":{"type":"object"}`)},
	{EventCommentUpdated, "comments", "A polymorphic comment was updated", "multi",
		dataSchema("comments.events.updated", nil, `"id":{"type":"string"},"resourceType":{"type":"string"},"resourceId":{"type":"string"},"authorId":{"type":"string"},"body":{"type":"string"}`)},
	{EventCommentDeleted, "comments", "A polymorphic comment was deleted", "multi",
		dataSchema("comments.events.deleted", nil, `"id":{"type":"string"},"resourceType":{"type":"string"},"resourceId":{"type":"string"}`)},

	// Chat (conversation-service per-message + per-channel events) -------
	{EventChatMessageCreated, "chat", "A chat message was created", "conversation-service",
		dataSchema("chat.message.created", []string{"channel_id"}, `"id":{"type":"string"},"channel_id":{"type":"string"},"author_id":{"type":"string"},"actor_id":{"type":"string"},"content":{"type":"string"},"message_type":{"type":"string"},"context_type":{"type":"string"},"organization_id":{"type":"string"},"system_event_data":{"type":"object"}`)},
	{EventChatMessageUpdated, "chat", "A chat message was updated", "conversation-service",
		dataSchema("chat.message.updated", []string{"channel_id"}, `"id":{"type":"string"},"channel_id":{"type":"string"},"content":{"type":"string"}`)},
	{EventChatMessageDeleted, "chat", "A chat message was deleted", "conversation-service",
		dataSchema("chat.message.deleted", []string{"channel_id"}, `"id":{"type":"string"},"channel_id":{"type":"string"}`)},
	{EventChatChannelCreated, "chat", "A chat channel was created", "conversation-service",
		dataSchema("chat.channel.created", nil, `"id":{"type":"string"},"name":{"type":"string"},"context_type":{"type":"string"},"organization_id":{"type":"string"}`)},
	{EventChatChannelUpdated, "chat", "A chat channel was updated", "conversation-service",
		dataSchema("chat.channel.updated", nil, `"id":{"type":"string"}`)},
	{EventChatChannelDeleted, "chat", "A chat channel was deleted", "conversation-service",
		dataSchema("chat.channel.deleted", nil, `"id":{"type":"string"}`)},
	{EventChatReactionAdded, "chat", "A reaction was added", "conversation-service",
		dataSchema("chat.reaction.added", nil, `"message_id":{"type":"string"},"channel_id":{"type":"string"},"emoji":{"type":"string"},"user_id":{"type":"string"}`)},
	{EventChatReactionRemoved, "chat", "A reaction was removed", "conversation-service",
		dataSchema("chat.reaction.removed", nil, `"message_id":{"type":"string"},"channel_id":{"type":"string"},"emoji":{"type":"string"},"user_id":{"type":"string"}`)},
	{EventChatTypingStarted, "chat", "Typing started", "conversation-service",
		dataSchema("chat.typing.started", nil, `"channel_id":{"type":"string"},"user_id":{"type":"string"}`)},
	{EventChatTypingStopped, "chat", "Typing stopped", "conversation-service",
		dataSchema("chat.typing.stopped", nil, `"channel_id":{"type":"string"},"user_id":{"type":"string"}`)},
	{EventChatPresenceUpdated, "chat", "Chat presence updated", "conversation-service",
		dataSchema("chat.presence.updated", nil, `"user_id":{"type":"string"},"status":{"type":"string"}`)},
	{EventChatVoiceState, "chat", "Voice state updated", "conversation-service",
		dataSchema("chat.voice.state.updated", nil, `"channel_id":{"type":"string"},"user_id":{"type":"string"}`)},
	{EventChatEveEvent, "chat", "Eve agent loop event", "conversation-service",
		dataSchema("chat.eve.event", nil, `"channel_id":{"type":"string"}`)},

	// Presence (cross-cutting co-presence surface) ----------------------
	{EventPresenceUpdated, "presence", "A resource presence snapshot was updated", "bff-service",
		dataSchema("presence.events.updated", []string{"resourceType", "resourceId"}, `"resourceType":{"type":"string"},"resourceId":{"type":"string"},"status":{"type":"string"},"userId":{"type":"string"},"activeUsers":{"type":"array"},"cursorPosition":{"type":"object"},"lastUpdated":{"type":"string"}`)},

	// Data ---------------------------------------------------------------
	{EventDataQueryExecuted, "data", "A data query was executed", "data-service",
		dataSchema("data.query.executed", []string{"query"}, `"query":{"type":"string"},"row_count":{"type":"integer"},"duration_ms":{"type":"integer"}`)},
	{EventDataQueryTranslated, "data", "An NL query was translated to SQL", "data-service",
		dataSchema("data.query.translated", nil, `"nl_query":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"data_source_id":{"type":"string"},"sql":{"type":"string"}`)},
	{EventDataQueryFailed, "data", "A data query failed", "data-service",
		dataSchema("data.query.failed", []string{"error"}, `"error":{"type":"string"},"query":{"type":"string"}`)},

	{EventDataSourceCreated, "data", "A data source was created", "data-service",
		dataSchema("data.data_source.created", []string{"name", "type"}, `"name":{"type":"string"},"type":{"type":"string"}`)},
	{EventDataSourceUpdated, "data", "A data source was updated", "data-service",
		dataSchema("data.data_source.updated", nil, `"changes":{"type":"object"}`)},
	{EventDataSourceDeleted, "data", "A data source was deleted", "data-service",
		dataSchema("data.data_source.deleted", nil, "")},
	{EventDataSourceConnected, "data", "A data source connection succeeded", "data-service",
		dataSchema("data.data_source.connected", nil, `"schema_count":{"type":"integer"}`)},

	{EventDataDashboardCreated, "data", "A dashboard was created", "data-service",
		dataSchema("data.dashboard.created", []string{"name"}, `"name":{"type":"string"}`)},
	{EventDataDashboardUpdated, "data", "A dashboard was updated", "data-service",
		dataSchema("data.dashboard.updated", nil, `"changes":{"type":"object"}`)},
	{EventDataDashboardDeleted, "data", "A dashboard was deleted", "data-service",
		dataSchema("data.dashboard.deleted", nil, "")},
	{EventDataDashboardShared, "data", "A dashboard was shared", "data-service",
		dataSchema("data.dashboard.shared", nil, `"recipient":{"type":"string"},"recipients":{"type":"array"},"permission":{"type":"string"},"public":{"type":"boolean"},"scope":{"type":"string"}`)},

	{EventDataSemanticFieldCreated, "data", "A semantic field was created", "data-service",
		dataSchema("data.semantic_field.created", []string{"name"}, `"name":{"type":"string"},"type":{"type":"string"}`)},
	{EventDataSemanticFieldUpdated, "data", "A semantic field was updated", "data-service",
		dataSchema("data.semantic_field.updated", nil, `"changes":{"type":"object"}`)},

	{EventDataDashboardAlertTriggered, "data", "A dashboard alert threshold was breached", "data-service",
		dataSchema("data.dashboard.alert_triggered", []string{"alert_id"}, `"alert_id":{"type":"string"},"dashboard_id":{"type":"string"},"panel_id":{"type":"string"},"value":{"type":"number"},"threshold":{"type":"number"},"notify_channel":{"type":"string"}`)},
	{EventDataDashboardEmbedded, "data", "A dashboard was embedded in a canvas node", "data-service",
		dataSchema("data.dashboard.embedded", []string{"dashboard_id", "canvas_id"}, `"dashboard_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"}`)},
	{EventDataSourceSampled, "data", "Row samples were captured from a data source", "data-service",
		dataSchema("data.data_source.sampled", []string{"data_source_id"}, `"data_source_id":{"type":"string"},"tables":{"type":"array"},"sample_rows":{"type":"integer"}`)},

	// Runtime ------------------------------------------------------------
	{EventRuntimeExecutionStarted, "runtime", "A runtime execution started", "runtime-service",
		dataSchema("runtime.execution.started", []string{"execution_id"}, `"execution_id":{"type":"string"},"canvas_id":{"type":"string"}`)},
	{EventRuntimeExecutionCompleted, "runtime", "A runtime execution completed", "runtime-service",
		dataSchema("runtime.execution.completed", []string{"execution_id"}, `"execution_id":{"type":"string"},"duration_ms":{"type":"integer"},"status":{"type":"string"}`)},
	{EventRuntimeExecutionFailed, "runtime", "A runtime execution failed", "runtime-service",
		dataSchema("runtime.execution.failed", []string{"execution_id", "error"}, `"execution_id":{"type":"string"},"error":{"type":"string"}`)},
	{EventRuntimeTestStarted, "runtime", "A runtime test run started", "runtime-service",
		dataSchema("runtime.test.started", []string{"suite"}, `"suite":{"type":"string"}`)},
	{EventRuntimeTestCompleted, "runtime", "A runtime test run completed", "runtime-service",
		dataSchema("runtime.test.completed", []string{"suite", "passed", "failed"}, `"suite":{"type":"string"},"passed":{"type":"integer"},"failed":{"type":"integer"}`)},
	{EventRuntimeTestFailed, "runtime", "A runtime test run failed", "runtime-service",
		dataSchema("runtime.test.failed", []string{"suite", "error"}, `"suite":{"type":"string"},"error":{"type":"string"}`)},
	{EventRuntimeExecutionRequested, "runtime", "A runtime execution was requested (by a saga or orchestrator)", "ops-service",
		dataSchema("runtime.execution.requested", []string{"execution_id"}, `"execution_id":{"type":"string"},"session_id":{"type":"string"},"saga_id":{"type":"string"},"reason":{"type":"string"}`)},

	// Saga lifecycle events (code_test_deploy).
	{EventSagaCodeTestDeployStarted, "saga", "A code_test_deploy saga started", "ops-service",
		dataSchema("saga.code_test_deploy.started", []string{"saga_id", "session_id"}, `"saga_id":{"type":"string"},"session_id":{"type":"string"},"spec_id":{"type":"string"}`)},
	{EventSagaCodeTestDeployTestsRequested, "saga", "The saga requested a runtime test run", "ops-service",
		dataSchema("saga.code_test_deploy.tests_requested", []string{"saga_id"}, `"saga_id":{"type":"string"},"execution_id":{"type":"string"}`)},
	{EventSagaCodeTestDeployTestsCompleted, "saga", "The saga observed test completion", "ops-service",
		dataSchema("saga.code_test_deploy.tests_completed", []string{"saga_id", "passed"}, `"saga_id":{"type":"string"},"passed":{"type":"boolean"},"execution_id":{"type":"string"}`)},
	{EventSagaCodeTestDeployGateEvaluated, "saga", "The saga evaluated the delivery quality gate", "ops-service",
		dataSchema("saga.code_test_deploy.gate_evaluated", []string{"saga_id", "passed"}, `"saga_id":{"type":"string"},"passed":{"type":"boolean"},"reason":{"type":"string"}`)},
	{EventSagaCodeTestDeployDeploymentRequested, "saga", "The saga requested a deployment", "ops-service",
		dataSchema("saga.code_test_deploy.deployment_requested", []string{"saga_id", "deployment_id"}, `"saga_id":{"type":"string"},"deployment_id":{"type":"string"},"environment_id":{"type":"string"}`)},
	{EventSagaCodeTestDeployCompleted, "saga", "The saga completed successfully", "ops-service",
		dataSchema("saga.code_test_deploy.completed", []string{"saga_id"}, `"saga_id":{"type":"string"},"deployment_id":{"type":"string"}`)},
	{EventSagaCodeTestDeployFailed, "saga", "The saga failed and compensation (if any) ran", "ops-service",
		dataSchema("saga.code_test_deploy.failed", []string{"saga_id", "reason"}, `"saga_id":{"type":"string"},"reason":{"type":"string"},"failed_step":{"type":"string"}`)},

	{EventRuntimeCheckpointCreated, "runtime", "A VM checkpoint was created by the scheduler", "runtime-service",
		dataSchema("runtime.checkpoint.created", []string{"vm_id", "snapshot_id"}, `"vm_id":{"type":"string"},"snapshot_id":{"type":"string"},"size_bytes":{"type":"integer"},"interval_seconds":{"type":"integer"}`)},
	{EventRuntimeCheckpointRestored, "runtime", "A VM was restored from a checkpoint", "runtime-service",
		dataSchema("runtime.checkpoint.restored", []string{"vm_id", "snapshot_id"}, `"vm_id":{"type":"string"},"snapshot_id":{"type":"string"},"restore_time_ms":{"type":"integer"}`)},
	{EventOpsTraceCapturedForRegression, "ops", "A production trace was captured for regression test generation", "ops-service",
		dataSchema("ops.trace.captured_for_regression", []string{"trace_id", "service_id"}, `"trace_id":{"type":"string"},"service_id":{"type":"string"},"organization_id":{"type":"string"}`)},
	{EventRuntimeRegressionTestCreated, "runtime", "A regression test was generated from a production trace", "runtime-service",
		dataSchema("runtime.regression_test.created", []string{"trace_id", "test_run_id"}, `"trace_id":{"type":"string"},"test_run_id":{"type":"string"},"language":{"type":"string"},"framework":{"type":"string"}`)},

	// Foundry ------------------------------------------------------------
	{EventFoundryAgentStarted, "foundry", "An agent loop started", "foundry-service",
		dataSchema("foundry.agent.started", []string{"agent", "session_id"}, `"agent":{"type":"string"},"session_id":{"type":"string"},"prompt":{"type":"string"}`)},
	{EventFoundryAgentCompleted, "foundry", "An agent loop completed", "foundry-service",
		dataSchema("foundry.agent.completed", []string{"agent", "session_id"}, `"agent":{"type":"string"},"session_id":{"type":"string"},"tokens_used":{"type":"integer"}`)},
	{EventFoundryAgentFailed, "foundry", "An agent loop failed", "foundry-service",
		dataSchema("foundry.agent.failed", []string{"agent", "error"}, `"agent":{"type":"string"},"error":{"type":"string"}`)},

	{EventFoundryApprovalRequested, "foundry", "An approval was requested", "foundry-service",
		dataSchema("foundry.approval.requested", []string{"action"}, `"action":{"type":"string"},"requester":{"type":"string"}`)},
	{EventFoundryApprovalGranted, "foundry", "An approval was granted", "foundry-service",
		dataSchema("foundry.approval.granted", []string{"approval_id", "approver"}, `"approval_id":{"type":"string"},"approver":{"type":"string"}`)},
	{EventFoundryApprovalRejected, "foundry", "An approval was rejected", "foundry-service",
		dataSchema("foundry.approval.rejected", []string{"approval_id", "approver"}, `"approval_id":{"type":"string"},"approver":{"type":"string"},"reason":{"type":"string"}`)},

	{EventFoundryProposalCreated, "foundry", "A proposal was created", "foundry-service",
		dataSchema("foundry.proposal.created", []string{"proposal_type"}, `"proposal_type":{"type":"string"},"title":{"type":"string"}`)},
	{EventFoundryProposalAccepted, "foundry", "A proposal was accepted", "foundry-service",
		dataSchema("foundry.proposal.accepted", nil, `"reviewer":{"type":"string"}`)},
	{EventFoundryProposalRejected, "foundry", "A proposal was rejected", "foundry-service",
		dataSchema("foundry.proposal.rejected", nil, `"reviewer":{"type":"string"},"reason":{"type":"string"}`)},

	{EventFoundryDiagnosisStarted, "foundry", "A diagnosis started", "foundry-service",
		dataSchema("foundry.diagnosis.started", []string{"subject"}, `"subject":{"type":"string"}`)},
	{EventFoundryDiagnosisCompleted, "foundry", "A diagnosis completed", "foundry-service",
		dataSchema("foundry.diagnosis.completed", []string{"subject"}, `"subject":{"type":"string"},"verdict":{"type":"string"}`)},

	{EventFoundryGenesisSecurityScanCompleted, "foundry", "Genesis security scan for a node proposal completed", "foundry-service",
		dataSchema("foundry.genesis.security_scan_completed", []string{"proposal_id", "verdict"},
			`"proposal_id":{"type":"string"},"verdict":{"type":"string"},"critical_count":{"type":"integer"},"high_count":{"type":"integer"},"medium_count":{"type":"integer"},"low_count":{"type":"integer"}`)},
	{EventFoundryGenesisSecurityBlocked, "foundry", "A node proposal was blocked from publishing by security scan", "foundry-service",
		dataSchema("foundry.genesis.security_blocked", []string{"proposal_id"},
			`"proposal_id":{"type":"string"},"critical_count":{"type":"integer"},"high_count":{"type":"integer"},"summary":{"type":"string"}`)},

	{EventFoundryReactiveThrottled, "foundry", "A reactive rule trigger was throttled", "foundry-service",
		dataSchema("foundry.reactive.throttled", []string{"rule_id", "reason"},
			`"rule_id":{"type":"string"},"rule_name":{"type":"string"},"event_type":{"type":"string"},"reason":{"type":"string"},"window_triggers":{"type":"integer"}`)},

	{EventFoundryEveToolCallsParallelStart, "foundry", "Eve launched a batch of tool calls in parallel", "foundry-service",
		dataSchema("foundry.eve.tool_calls_parallel_start", []string{"batch_size"},
			`"batch_size":{"type":"integer"},"iteration":{"type":"integer"},"tool_names":{"type":"array","items":{"type":"string"}}`)},
	{EventFoundryEveToolCallsParallelEnd, "foundry", "Eve completed a batch of parallel tool calls", "foundry-service",
		dataSchema("foundry.eve.tool_calls_parallel_end", []string{"batch_size"},
			`"batch_size":{"type":"integer"},"iteration":{"type":"integer"},"duration_ms":{"type":"integer"},"success_count":{"type":"integer"},"error_count":{"type":"integer"}`)},

	{EventFoundryMemoryStalenessRescored, "foundry", "Background worker rescored freshness of agent memory entries", "foundry-service",
		dataSchema("foundry.memory.staleness_rescored", []string{"total"},
			`"total":{"type":"integer"},"stale":{"type":"integer"},"fresh":{"type":"integer"}`)},

	// Tier-3 autonomous-loop signalling ------------------------------------
	{EventFoundryDecomposeSpecRequested, "foundry", "A spec decomposition run was requested", "work-service",
		dataSchema("foundry.decompose_spec.requested", []string{"spec_id"},
			`"spec_id":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},"organization_id":{"type":"string"},"attempt":{"type":"integer"},"source_kind":{"type":"string"}`)},
	{EventFoundryCodeGenStarted, "foundry", "Code generation for a canvas node started", "foundry-service",
		dataSchema("foundry.code_gen.started", []string{"node_id"},
			`"node_id":{"type":"string"},"canvas_id":{"type":"string"},"spec_id":{"type":"string"},"organization_id":{"type":"string"}`)},
	{EventFoundryCodeGenCompleted, "foundry", "Code generation for a canvas node completed", "foundry-service",
		dataSchema("foundry.code_gen.completed", []string{"node_id"},
			`"node_id":{"type":"string"},"canvas_id":{"type":"string"},"spec_id":{"type":"string"},"code":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventFoundryCodeGenFailed, "foundry", "Code generation for a canvas node failed", "foundry-service",
		dataSchema("foundry.code_gen.failed", []string{"node_id", "error"},
			`"node_id":{"type":"string"},"canvas_id":{"type":"string"},"spec_id":{"type":"string"},"error":{"type":"string"}`)},

	// Saga lifecycle events (Tier-2 long-running orchestrations) ------------
	{EventSagaSelfHealingStarted, "saga", "Self-healing saga started from an ops.alert.fired event", "foundry-service",
		dataSchema("saga.self_healing.started", []string{"saga_id", "alert_id"},
			`"saga_id":{"type":"string"},"alert_id":{"type":"string"},"incident_id":{"type":"string"},"severity":{"type":"string"}`)},
	{EventSagaSelfHealingDiagnosed, "saga", "Self-healing saga produced an AI diagnosis for the triggering alert", "foundry-service",
		dataSchema("saga.self_healing.diagnosed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"root_cause":{"type":"string"},"confidence":{"type":"number"},"severity":{"type":"string"}`)},
	{EventSagaSelfHealingApprovalRequired, "saga", "Self-healing saga is awaiting human approval before proceeding", "foundry-service",
		dataSchema("saga.self_healing.approval_required", []string{"saga_id", "approval_id"},
			`"saga_id":{"type":"string"},"approval_id":{"type":"string"},"reason":{"type":"string"},"confidence":{"type":"number"}`)},
	{EventSagaSelfHealingSpecCreated, "saga", "Self-healing saga created a remediation spec in work-service", "foundry-service",
		dataSchema("saga.self_healing.spec_created", []string{"saga_id", "spec_id"},
			`"saga_id":{"type":"string"},"spec_id":{"type":"string"},"title":{"type":"string"}`)},
	{EventSagaSelfHealingDeployed, "saga", "Self-healing saga deployed the remediation", "foundry-service",
		dataSchema("saga.self_healing.deployed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"deployment_id":{"type":"string"}`)},
	{EventSagaSelfHealingVerified, "saga", "Self-healing saga verified that the underlying metric recovered", "foundry-service",
		dataSchema("saga.self_healing.verified", []string{"saga_id"},
			`"saga_id":{"type":"string"},"verified_at":{"type":"string"}`)},
	{EventSagaSelfHealingCompleted, "saga", "Self-healing saga completed successfully", "foundry-service",
		dataSchema("saga.self_healing.completed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventSagaSelfHealingFailed, "saga", "Self-healing saga failed", "foundry-service",
		dataSchema("saga.self_healing.failed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"error":{"type":"string"},"state":{"type":"string"}`)},
	{EventSagaSelfHealingRolledBack, "saga", "Self-healing saga rolled back a failed remediation", "foundry-service",
		dataSchema("saga.self_healing.rolled_back", []string{"saga_id"},
			`"saga_id":{"type":"string"},"deployment_id":{"type":"string"},"reason":{"type":"string"}`)},

	{EventSagaSpecDrivenStarted, "saga", "Spec-driven saga began orchestration", "foundry-service",
		dataSchema("saga.spec_driven.started", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"title":{"type":"string"}`)},
	{EventSagaSpecDrivenDecomposed, "saga", "Spec decomposed into canvas node graph", "foundry-service",
		dataSchema("saga.spec_driven.decomposed", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"node_count":{"type":"integer"}`)},
	{EventSagaSpecDrivenCanvasCreated, "saga", "Canvas created for spec-driven saga", "foundry-service",
		dataSchema("saga.spec_driven.canvas_created", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"canvas_id":{"type":"string"}`)},
	{EventSagaSpecDrivenSessionCreated, "saga", "Git session created for spec-driven saga", "foundry-service",
		dataSchema("saga.spec_driven.session_created", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"session_id":{"type":"string"},"branch":{"type":"string"}`)},
	{EventSagaSpecDrivenCodeWritten, "saga", "Coder agent finished a pass on the spec-driven saga", "foundry-service",
		dataSchema("saga.spec_driven.code_written", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"session_id":{"type":"string"},"attempt":{"type":"integer"}`)},
	{EventSagaSpecDrivenTestingStarted, "saga", "Spec-driven saga kicked off a runtime test run", "foundry-service",
		dataSchema("saga.spec_driven.testing_started", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"session_id":{"type":"string"},"attempt":{"type":"integer"}`)},
	{EventSagaSpecDrivenRetryTriggered, "saga", "Spec-driven saga retrying after test failure", "foundry-service",
		dataSchema("saga.spec_driven.retry_triggered", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"attempt":{"type":"integer"},"max_retries":{"type":"integer"},"reason":{"type":"string"}`)},
	{EventSagaSpecDrivenCompleted, "saga", "Spec-driven saga finished (used for cross-saga chaining)", "foundry-service",
		dataSchema("saga.spec_driven.completed", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"state":{"type":"string"}`)},
	{EventSagaSpecDrivenFailed, "saga", "Spec-driven saga failed", "foundry-service",
		dataSchema("saga.spec_driven.failed", []string{"spec_id"},
			`"spec_id":{"type":"string"},"saga_id":{"type":"string"},"reason":{"type":"string"}`)},

	// Import-analyze-canvas saga (Flow 2C) ---------------------------------
	{EventSagaImportAnalyzeCanvasStarted, "saga", "Import-analyze-canvas saga started after git analysis completed", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.started", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"repository_url":{"type":"string"},"symbol_count":{"type":"integer"},"state":{"type":"string"}`)},
	{EventSagaImportAnalyzeCanvasNodes, "saga", "Import-analyze-canvas saga bulk-created nodes on the canvas", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.nodes_created", []string{"saga_id", "canvas_id"},
			`"saga_id":{"type":"string"},"canvas_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"node_count":{"type":"integer"},"state":{"type":"string"}`)},
	{EventSagaImportAnalyzeCanvasEdges, "saga", "Import-analyze-canvas saga inferred edges between imported nodes", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.edges_inferred", []string{"saga_id", "canvas_id"},
			`"saga_id":{"type":"string"},"canvas_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"edge_count":{"type":"integer"},"state":{"type":"string"}`)},
	{EventSagaImportAnalyzeCanvasLayout, "saga", "Import-analyze-canvas saga applied the auto-layout", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.layout_applied", []string{"saga_id", "canvas_id"},
			`"saga_id":{"type":"string"},"canvas_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"state":{"type":"string"}`)},
	{EventSagaImportAnalyzeCanvasCompleted, "saga", "Import-analyze-canvas saga completed successfully", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.completed", []string{"saga_id", "canvas_id"},
			`"saga_id":{"type":"string"},"canvas_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"node_count":{"type":"integer"},"edge_count":{"type":"integer"},"symbol_count":{"type":"integer"},"state":{"type":"string"}`)},
	{EventSagaImportAnalyzeCanvasFailed, "saga", "Import-analyze-canvas saga failed during one of its stages", "canvas-service",
		dataSchema("canvas.saga.import_analyze_canvas.failed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"import_id":{"type":"string"},"error":{"type":"string"},"state":{"type":"string"}`)},

	// NL-query saga (Flow 2E) ----------------------------------------------
	{EventSagaNLQueryStarted, "saga", "Natural-language query saga started", "foundry-service",
		dataSchema("saga.nl_query.started", []string{"saga_id", "question"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},
	{EventSagaNLQueryTranslated, "saga", "NL-query saga translated the question into SQL via data-service", "foundry-service",
		dataSchema("saga.nl_query.translated", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},
	{EventSagaNLQueryExecuted, "saga", "NL-query saga executed the translated SQL", "foundry-service",
		dataSchema("saga.nl_query.executed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},
	{EventSagaNLQueryRendered, "saga", "NL-query saga rendered the result as a canvas dashboard node", "foundry-service",
		dataSchema("saga.nl_query.rendered", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},
	{EventSagaNLQueryCompleted, "saga", "NL-query saga completed and posted the Eve reply", "foundry-service",
		dataSchema("saga.nl_query.completed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},
	{EventSagaNLQueryFailed, "saga", "NL-query saga failed during translation, execution, or rendering", "foundry-service",
		dataSchema("saga.nl_query.failed", []string{"saga_id"},
			`"saga_id":{"type":"string"},"organization_id":{"type":"string"},"user_id":{"type":"string"},"state":{"type":"string"},"question":{"type":"string"},"query_id":{"type":"string"},"canvas_id":{"type":"string"},"node_id":{"type":"string"},"chart_type":{"type":"string"},"row_count":{"type":"integer"},"error":{"type":"string"}`)},

	// Identity -----------------------------------------------------------
	{EventIdentityUserRegistered, "identity", "A user registered", "identity-service",
		dataSchema("identity.user.registered", []string{"email"}, `"email":{"type":"string"},"full_name":{"type":"string"}`)},
	{EventIdentityUserUpdated, "identity", "A user profile was updated", "identity-service",
		dataSchema("identity.user.updated", nil, `"changes":{"type":"object"}`)},
	{EventIdentityUserDeleted, "identity", "A user was deleted", "identity-service",
		dataSchema("identity.user.deleted", nil, "")},
	{EventIdentityUserLoggedIn, "identity", "A user logged in", "identity-service",
		dataSchema("identity.user.logged_in", nil, `"ip":{"type":"string"},"user_agent":{"type":"string"}`)},
	{EventIdentityUserFirstLogin, "identity", "A user logged in for the first time", "identity-service",
		dataSchema("identity.user.first_login", []string{"user_id"}, `"user_id":{"type":"string"},"organization_id":{"type":"string"},"email":{"type":"string"},"full_name":{"type":"string"}`)},
	{EventIdentityUserPresenceChanged, "identity", "A user's presence state changed (online/away/offline)", "identity-service",
		dataSchema("identity.user.presence_changed", []string{"user_id", "state"}, `"user_id":{"type":"string"},"state":{"type":"string","enum":["online","away","offline"]},"at":{"type":"string"}`)},

	{EventIdentityOrganizationCreated, "identity", "An organization was created", "identity-service",
		dataSchema("identity.organization.created", []string{"name"}, `"name":{"type":"string"},"slug":{"type":"string"}`)},
	{EventIdentityOrganizationUpdated, "identity", "An organization was updated", "identity-service",
		dataSchema("identity.organization.updated", nil, `"changes":{"type":"object"}`)},
	{EventIdentityOrganizationDeleted, "identity", "An organization was deleted", "identity-service",
		dataSchema("identity.organization.deleted", nil, "")},

	{EventIdentityTeamCreated, "identity", "A team was created", "identity-service",
		dataSchema("identity.team.created", []string{"name"}, `"name":{"type":"string"}`)},
	{EventIdentityTeamUpdated, "identity", "A team was updated", "identity-service",
		dataSchema("identity.team.updated", nil, `"changes":{"type":"object"}`)},
	{EventIdentityTeamDeleted, "identity", "A team was deleted", "identity-service",
		dataSchema("identity.team.deleted", nil, "")},

	{EventIdentityMemberAdded, "identity", "A member was added", "identity-service",
		dataSchema("identity.member.added", []string{"user_id"}, `"user_id":{"type":"string"},"role":{"type":"string"}`)},
	{EventIdentityMemberRemoved, "identity", "A member was removed", "identity-service",
		dataSchema("identity.member.removed", []string{"user_id"}, `"user_id":{"type":"string"}`)},
	{EventIdentityMemberRoleChanged, "identity", "A member's role changed", "identity-service",
		dataSchema("identity.member.role_changed", []string{"user_id", "new_role"}, `"user_id":{"type":"string"},"new_role":{"type":"string"},"old_role":{"type":"string"}`)},

	{EventIdentityInvitationCreated, "identity", "An invitation was created", "identity-service",
		dataSchema("identity.invitation.created", []string{"email"}, `"email":{"type":"string"},"role":{"type":"string"}`)},
	{EventIdentityInvitationAccepted, "identity", "An invitation was accepted", "identity-service",
		dataSchema("identity.invitation.accepted", []string{"email"}, `"email":{"type":"string"}`)},
	{EventIdentityInvitationRevoked, "identity", "An invitation was revoked", "identity-service",
		dataSchema("identity.invitation.revoked", []string{"email"}, `"email":{"type":"string"}`)},

	// ---- Work aliases / additions --------------------------------------
	{EventWorkCriterionCreated, "work", "Acceptance criterion created (alias)", "work-service",
		dataSchema("work.criterion.created", nil, `"spec_id":{"type":"string"},"given":{"type":"string"},"when":{"type":"string"},"then":{"type":"string"},"status":{"type":"string"}`)},
	{EventWorkCriterionCompleted, "work", "Acceptance criterion completed", "work-service",
		dataSchema("work.criterion.completed", nil, `"spec_id":{"type":"string"},"status":{"type":"string"}`)},
	{EventWorkCriterionStatusChanged, "work", "Acceptance criterion status changed (alias)", "work-service",
		dataSchema("work.criterion.status_changed", nil, `"spec_id":{"type":"string"},"old_status":{"type":"string"},"new_status":{"type":"string"}`)},

	{EventWorkSpecLinkedToNode, "work", "Spec linked to a canvas node", "work-service",
		dataSchema("work.spec.linked_to_node", nil, `"spec_id":{"type":"string"},"node_id":{"type":"string"},"link_type":{"type":"string"}`)},
	{EventWorkSpecUnlinkedFromNode, "work", "Spec unlinked from a canvas node", "work-service",
		dataSchema("work.spec.unlinked_from_node", nil, `"spec_id":{"type":"string"},"node_id":{"type":"string"}`)},
	{EventWorkSpecLinkedToFeature, "work", "Spec linked to a feature", "work-service",
		dataSchema("work.spec.linked_to_feature", nil, `"feature_id":{"type":"string"},"spec_id":{"type":"string"},"relation":{"type":"string"}`)},
	{EventWorkSpecUnlinkedFromFeature, "work", "Spec unlinked from a feature", "work-service",
		dataSchema("work.spec.unlinked_from_feature", nil, `"feature_id":{"type":"string"},"spec_id":{"type":"string"}`)},

	{EventWorkSignalReceived, "work", "A signal was received (lighter alias of signal.ingested)", "work-service",
		dataSchema("work.signal.received", nil, `"provider_id":{"type":"string"},"category":{"type":"string"},"status":{"type":"string"},"summary":{"type":"string"},"metric_name":{"type":"string"}`)},
	{EventWorkFeatureHealthChanged, "work", "Feature health status transitioned", "work-service",
		dataSchema("work.feature.health_changed", nil, `"name":{"type":"string"},"old_health":{"type":"string"},"new_health":{"type":"string"},"health_status":{"type":"string"},"incident_count":{"type":"integer"},"error_rate_pct":{"type":"number"},"uptime_pct":{"type":"number"},"latency_p99_ms":{"type":"number"}`)},

	// ---- Ops aliases / additions ---------------------------------------
	{EventOpsDeployCreated, "ops", "A deployment was created", "ops-service",
		dataSchema("ops.deploy.created", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"},"version":{"type":"string"}`)},
	{EventOpsDeployStarted, "ops", "A deployment started", "ops-service",
		dataSchema("ops.deploy.started", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"}`)},
	{EventOpsDeployInProgress, "ops", "A deployment is in progress", "ops-service",
		dataSchema("ops.deploy.in_progress", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"},"progress":{"type":"number"}`)},
	{EventOpsDeployCompleted, "ops", "A deployment completed", "ops-service",
		dataSchema("ops.deploy.completed", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"},"status":{"type":"string"},"duration_ms":{"type":"integer"}`)},
	{EventOpsDeployFailed, "ops", "A deployment failed", "ops-service",
		dataSchema("ops.deploy.failed", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"},"error":{"type":"string"}`)},
	{EventOpsDeployRolledBack, "ops", "A deployment was rolled back", "ops-service",
		dataSchema("ops.deploy.rolled_back", nil, `"deployment_id":{"type":"string"},"reason":{"type":"string"}`)},
	{EventOpsDeployRollbackFailed, "ops", "A deployment rollback failed", "ops-service",
		dataSchema("ops.deploy.rollback_failed", nil, `"deployment_id":{"type":"string"},"error":{"type":"string"}`)},
	{EventOpsDeployPromoted, "ops", "A deployment was promoted", "ops-service",
		dataSchema("ops.deploy.promoted", nil, `"deployment_id":{"type":"string"},"from_env":{"type":"string"},"to_env":{"type":"string"}`)},
	{EventOpsDeploymentFailed, "ops", "A deployment failed (deployment.* form)", "ops-service",
		dataSchema("ops.deployment.failed", nil, `"deployment_id":{"type":"string"},"environment":{"type":"string"},"error":{"type":"string"}`)},
	{EventOpsDeploymentRequested, "ops", "Quality-gate evaluator requested a deployment after tests passed", "ops-service",
		dataSchema("ops.deployment.requested", []string{"deployment_id"},
			`"deployment_id":{"type":"string"},"test_run_id":{"type":"string"},"artifact":{"type":"string"},"session_id":{"type":"string"},"spec_id":{"type":"string"},"trigger":{"type":"string"},"passing_gates":{"type":"integer"}`)},

	{EventOpsAlertFired, "ops", "An alert was fired (canonical name; ops.alert.triggered is an alias)", "ops-service",
		dataSchema("ops.alert.fired", nil, `"severity":{"type":"string"},"service":{"type":"string"},"summary":{"type":"string"}`)},
	{EventOpsAlertSilenced, "ops", "An alert was silenced", "ops-service",
		dataSchema("ops.alert.silenced", nil, `"alert_id":{"type":"string"},"silenced_by":{"type":"string"},"duration":{"type":"string"}`)},

	{EventOpsIncidentCreated, "ops", "An incident was created", "ops-service",
		dataSchema("ops.incident.created", nil, `"incident_id":{"type":"string"},"severity":{"type":"string"},"title":{"type":"string"}`)},
	{EventOpsIncidentSeverityChanged, "ops", "Incident severity changed", "ops-service",
		dataSchema("ops.incident.severity_changed", nil, `"incident_id":{"type":"string"},"old_severity":{"type":"string"},"new_severity":{"type":"string"}`)},
	{EventOpsIncidentStatusChanged, "ops", "Incident status changed", "ops-service",
		dataSchema("ops.incident.status_changed", nil, `"incident_id":{"type":"string"},"old_status":{"type":"string"},"new_status":{"type":"string"}`)},
	{EventOpsIncidentPostMortemCreated, "ops", "Incident post-mortem document created", "ops-service",
		dataSchema("ops.incident.post_mortem_created", nil, `"incident_id":{"type":"string"},"post_mortem_id":{"type":"string"}`)},

	{EventOpsProbeFailed, "ops", "A probe failed (alias of probe.failure)", "ops-service",
		dataSchema("ops.probe.failed", nil, `"probe_id":{"type":"string"},"error":{"type":"string"}`)},
	{EventOpsProbeResultSignal, "ops", "A probe result published as a work-service signal", "ops-service",
		dataSchema("ops.probe.result_signal", nil, `"probe_id":{"type":"string"},"signal_status":{"type":"string"},"signal_category":{"type":"string"},"target_id":{"type":"string"},"latency_ms":{"type":"integer"},"metric_name":{"type":"string"}`)},

	{EventOpsDeliveryGatePassing, "ops", "A delivery gate is passing", "ops-service",
		dataSchema("ops.delivery_gate.passing", nil, `"gate":{"type":"string"},"pipeline_id":{"type":"string"}`)},
	{EventOpsDeliveryGateFailing, "ops", "A delivery gate is failing", "ops-service",
		dataSchema("ops.delivery_gate.failing", nil, `"gate":{"type":"string"},"pipeline_id":{"type":"string"},"reason":{"type":"string"}`)},
	{EventOpsDeliveryGateOverridden, "ops", "A delivery gate was overridden", "ops-service",
		dataSchema("ops.delivery_gate.overridden", nil, `"gate":{"type":"string"},"pipeline_id":{"type":"string"},"overridden_by":{"type":"string"}`)},

	{EventOpsPipelineCancelled, "ops", "A pipeline was cancelled", "ops-service",
		dataSchema("ops.pipeline.cancelled", nil, `"pipeline_id":{"type":"string"},"reason":{"type":"string"}`)},

	{EventOpsSelfHealingTriggered, "ops", "A self-healing action was triggered", "ops-service",
		dataSchema("ops.self_healing.triggered", nil, `"alert_id":{"type":"string"},"action":{"type":"string"}`)},

	{EventOpsPreviewCreated, "ops", "Preview environment created", "ops-service",
		dataSchema("ops.preview.created", nil, `"preview_id":{"type":"string"},"branch":{"type":"string"},"url":{"type":"string"}`)},
	{EventOpsPreviewUpdated, "ops", "Preview environment updated", "ops-service",
		dataSchema("ops.preview.updated", nil, `"preview_id":{"type":"string"},"changes":{"type":"object"}`)},
	{EventOpsPreviewDeleted, "ops", "Preview environment deleted", "ops-service",
		dataSchema("ops.preview.deleted", nil, `"preview_id":{"type":"string"}`)},

	{EventOpsCostAnomaly, "ops", "Cost anomaly detected", "ops-service",
		dataSchema("ops.cost.anomaly", nil, `"service":{"type":"string"},"amount":{"type":"number"},"baseline":{"type":"number"}`)},
	{EventOpsCostBudgetThreshold, "ops", "Cost budget threshold crossed", "ops-service",
		dataSchema("ops.cost.budget_threshold", nil, `"budget_id":{"type":"string"},"threshold":{"type":"number"},"current":{"type":"number"}`)},

	{EventOpsComplianceContinuousReport, "ops", "Continuous compliance report emitted", "ops-service",
		dataSchema("ops.compliance.continuous_report", nil, `"framework":{"type":"string"},"score":{"type":"number"},"findings":{"type":"integer"}`)},

	{EventOpsMonitoringPredictiveAnomaly, "ops", "Predictive monitoring anomaly", "ops-service",
		dataSchema("ops.monitoring.predictive_anomaly", nil, `"metric":{"type":"string"},"forecast":{"type":"number"},"horizon_minutes":{"type":"integer"}`)},

	{EventOpsServiceCreated, "ops", "A service catalog entry was created", "ops-service",
		dataSchema("ops.service.created", nil, `"name":{"type":"string"},"owner":{"type":"string"}`)},
	{EventOpsServiceUpdated, "ops", "A service catalog entry was updated", "ops-service",
		dataSchema("ops.service.updated", nil, `"name":{"type":"string"},"changes":{"type":"object"}`)},
	{EventOpsServiceDeleted, "ops", "A service catalog entry was deleted", "ops-service",
		dataSchema("ops.service.deleted", nil, `"name":{"type":"string"}`)},
	{EventOpsServiceLifecycleChanged, "ops", "Service lifecycle stage changed", "ops-service",
		dataSchema("ops.service.lifecycle_changed", nil, `"name":{"type":"string"},"stage":{"type":"string"}`)},
	{EventOpsServiceDiscovered, "ops", "A service was discovered", "ops-service",
		dataSchema("ops.service.discovered", nil, `"name":{"type":"string"},"source":{"type":"string"}`)},

	{EventOpsTargetCreated, "ops", "A deploy target was created", "ops-service",
		dataSchema("ops.target.created", nil, `"name":{"type":"string"},"kind":{"type":"string"}`)},
	{EventOpsTargetUpdated, "ops", "A deploy target was updated", "ops-service",
		dataSchema("ops.target.updated", nil, `"name":{"type":"string"},"changes":{"type":"object"}`)},
	{EventOpsTargetDeleted, "ops", "A deploy target was deleted", "ops-service",
		dataSchema("ops.target.deleted", nil, `"name":{"type":"string"}`)},

	{EventOpsTestPostMergeTriggered, "ops", "Post-merge test triggered", "ops-service",
		dataSchema("ops.test.post_merge_triggered", nil, `"suite":{"type":"string"},"commit_sha":{"type":"string"}`)},
	{EventOpsTestOnCommitTriggered, "ops", "On-commit test triggered", "ops-service",
		dataSchema("ops.test.on_commit_triggered", nil, `"suite":{"type":"string"},"commit_sha":{"type":"string"}`)},
	{EventOpsTestOnSessionTriggered, "ops", "On-session test triggered", "ops-service",
		dataSchema("ops.test.on_session_triggered", nil, `"suite":{"type":"string"},"session_id":{"type":"string"}`)},

	{EventOpsAdminImpersonationStarted, "ops", "Admin impersonation session started", "ops-service",
		dataSchema("ops.admin.impersonation.started", nil, `"admin_id":{"type":"string"},"target_user_id":{"type":"string"},"reason":{"type":"string"}`)},

	// ---- Canvas additions ---------------------------------------------
	{EventCanvasImportProgress, "canvas", "Repository import progress update", "canvas-service",
		dataSchema("canvas.import.progress", nil, `"repository":{"type":"string"},"phase":{"type":"string"},"percent":{"type":"number"}`)},
	{EventCanvasImportCompleted, "canvas", "Repository import completed", "canvas-service",
		dataSchema("canvas.import.completed", nil, `"repository":{"type":"string"},"node_count":{"type":"integer"}`)},
	{EventCanvasImportFailed, "canvas", "Repository import failed", "canvas-service",
		dataSchema("canvas.import.failed", nil, `"repository":{"type":"string"},"error":{"type":"string"}`)},
	{EventCanvasExternalProxyCreated, "canvas", "An external proxy was created", "canvas-service",
		dataSchema("canvas.external_proxy.created", nil, `"name":{"type":"string"},"endpoint":{"type":"string"},"kind":{"type":"string"}`)},

	// ---- Git legacy "sentiae.git.*" event types -----------------------
	{EventGitLegacyRepositoryCreated, "sentiae", "Repository created (legacy git-service event type)", "git-service",
		dataSchema("sentiae.git.repository.created", nil, `"name":{"type":"string"},"default_branch":{"type":"string"}`)},
	{EventGitLegacyRepositoryImported, "sentiae", "Repository imported (legacy)", "git-service",
		dataSchema("sentiae.git.repository.imported", nil, `"name":{"type":"string"},"source_url":{"type":"string"}`)},
	{EventGitLegacyRepositoryForked, "sentiae", "Repository forked (legacy)", "git-service",
		dataSchema("sentiae.git.repository.forked", nil, `"name":{"type":"string"},"parent_id":{"type":"string"}`)},

	{EventGitLegacyPush, "sentiae", "Git push received (legacy)", "git-service",
		dataSchema("sentiae.git.push", nil, `"branch":{"type":"string"},"repository_id":{"type":"string"},"commits":{"type":"array"}`)},

	{EventGitLegacyCommitCreated, "sentiae", "Commit created (legacy)", "git-service",
		dataSchema("sentiae.git.commit.created", nil, `"sha":{"type":"string"},"author":{"type":"string"},"message":{"type":"string"}`)},

	{EventGitLegacyPRCreated, "sentiae", "PR created (legacy)", "git-service",
		dataSchema("sentiae.git.pr.created", nil, `"pr_number":{"type":"integer"},"title":{"type":"string"}`)},
	{EventGitLegacyPRUpdated, "sentiae", "PR updated (legacy)", "git-service",
		dataSchema("sentiae.git.pr.updated", nil, `"pr_number":{"type":"integer"}`)},
	{EventGitLegacyPRMerged, "sentiae", "PR merged (legacy)", "git-service",
		dataSchema("sentiae.git.pr.merged", nil, `"pr_number":{"type":"integer"},"merge_sha":{"type":"string"}`)},
	{EventGitLegacyPRClosed, "sentiae", "PR closed (legacy)", "git-service",
		dataSchema("sentiae.git.pr.closed", nil, `"pr_number":{"type":"integer"}`)},
	{EventGitLegacyPRReviewRequested, "sentiae", "PR review requested (legacy)", "git-service",
		dataSchema("sentiae.git.pr.review_requested", nil, `"pr_number":{"type":"integer"},"reviewer_id":{"type":"string"}`)},
	{EventGitLegacyPRApproved, "sentiae", "PR approved (legacy)", "git-service",
		dataSchema("sentiae.git.pr.approved", nil, `"pr_number":{"type":"integer"},"reviewer_id":{"type":"string"}`)},
	{EventGitLegacyPRChangesRequested, "sentiae", "PR changes requested (legacy)", "git-service",
		dataSchema("sentiae.git.pr.changes_requested", nil, `"pr_number":{"type":"integer"},"reviewer_id":{"type":"string"}`)},
	{EventGitLegacyPRReviewSubmitted, "sentiae", "PR review submitted (legacy)", "git-service",
		dataSchema("sentiae.git.pr.review.submitted", nil, `"pr_number":{"type":"integer"},"reviewer_id":{"type":"string"},"state":{"type":"string"}`)},

	{EventGitLegacyBranchCreated, "sentiae", "Branch created (legacy)", "git-service",
		dataSchema("sentiae.git.branch.created", nil, `"branch":{"type":"string"},"repository_id":{"type":"string"}`)},
	{EventGitLegacyBranchDeleted, "sentiae", "Branch deleted (legacy)", "git-service",
		dataSchema("sentiae.git.branch.deleted", nil, `"branch":{"type":"string"},"repository_id":{"type":"string"}`)},
	{EventGitLegacyBranchProtectedChanged, "sentiae", "Branch protection toggled (legacy)", "git-service",
		dataSchema("sentiae.git.branch.protected_changed", nil, `"branch":{"type":"string"},"protected":{"type":"boolean"}`)},

	{EventGitLegacySessionCreated, "sentiae", "Coding session created (legacy)", "git-service",
		dataSchema("sentiae.git.session.created", nil, `"session_id":{"type":"string"},"branch":{"type":"string"}`)},
	{EventGitLegacySessionCodeReady, "sentiae", "Coding session code ready (Tier 2 trigger)", "git-service",
		dataSchema("sentiae.git.session.code_ready", nil, `"session_id":{"type":"string"},"branch":{"type":"string"},"commit_sha":{"type":"string"}`)},
	{EventGitLegacySessionMerged, "sentiae", "Coding session merged (legacy)", "git-service",
		dataSchema("sentiae.git.session.merged", nil, `"session_id":{"type":"string"},"merge_sha":{"type":"string"}`)},
	{EventGitLegacySessionClosed, "sentiae", "Coding session closed (legacy)", "git-service",
		dataSchema("sentiae.git.session.closed", nil, `"session_id":{"type":"string"},"reason":{"type":"string"}`)},

	{EventGitLegacyAIReviewCompleted, "sentiae", "AI code review completed (legacy)", "git-service",
		dataSchema("sentiae.git.ai_review.completed", nil, `"pr_number":{"type":"integer"},"verdict":{"type":"string"}`)},
	{EventGitLegacyReleaseCreated, "sentiae", "Release created (legacy)", "git-service",
		dataSchema("sentiae.git.release.created", nil, `"tag":{"type":"string"}`)},
	{EventGitLegacyDocStale, "sentiae", "Doc flagged stale (legacy)", "git-service",
		dataSchema("sentiae.git.doc.stale", nil, `"path":{"type":"string"},"reason":{"type":"string"}`)},

	// ---- Hierarchical spec events ------------------------------------
	{EventWorkSpecChildCreated, "work", "A child spec was created under a parent spec", "work-service",
		dataSchema("work.spec.child_created", []string{"parent_spec_id", "child_spec_id"}, `"parent_spec_id":{"type":"string"},"child_spec_id":{"type":"string"},"title":{"type":"string"},"priority":{"type":"string"}`)},
	{EventWorkSpecDecomposed, "work", "A spec was decomposed into child specs", "work-service",
		dataSchema("work.spec.decomposed", []string{"parent_spec_id", "child_count"}, `"parent_spec_id":{"type":"string"},"child_count":{"type":"integer"},"child_spec_ids":{"type":"array","items":{"type":"string"}}`)},
	{EventWorkSpecDecompositionTriggered, "work", "A decomposition attempt was dispatched to foundry", "work-service",
		dataSchema("work.spec.decomposition_triggered", []string{"spec_id"}, `"spec_id":{"type":"string"},"attempt":{"type":"integer"},"source_kind":{"type":"string"},"dispatch_target":{"type":"string"}`)},

	// ---- Feature metrics / rollups -----------------------------------
	{EventWorkFeatureMetricsUpdated, "work", "Feature usage metrics (DAU/MAU) were updated", "work-service",
		dataSchema("work.feature.metrics_updated", []string{"feature_id", "period_start"}, `"feature_id":{"type":"string"},"period_start":{"type":"string"},"dau":{"type":"integer"},"mau":{"type":"integer"},"total_actions":{"type":"integer"}`)},
	{EventWorkFeatureCodeHealthUpdated, "work", "Feature code-health metrics were updated", "work-service",
		dataSchema("work.feature.code_health_updated", []string{"feature_id"}, `"feature_id":{"type":"string"},"churn_score":{"type":"number"},"test_coverage_pct":{"type":"number"},"static_analysis_severity":{"type":"string"},"commit_count_7d":{"type":"integer"},"pr_count_7d":{"type":"integer"}`)},
	{EventWorkFeatureCostUpdated, "work", "Feature live cost attribution was updated", "work-service",
		dataSchema("work.feature.cost_updated", []string{"feature_id"}, `"feature_id":{"type":"string"},"cost_usd_24h":{"type":"number"},"source":{"type":"string"}`)},

	// ---- Analytics ---------------------------------------------------
	{EventAnalyticsUsageRecorded, "analytics", "A single user action was recorded against a feature", "portal",
		dataSchema("analytics.usage.recorded", []string{"user_id", "feature_id", "action"}, `"user_id":{"type":"string"},"feature_id":{"type":"string"},"action":{"type":"string"},"session_id":{"type":"string"},"properties":{"type":"object"}`)},

	// ---- Pulse (flow visualization) ---------------------------------
	{EventPulseFlowCreated, "pulse", "Pulse created a new flow from a saga.*.started event", "pulse-service",
		dataSchema("pulse.flow.created", []string{"saga_id"}, `"saga_id":{"type":"string"},"kind":{"type":"string"},"state":{"type":"string"},"steps_total":{"type":"integer"}`)},
	{EventPulseFlowStepStarted, "pulse", "A step within a tracked flow started", "pulse-service",
		dataSchema("pulse.flow.step_started", []string{"saga_id"}, `"saga_id":{"type":"string"},"kind":{"type":"string"},"step":{"type":"string"}`)},
	{EventPulseFlowStepCompleted, "pulse", "A step within a tracked flow completed", "pulse-service",
		dataSchema("pulse.flow.step_completed", []string{"saga_id"}, `"saga_id":{"type":"string"},"kind":{"type":"string"},"state":{"type":"string"},"steps_complete":{"type":"integer"},"steps_total":{"type":"integer"}`)},
	{EventPulseFlowCompleted, "pulse", "A tracked flow reached a terminal completed state", "pulse-service",
		dataSchema("pulse.flow.completed", []string{"saga_id"}, `"saga_id":{"type":"string"},"kind":{"type":"string"},"state":{"type":"string"},"steps_complete":{"type":"integer"},"steps_total":{"type":"integer"}`)},
	{EventPulseFlowFailed, "pulse", "A tracked flow failed (terminal)", "pulse-service",
		dataSchema("pulse.flow.failed", []string{"saga_id"}, `"saga_id":{"type":"string"},"kind":{"type":"string"},"state":{"type":"string"},"steps_complete":{"type":"integer"},"steps_total":{"type":"integer"}`)},
	{EventPulseFlowReplayed, "pulse", "Pulse replayed the audited event sequence for a flow", "pulse-service",
		dataSchema("pulse.flow.replayed", nil, `"saga_id":{"type":"string"},"original_event":{"type":"string"},"occurred_at":{"type":"string"},"payload":{"type":"string"}`)},

	// audit domain
	{EventAuditReplayExecuted, "audit", "An admin replayed a batch of audited events", "pulse-service",
		dataSchema("audit.replay.executed", nil, `"replayed_event":{"type":"string"},"payload":{"type":"string"}`)},
}

// registry is the package-level lookup table. It is built once at init.
var (
	registryMu sync.RWMutex
	registry   = make(map[string]RegisteredEvent, len(registeredEvents))
)

func init() {
	for _, e := range registeredEvents {
		registry[e.Type] = e
	}
}

// LookupEvent returns the registered event for a bare event type.
// ok is false if the event is not registered.
func LookupEvent(eventType string) (RegisteredEvent, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	e, ok := registry[eventType]
	return e, ok
}

// AllEvents returns all registered events sorted by type. Useful for tooling.
func AllEvents() []RegisteredEvent {
	registryMu.RLock()
	out := make([]RegisteredEvent, 0, len(registry))
	for _, e := range registry {
		out = append(out, e)
	}
	registryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// EventsByDomain returns all registered events for a domain, sorted by type.
func EventsByDomain(domain string) []RegisteredEvent {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out []RegisteredEvent
	for _, e := range registry {
		if e.Domain == domain {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// RegisterExtensionEvent lets services add schemas at runtime (for in-dev
// additions). In production, prefer adding the constant here so all
// consumers can statically reference it.
func RegisterExtensionEvent(e RegisteredEvent) error {
	if e.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if err := ValidateEventType(e.Type); err != nil {
		return err
	}
	if e.Schema == "" {
		return fmt.Errorf("schema is required for %s", e.Type)
	}
	registryMu.Lock()
	registry[e.Type] = e
	registryMu.Unlock()
	return nil
}

// KnownTopics returns the de-duplicated full topic list for all registered
// events with the given prefix (default "sentiae").
func KnownTopics(prefix string) []string {
	if prefix == "" {
		prefix = "sentiae"
	}
	seen := make(map[string]struct{})
	registryMu.RLock()
	for _, e := range registry {
		seen[topicFromEventType(prefix, e.Type)] = struct{}{}
	}
	registryMu.RUnlock()
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// DomainOf returns the leading dotted segment of a bare event type.
func DomainOf(eventType string) string {
	if i := strings.Index(eventType, "."); i > 0 {
		return eventType[:i]
	}
	return eventType
}
