package entitlement

// ModelTier identifies a Claude model class for Eve. Higher tiers
// (Opus) are reserved for paid plans; free tier gets Haiku.
// (Paradigm Shift §I.3)
type ModelTier string

const (
	ModelTierHaiku  ModelTier = "haiku"  // free / eve.basic only
	ModelTierSonnet ModelTier = "sonnet" // eve.full
	ModelTierOpus   ModelTier = "opus"   // eve.full + eve.max_quota
)

// ModelTierForSet picks the best available tier for the bearer's
// entitlement set. Defaults to Haiku for free / unknown plans.
//
// Mapping:
//   - eve.full + eve.max_quota → Opus
//   - eve.full                 → Sonnet
//   - eve.basic                → Haiku
//   - none                     → Haiku (Eve disabled at quota=0
//                                applied separately by the caller)
func ModelTierForSet(s *Set) ModelTier {
	if s == nil || s.Empty() {
		return ModelTierHaiku
	}
	if s.Has(EveFull) && s.Has(EveMaxQuota) {
		return ModelTierOpus
	}
	if s.Has(EveFull) {
		return ModelTierSonnet
	}
	return ModelTierHaiku
}

// ModelIDForTier maps a ModelTier to the canonical Anthropic model
// id. Updates whenever Anthropic ships a new model family.
func ModelIDForTier(t ModelTier) string {
	switch t {
	case ModelTierOpus:
		return "claude-opus-4-7"
	case ModelTierSonnet:
		return "claude-sonnet-4-6"
	case ModelTierHaiku:
		fallthrough
	default:
		return "claude-haiku-4-5"
	}
}

// SelectModelID is the convenience wrapper foundry calls per request.
func SelectModelID(s *Set) string {
	return ModelIDForTier(ModelTierForSet(s))
}
