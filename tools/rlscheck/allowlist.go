package rlscheck

var allowlist = map[string]string{
	"batch.consumer_inbox:" + problemNotForced:        "TODO(#299): RLS not enabled",
	"batch.consumer_inbox:" + problemNoTenantID:       "TODO(#299): no tenant_id column",
	"close.reopen_expiry:" + problemNotForced:         "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:       "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"identity.identity_tenants:" + problemExtraPolicy: "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
}
