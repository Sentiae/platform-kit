package retention

// DefaultsFor returns the §H.2 retention table for a plan slug. The
// matrix matches the customer-facing pricing copy: free deletes
// quickly, team archives to S3 cold, enterprise extends to multi-
// year cold for audit. Unknown slugs return the free defaults so a
// new plan that hasn't been configured yet still has retention.
func DefaultsFor(planSlug string) PlanRetention {
	switch planSlug {
	case "team", "max":
		return teamRetention()
	case "enterprise":
		return enterpriseRetention()
	default:
		return freeRetention()
	}
}

func freeRetention() PlanRetention {
	return PlanRetention{
		DataKindSignals:   {DataKind: DataKindSignals, HotDays: 30, DeleteAfter: 30},
		DataKindLogs:      {DataKind: DataKindLogs, HotDays: 3, DeleteAfter: 3},
		DataKindArtifacts: {DataKind: DataKindArtifacts, HotDays: 7, DeleteAfter: 7},
		DataKindAudit:     {DataKind: DataKindAudit, HotDays: 90, DeleteAfter: 90},
	}
}

func teamRetention() PlanRetention {
	return PlanRetention{
		DataKindSignals:        {DataKind: DataKindSignals, HotDays: 30, WarmDays: 60, ColdAfter: 90, DeleteAfter: 365},
		DataKindLogs:           {DataKind: DataKindLogs, HotDays: 30, DeleteAfter: 30},
		DataKindArtifacts:      {DataKind: DataKindArtifacts, HotDays: 90, DeleteAfter: 90},
		DataKindAudit:          {DataKind: DataKindAudit, HotDays: 365, DeleteAfter: 365},
		DataKindEmbeddingsCold: {DataKind: DataKindEmbeddingsCold, HotDays: 30, ColdAfter: 30},
	}
}

func enterpriseRetention() PlanRetention {
	return PlanRetention{
		DataKindSignals:        {DataKind: DataKindSignals, HotDays: 30, WarmDays: 60, ColdAfter: 90, DeleteAfter: 365 * 7},
		DataKindLogs:           {DataKind: DataKindLogs, HotDays: 30, WarmDays: 60, ColdAfter: 90, DeleteAfter: 365},
		DataKindArtifacts:      {DataKind: DataKindArtifacts, HotDays: 365, DeleteAfter: 365},
		DataKindAudit:          {DataKind: DataKindAudit, HotDays: 365 * 7, DeleteAfter: 365 * 7},
		DataKindEmbeddingsCold: {DataKind: DataKindEmbeddingsCold, HotDays: 30, ColdAfter: 30},
	}
}
