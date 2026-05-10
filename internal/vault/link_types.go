package vault

// Dedicated link_type constants for deterministic auto-linking
// (task-based and delegation-based). These types live OUTSIDE
// validClassifyTypes in enrich_classify.go so DeleteDocLinksByTypes
// cannot wipe them on the same enrichment tick.
const (
	LinkTypeTaskAttachment       = "task_attachment"
	LinkTypeDelegationAttachment = "delegation_attachment"
)
