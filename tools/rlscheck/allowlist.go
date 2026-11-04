package rlscheck

// replayNotForcedReason is the documented gap on dbx.ServiceTokenReplayDDL:
// RLS is enabled but not forced so SECURITY DEFINER service_token_replay_purge()
// can delete every tenant's expired ids; passage_app is still bound by the
// tenant policy and may only insert. Any service that copies the template is
// covered — the table is allowlisted by name so a new copy cannot fail CI.
const replayNotForcedReason = "permanent: RLS enabled but not forced so the SECURITY DEFINER service_token_replay_purge() can delete every tenant's expired ids; passage_app is still bound by the tenant policy and may only insert"

func allowReason(v violation) (string, bool) {
	if v.Table == "service_token_replay" && v.Problem == problemNotForced {
		return replayNotForcedReason, true
	}
	reason, ok := allowlist[v.key()]
	return reason, ok
}

var allowlist = map[string]string{
	"eam.asset:" + problemNoEntityPol:                 "tracked: #486; legal_entity_id is nullable until PUT /v1/eam/assets/{id}/legal-entity; tenant policy still binds",
	"eam.work_order:" + problemNoEntityPol:            "tracked: #486; legal_entity_id is copied from the asset and may be null for legacy orders; tenant policy still binds",
	"eam.eam_gl_posting:" + problemNoEntityPol:        "tracked: #486; tenant policy binds; legal-entity scope is enforced on eam_pending_booking before the subledger row is written",
	"eam.eam_posting_profile:" + problemNoEntityPol:   "tracked: #486; tenant policy binds; profile rows are keyed by legal_entity_id in the primary key",
	"edi.partner_trade_profile:" + problemNoEntityPol: "tracked: #486; tenant policy binds; legal_entity_id is a required column, not a caller scope filter",
	// Per the #398 review decision: identity 0008_policy_bundle_read adds operator-gated permissive SELECT policies so the policy compiler can read users and role assignments; the gate is passage.policy_compile plus a visible identity_operator row.
	"identity.identity_users:" + problemExtraPolicy:            "permanent: SELECT-only policy gated on passage.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"identity.identity_role_assignments:" + problemExtraPolicy: "permanent: SELECT-only policy gated on passage.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"close.reopen_expiry:" + problemNotForced:                  "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:                "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"identity.identity_tenants:" + problemExtraPolicy:          "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
	"ledger.description_template:" + problemNullEntity:         "permanent: NULL legal_entity_id is a tenant-wide description template, not a missing scope",
	"org.address_purpose:" + problemNullEntity:                 "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.currency_rounding:" + problemNullEntity:               "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.org_unit_translation:" + problemNullEntity:            "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.postal_address:" + problemNullEntity:                  "permanent: NULL legal_entity_id is tenant-wide reference data",
}
