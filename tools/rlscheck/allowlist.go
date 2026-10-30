package rlscheck

var allowlist = map[string]string{
	"batch.consumer_inbox:" + problemNotForced:            "TODO(#299): RLS not enabled",
	"batch.consumer_inbox:" + problemNoTenantID:           "TODO(#299): no tenant_id column",
	"metadata.metadata_usage_daily:" + problemNoEntityPol: "TODO(#299): text legal_entity_id with '' default and no restrictive policy",
	"sales.sales_number_sequence:" + problemNoEntityPol:   "TODO(#299): no restrictive legal-entity policy",
	"close.reopen_expiry:" + problemNotForced:             "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:           "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"identity.identity_tenants:" + problemExtraPolicy:     "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
	"ledger.description_template:" + problemNullEntity:    "permanent: NULL legal_entity_id is a tenant-wide description template, not a missing scope",
	"org.address_purpose:" + problemNullEntity:            "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.currency_rounding:" + problemNullEntity:          "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.org_unit_translation:" + problemNullEntity:       "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.postal_address:" + problemNullEntity:             "permanent: NULL legal_entity_id is tenant-wide reference data",
}
