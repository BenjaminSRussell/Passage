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
	// #486 review decision: in EAM, legal_entity_id on asset and work_order is the GL booking entity, not a visibility scope. Sites, plans, permits,
	// rounds and every other EAM table are tenant-wide and join these rows, and legacy rows have a NULL entity that a scoped caller must see to set it
	// (PUT /v1/eam/assets/{id}/legal-entity) and that the GL reconciliation lists; a restrictive policy would hide them or would have to admit NULL.
	// The GL tables carrying the entity (eam_posting_profile, eam_gl_posting, eam_pending_booking) do have the policy (0010, 0011).
	"eam.asset:" + problemNoEntityPol:      "permanent: tenant-wide EAM master data; legal_entity_id is the GL booking entity, nullable for legacy assets a scoped caller must see to set it (#486 review decision)",
	"eam.work_order:" + problemNoEntityPol: "permanent: tenant-wide EAM work; legal_entity_id is copied from the asset for GL booking and is null for legacy orders (#486 review decision)",
	// Per the #398 review decision: identity 0008_policy_bundle_read adds operator-gated permissive SELECT policies so the policy compiler can read users and role assignments; the gate is passage.policy_compile plus a visible identity_operator row.
	"identity.identity_users:" + problemExtraPolicy:               "permanent: SELECT-only policy gated on passage.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"identity.identity_role_assignments:" + problemExtraPolicy:    "permanent: SELECT-only policy gated on passage.policy_compile and identity_operator (0008_policy_bundle_read, #398 review decision)",
	"close.reopen_expiry:" + problemNotForced:                     "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.reopen_expiry:" + problemNoTenantPol:                   "permanent: no passage_app grant; reached only via SECURITY DEFINER functions that scan across tenants by design",
	"close.accrual_due:" + problemNotForced:                       "permanent: no passage_app grant; maintained by the SECURITY DEFINER accrual_run_track_due() trigger and read only by close_accrual_due_tenants(), which lists due tenants across tenants for the accrual sweeper (0007_slice_b, #460)",
	"close.accrual_due:" + problemNoTenantPol:                     "permanent: no passage_app grant; maintained by the SECURITY DEFINER accrual_run_track_due() trigger and read only by close_accrual_due_tenants(), which lists due tenants across tenants for the accrual sweeper (0007_slice_b, #460)",
	"fulfillment.sales_delivery_report_due:" + problemNotForced:   "permanent: no passage_app grant; maintained by the SECURITY DEFINER sales_delivery_report_track_due() trigger and read only by fulfillment_sales_report_due_tenants(), which lists the tenants with a sales delivery report due across tenants for the reporter (0035_sales_delivery_report, #496)",
	"fulfillment.sales_delivery_report_due:" + problemNoTenantPol: "permanent: no passage_app grant; maintained by the SECURITY DEFINER sales_delivery_report_track_due() trigger and read only by fulfillment_sales_report_due_tenants(), which lists the tenants with a sales delivery report due across tenants for the reporter (0035_sales_delivery_report, #496)",
	"billing.sales_invoice_report_due:" + problemNotForced:        "permanent: no passage_app grant; maintained by the SECURITY DEFINER sales_invoice_report_track_due() trigger and read only by billing_sales_report_due_tenants(), which lists the tenants with a sales invoice report due across tenants for the reporter (0014_sales_invoice_report, #29)",
	"billing.sales_invoice_report_due:" + problemNoTenantPol:      "permanent: no passage_app grant; maintained by the SECURITY DEFINER sales_invoice_report_track_due() trigger and read only by billing_sales_report_due_tenants(), which lists the tenants with a sales invoice report due across tenants for the reporter (0014_sales_invoice_report, #29)",
	"edi.partner_entity_index:" + problemNotForced:                "permanent: no passage_app grant; maintained by the SECURITY DEFINER partner_trade_profile trigger and read only by edi_partner_in_entity_scope(), which returns whether the current caller's scopes cover the legal entity of a partner's trade profile, so the partner update can refuse a caller the profile's restrictive policy hides it from (0009_partner_update, #483)",
	"edi.partner_entity_index:" + problemNoTenantPol:              "permanent: no passage_app grant; maintained by the SECURITY DEFINER partner_trade_profile trigger and read only by edi_partner_in_entity_scope(), which filters by passage.tenant_id itself and returns a boolean (0009_partner_update, #483)",
	"edi.partner_entity_index:" + problemNoEntityPol:              "permanent: the index exists to answer the legal-entity question the trade profile's restrictive policy hides; no passage_app grant, read only by edi_partner_in_entity_scope() (0009_partner_update, #483)",
	"edi.transport_work:" + problemNotForced:                      "permanent: no passage_app grant; maintained by the SECURITY DEFINER partner_transport, transport_delivery and transport_mdn triggers and read only by edi_as2_endpoint() (endpoint id to tenant and partner for the unauthenticated AS2 listener) and edi_transport_due_tenants() (due tenants for the dispatcher, the MDN timeout sweep and the SFTP poller) (0006_partner_transport, 0007_as2_mdn_queue, #483)",
	"edi.transport_work:" + problemNoTenantPol:                    "permanent: no passage_app grant; maintained by the SECURITY DEFINER partner_transport, transport_delivery and transport_mdn triggers and read only by edi_as2_endpoint() (endpoint id to tenant and partner for the unauthenticated AS2 listener) and edi_transport_due_tenants() (due tenants for the dispatcher, the MDN timeout sweep and the SFTP poller) (0006_partner_transport, 0007_as2_mdn_queue, #483)",
	"identity.identity_tenants:" + problemExtraPolicy:             "permanent: SELECT-only operator policy gated on identity_operator, whose own policy is tenant-scoped; lets the provisioning operator list tenants",
	"ledger.description_template:" + problemNullEntity:            "permanent: NULL legal_entity_id is a tenant-wide description template, not a missing scope",
	"org.address_purpose:" + problemNullEntity:                    "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.currency_rounding:" + problemNullEntity:                  "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.org_unit_translation:" + problemNullEntity:               "permanent: NULL legal_entity_id is tenant-wide reference data",
	"org.postal_address:" + problemNullEntity:                     "permanent: NULL legal_entity_id is tenant-wide reference data",
}
