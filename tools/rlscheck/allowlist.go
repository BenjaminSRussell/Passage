package rlscheck

var allowlist = map[string]string{
	"batch.consumer_inbox":          "TODO(#299): no tenant_id column and RLS not enabled",
	"close.reopen_expiry":           "TODO(#299): RLS not enabled and no tenant policy",
	"identity.identity_operator":    "TODO(#299): no tenant policy",
	"metadata.metadata_usage_daily": "TODO(#299): no restrictive legal-entity policy",
	"sales.sales_number_sequence":   "TODO(#299): no restrictive legal-entity policy",
}
