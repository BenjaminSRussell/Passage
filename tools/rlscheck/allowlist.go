package rlscheck

var allowlist = map[string]string{
	"close.service_token_replay:" + problemNotForced:      "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"fx.service_token_replay:" + problemNotForced:         "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"inventory.service_token_replay:" + problemNotForced:  "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"ledger.service_token_replay:" + problemNotForced:     "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"numbering.service_token_replay:" + problemNotForced:  "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"payables.service_token_replay:" + problemNotForced:   "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"party.service_token_replay:" + problemNotForced:      "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"product.service_token_replay:" + problemNotForced:    "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"purchasing.service_token_replay:" + problemNotForced: "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert",
	"close.reopen_expiry:" + problemNotForced:             "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:           "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"identity.identity_tenants:" + problemExtraPolicy:     "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
	"ledger.description_template:" + problemNullEntity:    "permanent: NULL legal_entity_id is a tenant-wide description template, not a missing scope",
	"org.address_purpose:" + problemNullEntity:            "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.currency_rounding:" + problemNullEntity:          "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.org_unit_translation:" + problemNullEntity:       "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.postal_address:" + problemNullEntity:             "permanent: NULL legal_entity_id is tenant-wide reference data",
}
