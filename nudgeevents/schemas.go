package nudgeevents

// Schemas maps each EventType to its JSON Schema definition.
//
// Schemas are intentionally permissive on optional fields and strict on
// required ones (NudgeID, RuleID, UserID, OrgID, GroupKey, Priority, Surface,
// TransitionedAt). Adding new optional fields is backward-compatible; renaming
// or removing required fields is breaking and requires a v2 topic per §28.
var Schemas = map[EventType]string{
	TypeDelivered:  schemaDelivered,
	TypeSeen:       schemaBase,
	TypeActed:      schemaActed,
	TypeDismissed:  schemaDismissed,
	TypeSuppressed: schemaSuppressed,
	TypeExpired:    schemaBase,
	TypeSuperseded: schemaSuperseded,
}

const schemaBaseProperties = `
"nudge_id":        {"type": "string", "format": "uuid"},
"rule_id":         {"type": "string"},
"user_id":         {"type": "string", "format": "uuid"},
"org_id":          {"type": "string", "format": "uuid"},
"product_id":      {"type": ["string", "null"], "format": "uuid"},
"bundle_id":       {"type": ["string", "null"], "format": "uuid"},
"group_key":       {"type": "string"},
"priority":        {"type": "integer", "minimum": 0, "maximum": 100},
"surface":         {"type": "string", "enum": ["bubble", "badge", "inbox"]},
"transitioned_at": {"type": "string", "format": "date-time"}
`

const schemaBaseRequired = `["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at"]`

const schemaBase = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `},
  "required": ` + schemaBaseRequired + `
}`

const schemaDelivered = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `,
    "conversation_id": {"type": "string", "format": "uuid"},
    "message_id":      {"type": "string", "format": "uuid"},
    "expires_at":      {"type": "string", "format": "date-time"}
  },
  "required": ["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at","conversation_id","message_id","expires_at"]
}`

const schemaActed = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `,
    "chip_id":     {"type": "string"},
    "chip_params": {"type": "object"}
  },
  "required": ["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at","chip_id"]
}`

const schemaDismissed = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `,
    "reason": {"type": "string", "enum": ["user", "auto_dismiss", "timeout"]}
  },
  "required": ["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at","reason"]
}`

const schemaSuppressed = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `,
    "reason": {"type": "string", "enum": ["budget_exhausted","dnd","scope_cooldown","engagement_decay","group_exclusion"]}
  },
  "required": ["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at","reason"]
}`

const schemaSuperseded = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "properties": {` + schemaBaseProperties + `,
    "superseded_by_bundle_id": {"type": "string", "format": "uuid"}
  },
  "required": ["nudge_id","rule_id","user_id","org_id","group_key","priority","surface","transitioned_at","superseded_by_bundle_id"]
}`
