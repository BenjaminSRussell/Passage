package authz

import rego.v1

concrete(resource) := replace(replace(resource, "{,/**}", "/item-1"), "*", "item-1")

allowed(duty, method, resource) if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": method,
		"resource": resource,
	}
		with data.assignments as {"tenant-a": {"user-a": [duty]}}
	result.allow
}

has_route_permissions(spec) if count(spec.permissions) > 0

has_route_permissions(spec) if {
	spec.flag == true
	count(spec.permissions) == 0
}

test_all_seeded_duties_allow_their_declared_permissions if {
	count(data.duties) > 0
	every duty, spec in data.duties {
		has_route_permissions(spec)
		every permission in spec.permissions {
			every method in permission.methods {
				allowed(duty, method, concrete(permission.resource))
			}
		}
	}
}

test_flag_duties_grant_no_routes if {
	flag_duties := {"receivables.write-off.approve", "purchasing.inbound.apply", "edi.pii.view", "tax.code.maintain", "fulfillment.order.sync", "budget.override", "inventory.blocking.scrap", "ledger.journal.postdirect", "ledger.journal.postledger", "ledger.period.postclosed", "org.hierarchy.backdate", "party.merge", "pii.read", "pricing.cache.invalidate", "production.batch.consume-expired", "production.journal.over-consume", "production.order.bom-after-release", "production.order.rework", "production.planned.intake", "receivables.openitem.from-billing", "revrec.creditnote.reverse", "revrec.contract.from-invoice", "production.order.zero-output", "production.jobcard.shopfloor", "scheduling.constraints.override", "scheduling.order.import", "search.index.write", "shopfloor.job.sync", "tenant.administer", "treasury.facility.override"}
	{d | some d, spec in data.duties; spec.flag == true} == flag_duties
	every duty in flag_duties {
		count(data.duties[duty].permissions) == 0
		not allowed(duty, "POST", "/v1/ledger/journal-entries")
		not allowed(duty, "POST", "/v1/inventory/count-sheets/c1:post")
	}
}

segregation_denials := [
	["inventory.journal.write", "POST", "/v1/inventory/journals/j1:post"],
	["inventory.journal.write", "POST", "/v1/inventory/journals/j1:approve"],
	["inventory.count.write", "POST", "/v1/inventory/count-sheets/c1:approve"],
	["inventory.count.write", "POST", "/v1/inventory/count-sheets/c1/lines/l1:recount"],
	["inventory.count.write", "POST", "/v1/inventory/count-sheets/c1:post"],
	["inventory.count.post", "POST", "/v1/inventory/count-sheets/c1:approve"],
	["inventory.count.approve", "POST", "/v1/inventory/count-sheets/c1:post"],
	["inventory.count.approve", "PUT", "/v1/inventory/count-sheets/c1/lines/l1"],
	["engineering.bom.write", "POST", "/v1/engineering/ecrs/e1:approve"],
	["engineering.bom.write", "POST", "/v1/engineering/ecrs/e1:review"],
	["engineering.ecr.review", "POST", "/v1/engineering/ecrs/e1:submit"],
	["engineering.bom.write", "POST", "/v1/engineering/documents/d1:sign"],
	["engineering.bom.write", "POST", "/v1/engineering/release-lines/l1:accept"],
	["engineering.doc.sign", "POST", "/v1/engineering/documents"],
	["engineering.release.accept", "POST", "/v1/engineering/release-sessions"],
	["production.batch-record.write", "POST", "/v1/production/batch-records/r1:approve"],
	["production.batch-record.write", "POST", "/v1/production/batch-records/r1:seal"],
	["production.batch-record.approve", "POST", "/v1/production/batch-records/r1/exceptions/x1:disposition"],
	["inventory.transfer.write", "POST", "/v1/inventory/transfer-orders/t1:ship"],
	["inventory.transfer.write", "POST", "/v1/inventory/transfer-orders/t1:receive"],
	["ledger.journal.write", "POST", "/v1/ledger/journal-batches/b1/approve"],
	["ledger.journal.write", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.journal.approve", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.journal.post", "POST", "/v1/ledger/journal-entries/e1/reverse"],
	["ledger.journal.reverse", "POST", "/v1/ledger/journal-entries"],
	["ledger.journal.postledger", "POST", "/v1/ledger/journal-entries"],
	["ledger.journal.read", "POST", "/v1/ledger/ledger-sets/s1/journal-entries"],
	["ledger.entry.post", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.entry.post", "POST", "/v1/ledger/ledger-sets/s1/journal-entries"],
	["ledger.entry.post", "POST", "/v1/ledger/validate"],
	["ledger.entry.post", "POST", "/v1/ledger/journal-entries/e1/reverse"],
	["ledger.entry.validate", "POST", "/v1/ledger/journal-entries"],
	["ledger.batch.post", "POST", "/v1/ledger/journal-entries"],
	["ledger.batch.post", "POST", "/v1/ledger/ledger-sets/s1/journal-entries"],
	["ledger.set.post", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.entry.lookup", "GET", "/v1/ledger/ledgers/l1"],
	["ledger.entry.lookup", "GET", "/v1/ledger/journal-entries/e1"],
	["ledger.ledger.lookup", "GET", "/v1/ledger/journal-lines"],
	["numbering.sequence.allocate", "POST", "/v1/numbering/sequences"],
	["receiving.receipt.maintain", "POST", "/v1/receiving/receipts/r1:post"],
	["receiving.receipt.maintain", "POST", "/v1/receiving/receipts/r1:correct"],
	["receiving.receipt.maintain", "POST", "/v1/receiving/receipts/r1:cancel"],
	["receiving.receipt.post", "POST", "/v1/receiving/invoice-adjustments:post"],
	["receiving.receipt.post", "POST", "/v1/receiving/plan-settlements:run"],
	["receiving.receipt.post", "POST", "/v1/receiving/consignment-settlements:run"],
	["receiving.receipt.post", "POST", "/v1/receiving/ers-settlements:run"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/receipts/r1:post"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/invoices/i1:reject-audit-block"],
	["receiving.matching-policy.maintain", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["receiving.invoice-audit.release", "POST", "/v1/receiving/invoices:post"],
	["receiving.invoice-audit.release", "PUT", "/v1/receiving/audit-policy"],
	["receiving.vendor-invoice.post", "PUT", "/v1/receiving/audit-policy"],
	["receiving.vendor-invoice.post", "PUT", "/v1/receiving/ers-vendors/v1"],
	["procurement.purchase-order.maintain", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["procurement.rfq.maintain", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/outputs/pdf"],
	["reporting.statement.view", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.statement.view", "GET", "/v1/reporting/definitions/d1/export"],
	["platform.release.view", "POST", "/v1/platform/releases"],
	["platform.promotion.approve", "GET", "/v1/platform/releases"],
	["platform.environment.rollback", "DELETE", "/v1/platform/releases/r1"],
	["receiving.receipt.maintain", "GET", "/v1/receiving/invoices/i1"],
	["party.write", "GET", "/v1/party/vendors:search"],
	["party.vendor-bank.write", "GET", "/v1/party/customers:search"],
]

test_narrow_duties_cannot_reach_verb_routes if {
	every denial in segregation_denials {
		not allowed(denial[0], denial[1], denial[2])
	}
}

segregation_grants := [
	["inventory.journal.post", "POST", "/v1/inventory/journals/j1:post"],
	["inventory.transfer.post", "POST", "/v1/inventory/transfer-orders/t1:ship"],
	["ledger.journal.approve", "POST", "/v1/ledger/journal-batches/b1/approve"],
	["ledger.journal.post", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.journal.reverse", "POST", "/v1/ledger/journal-entries/e1/reverse"],
	["ledger.journal.post", "POST", "/v1/ledger/ledger-sets/s1/journal-entries"],
	["ledger.journal.post", "POST", "/v1/ledger/journal-entries"],
	["ledger.journal.post", "POST", "/v1/ledger/validate"],
	["ledger.entry.post", "POST", "/v1/ledger/journal-entries"],
	["ledger.entry.validate", "POST", "/v1/ledger/validate"],
	["ledger.batch.post", "POST", "/v1/ledger/journal-batches/b1/post"],
	["ledger.set.post", "POST", "/v1/ledger/ledger-sets/s1/journal-entries"],
	["ledger.entry.lookup", "GET", "/v1/ledger/journal-lines"],
	["ledger.ledger.lookup", "GET", "/v1/ledger/ledgers/l1"],
	["numbering.sequence.allocate", "POST", "/v1/numbering/sequences/INV/next"],
	["ledger.setup.write", "POST", "/v1/ledger/ledgers/l1/account-mappings"],
	["receiving.receipt.post", "POST", "/v1/receiving/receipts/r1:post"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/invoice-adjustments:post"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/plan-settlements:run"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/consignment-settlements:run"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/ers-settlements:run"],
	["receiving.vendor-invoice.post", "POST", "/v1/receiving/invoice-captures"],
	["receiving.ap.inquire", "GET", "/v1/receiving/invoices"],
	["receiving.ap.inquire", "GET", "/v1/receiving/invoices/i1"],
	["receiving.ap.inquire", "GET", "/v1/receiving/invoice-captures"],
	["receiving.ap.inquire", "GET", "/v1/receiving/invoice-captures/c1"],
	["receiving.ap.inquire", "GET", "/v1/receiving/ers-self-bills/i1"],
	["receiving.service-entry.maintain", "POST", "/v1/receiving/service-entry-sheets"],
	["receiving.service-entry.maintain", "GET", "/v1/receiving/service-entry-sheets/s1"],
	["receiving.service-entry.accept", "GET", "/v1/receiving/service-entry-sheets/s1"],
	["receiving.service-entry.accept", "POST", "/v1/receiving/service-entry-sheets/s1:accept"],
	["receiving.service-entry.accept", "POST", "/v1/receiving/service-entry-sheets/s1:reject"],
	["receiving.asn.maintain", "POST", "/v1/receiving/asns"],
	["receiving.asn.maintain", "GET", "/v1/receiving/asns"],
	["receiving.asn.maintain", "GET", "/v1/receiving/asns/a1"],
	["receiving.asn.receive", "GET", "/v1/receiving/asns"],
	["receiving.asn.receive", "GET", "/v1/receiving/asns/a1"],
	["receiving.asn.receive", "POST", "/v1/receiving/asns/a1:start-receipt"],
	["receiving.invoice-audit.release", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["receiving.invoice-audit.release", "POST", "/v1/receiving/invoices/i1:reject-audit-block"],
	["receiving.ers-price.confirm", "PUT", "/v1/receiving/receipts/r1/lines/1/ers-price-confirmation"],
	["receiving.matching-policy.maintain", "PUT", "/v1/receiving/audit-policy"],
	["receiving.matching-policy.maintain", "PUT", "/v1/receiving/ers-vendors/v1"],
	["procurement.purchase-order.confirm", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["procurement.rfq.award", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.definition.maintain", "GET", "/v1/reporting/definitions/d1/export"],
	["platform.promotion.approve", "POST", "/v1/platform/releases"],
	["platform.environment.rollback", "POST", "/v1/platform/releases"],
	["platform.release.maintain", "POST", "/v1/platform/releases"],
	["party.read", "GET", "/v1/party/parties:search"],
	["party.read", "GET", "/v1/party/vendors:search"],
	["party.read", "GET", "/v1/party/customers:search"],
]

test_verb_routes_granted_to_their_own_duty if {
	every grant in segregation_grants {
		allowed(grant[0], grant[1], grant[2])
	}
}

test_platform_duties_keep_prefix_reach if {
	allowed("party.vendor.maintain", "GET", "/v1/vendors")
	allowed("party.vendor.maintain", "PUT", "/v1/vendors/v1/contacts/c1")
	allowed("fx.rate.view", "GET", "/v1/fx/rates/history")
	not allowed("fx.rate.view", "GET", "/v1/fx/ratesx")
}

test_maintain_vendors_can_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["party.vendor.maintain"]}}
	result.allow
}

test_view_vendors_cannot_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["party.vendor.view"]}}
	result == {"allow": false, "duty_missing": "party.vendor.maintain"}
}

test_missing_vendor_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/vendors/vendor-a",
	}
	result == {"allow": false, "duty_missing": "party.vendor.view"}
}

test_unknown_duty_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Unrecognized duty"]}}
	result.allow == false
}

test_assignment_is_tenant_scoped if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-b", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["party.vendor.maintain"]}}
	result.allow == false
}

test_neighbor_path_does_not_match if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors-extra/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["party.vendor.maintain"]}}
	result.allow == false
}

test_view_products_can_get_but_not_post if {
	allowed := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/product/products/p1",
	}
		with data.assignments as {"tenant-a": {"user-a": ["product.product.view"]}}
	allowed.allow

	denied := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/product/products",
	}
		with data.assignments as {"tenant-a": {"user-a": ["product.product.view"]}}
	denied.allow == false
	denied.duty_missing in {"product.write", "product.product.maintain"}
}

test_missing_identity_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
	result.allow == false
}

test_maintain_number_sequences_can_post_create_and_allocate if {
	create := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/numbering/sequences",
	}
		with data.assignments as {"tenant-a": {"user-a": ["numbering.sequence.maintain"]}}
	create.allow

	allocate := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/numbering/sequences/INV/next",
	}
		with data.assignments as {"tenant-a": {"user-a": ["numbering.sequence.maintain"]}}
	allocate.allow
}

test_view_number_sequences_can_get_but_cannot_allocate if {
	read := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/numbering/sequences/INV",
	}
		with data.assignments as {"tenant-a": {"user-a": ["numbering.sequence.view"]}}
	read.allow

	write := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/numbering/sequences/INV/next",
	}
		with data.assignments as {"tenant-a": {"user-a": ["numbering.sequence.view"]}}
	write == {"allow": false, "duty_missing": "numbering.sequence.maintain"}
}

test_legacy_number_sequences_path_is_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/number-sequences",
	}
		with data.assignments as {"tenant-a": {"user-a": ["numbering.sequence.maintain"]}}
	result.allow == false
}

test_view_audit_log_can_get_record_and_inbox if {
	record := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/records/party.vendor/v-1",
	}
		with data.assignments as {"tenant-a": {"user-a": ["audit.log.view"]}}
	record.allow

	inbox := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/alerts",
	}
		with data.assignments as {"tenant-a": {"user-a": ["audit.log.view"]}}
	inbox.allow
}

test_view_audit_log_cannot_ingest_change if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/changes",
	}
		with data.assignments as {"tenant-a": {"user-a": ["audit.log.view"]}}
	result == {"allow": false, "duty_missing": "audit.change.record"}
}

test_record_audit_changes_can_post_ingest_and_alert_rules if {
	ingest := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/changes",
	}
		with data.assignments as {"tenant-a": {"user-a": ["audit.change.record"]}}
	ingest.allow

	rules := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/alert-rules",
	}
		with data.assignments as {"tenant-a": {"user-a": ["audit.change.record"]}}
	rules.allow
}

test_missing_numbering_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/numbering/sequences/INV",
	}
	result == {"allow": false, "duty_missing": "numbering.read"}
}

test_missing_audit_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/records/party.vendor/v-1",
	}
	result == {"allow": false, "duty_missing": "audit.log.view"}
}

test_approve_workflow_can_get_inbox_and_act if {
	inbox := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/workitems",
	}
		with data.assignments as {"tenant-a": {"user-a": ["workflow.workitem.approve"]}}
	inbox.allow

	act := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/workitems/wi-1/actions",
	}
		with data.assignments as {"tenant-a": {"user-a": ["workflow.workitem.approve"]}}
	act.allow
}

test_view_outbound_messages_cannot_enqueue if {
	read := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/notify/messages",
	}
		with data.assignments as {"tenant-a": {"user-a": ["notify.message.view"]}}
	read.allow

	write := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/notify/messages",
	}
		with data.assignments as {"tenant-a": {"user-a": ["notify.message.view"]}}
	write == {"allow": false, "duty_missing": "notify.message.send"}
}

test_maintain_batch_jobs_can_start_run_and_replay if {
	run := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/batch/jobs/rows-1m/runs",
	}
		with data.assignments as {"tenant-a": {"user-a": ["batch.job.maintain"]}}
	run.allow

	replay := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/events/replay",
	}
		with data.assignments as {"tenant-a": {"user-a": ["batch.job.maintain"]}}
	replay.allow
}

test_production_reads_product_site_defaults if {
	data.service_grants.production.product == ["product.read"]
	allowed("product.read", "GET", "/v1/product/released-products/rp-1/site-defaults/s-1")
}

test_view_exchange_rates_cannot_post_rates if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/fx/rates",
	}
		with data.assignments as {"tenant-a": {"user-a": ["fx.rate.view"]}}
	result == {"allow": false, "duty_missing": "fx.rate.maintain"}
}

test_view_exchange_rates_can_convert if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/fx/convert",
	}
		with data.assignments as {"tenant-a": {"user-a": ["fx.rate.view"]}}
	result.allow
}

test_administer_exchange_rates_allows_only_provider_import if {
	allowed("fx.rate.administer", "POST", "/v1/fx/providers/ecb:import")
	not allowed("fx.rate.administer", "GET", "/v1/fx/providers/ecb:import")
	not allowed("fx.rate.administer", "POST", "/v1/fx/rates")
	not allowed("fx.rate.maintain", "POST", "/v1/fx/providers/ecb:import")
}

test_procurement_rfq_types_and_amendments if {
	allowed("procurement.policy.maintain", "POST", "/v1/procurement/rfq-types")
	allowed("procurement.policy.maintain", "PUT", "/v1/procurement/rfq-types/t-1")
	allowed("procurement.rfq.maintain", "GET", "/v1/procurement/rfq-types/t-1")
	allowed("procurement.rfq.award", "GET", "/v1/procurement/rfq-types")
	allowed("procurement.rfq.maintain", "PUT", "/v1/procurement/rfqs/r-1")
	allowed("procurement.rfq.maintain", "POST", "/v1/procurement/rfqs/r-1/amendments")
	allowed("procurement.rfq.maintain", "PUT", "/v1/procurement/rfqs/r-1/amendments/2/acknowledgements/v-1")
	not allowed("procurement.rfq.maintain", "POST", "/v1/procurement/rfq-types")
	not allowed("procurement.rfq.award", "PUT", "/v1/procurement/rfqs/r-1")
	not allowed("procurement.rfq.award", "POST", "/v1/procurement/rfqs/r-1/amendments")
}

test_payment_file_preview_duty_allows_only_its_route if {
	allowed("payment-file.preview", "POST", "/v1/payables/payment-files:preview")
	not allowed("payment-file.preview", "POST", "/v1/payables/payments/status:parse")
}

test_payment_status_parse_duty_does_not_open_file_preview if {
	allowed("payment-status.parse", "POST", "/v1/payables/payments/status:parse")
	not allowed("payment-status.parse", "POST", "/v1/payables/payment-files:preview")
}

test_fulfillment_carrier_manifests if {
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/manifest-candidates")
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/manifests")
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/manifests/m-1")
	allowed("fulfillment.warehouse.ship", "POST", "/v1/fulfillment/manifests")
	allowed("fulfillment.warehouse.ship", "POST", "/v1/fulfillment/manifests/m-1:cancel")
	not allowed("fulfillment.warehouse.view", "POST", "/v1/fulfillment/manifests")
	not allowed("fulfillment.warehouse.view", "POST", "/v1/fulfillment/manifests/m-1:cancel")
}

test_aging_duties_open_only_their_routes if {
	allowed("aging.read", "GET", "/v1/payables/aging")
	not allowed("aging.read", "GET", "/v1/payables/aging:reconcile")
	allowed("aging.reconcile", "GET", "/v1/payables/aging:reconcile")
	not allowed("aging.reconcile", "GET", "/v1/payables/aging")
}

test_open_item_read_duty_opens_list_and_get_only if {
	allowed("open-item.read", "GET", "/v1/payables/open-items")
	allowed("open-item.read", "GET", "/v1/payables/open-items/i1")
	not allowed("open-item.read", "POST", "/v1/payables/open-items")
	not allowed("open-item.read", "GET", "/v1/payables/payment-proposals/p1")
}

test_payment_run_duties_are_separate_from_proposal_duties if {
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs")
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:generate-file")
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:post-journal")
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:settle")
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:complete")
	allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:fail")
	not allowed("payment-run.execute", "POST", "/v1/payables/payment-runs/r1:approve")
	not allowed("payment-run.execute", "GET", "/v1/payables/payment-runs/r1")
	not allowed("payment-run.execute", "POST", "/v1/payables/payment-proposals/p1:approve")
	allowed("payment-run.read", "GET", "/v1/payables/payment-runs/r1")
	allowed("payment-run.execute", "GET", "/v1/payables/payment-runs/r1/file")
	not allowed("payment-run.read", "GET", "/v1/payables/payment-runs/r1/file")
	not allowed("payment-run.read", "POST", "/v1/payables/payment-runs")
	not allowed("payment-run.read", "POST", "/v1/payables/payment-runs/r1:settle")
	not allowed("payment-proposal.approve", "POST", "/v1/payables/payment-runs")
	not allowed("payment-proposal.write", "POST", "/v1/payables/payment-runs")
	not allowed("payment-proposal.read", "GET", "/v1/payables/payment-runs/r1")
}

test_payment_proposal_duties_are_separate if {
	allowed("payment-proposal.write", "POST", "/v1/payables/payment-proposals")
	not allowed("payment-proposal.write", "POST", "/v1/payables/payment-proposals/p1:approve")
	allowed("payment-proposal.write", "POST", "/v1/payables/payment-proposals/p1:cancel")
	not allowed("payment-proposal.write", "GET", "/v1/payables/payment-proposals/p1:cancel")
	not allowed("payment-proposal.read", "POST", "/v1/payables/payment-proposals/p1:cancel")
	not allowed("payment-proposal.approve", "POST", "/v1/payables/payment-proposals/p1:cancel")
	not allowed("payment-proposal.write", "GET", "/v1/payables/payment-proposals/p1")
	allowed("payment-proposal.read", "GET", "/v1/payables/payment-proposals/p1")
	not allowed("payment-proposal.read", "POST", "/v1/payables/payment-proposals/p1:approve")
	allowed("payment-proposal.approve", "POST", "/v1/payables/payment-proposals/p1:approve")
	not allowed("payment-proposal.approve", "POST", "/v1/payables/payment-proposals")
	not allowed("payment-proposal.approve", "GET", "/v1/payables/payment-proposals/p1")
	allowed("payment-proposal.read", "GET", "/v1/payables/payment-proposals")
	not allowed("payment-proposal.write", "GET", "/v1/payables/payment-proposals")
	not allowed("payment-proposal.read", "POST", "/v1/payables/payment-proposals")
}

test_payment_hold_and_vendor_block_duties_open_only_their_routes if {
	allowed("payment-hold.manage", "POST", "/v1/payables/open-items/i1:hold")
	allowed("payment-hold.manage", "POST", "/v1/payables/open-items/i1:release")
	not allowed("payment-hold.manage", "GET", "/v1/payables/open-items/i1")
	not allowed("payment-hold.manage", "POST", "/v1/payables/vendors/v1:block")
	not allowed("open-item.read", "POST", "/v1/payables/open-items/i1:hold")
	allowed("vendor-block.manage", "POST", "/v1/payables/vendors/v1:block")
	allowed("vendor-block.manage", "POST", "/v1/payables/vendors/v1:unblock")
	not allowed("vendor-block.manage", "POST", "/v1/payables/open-items/i1:hold")
	not allowed("payment-proposal.write", "POST", "/v1/payables/vendors/v1:block")
	not allowed("payment-proposal.approve", "POST", "/v1/payables/open-items/i1:release")
}

test_fulfillment_operation_rules if {
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/operation-rules")
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/operation-rules/w-1")
	allowed("fulfillment.warehouse.release", "PUT", "/v1/fulfillment/operation-rules/w-1")
	not allowed("fulfillment.warehouse.view", "PUT", "/v1/fulfillment/operation-rules/w-1")
}

test_inventory_hold_duties_open_only_their_routes if {
	allowed("inventory.hold.write", "POST", "/v1/inventory/holds")
	allowed("inventory.hold.write", "POST", "/v1/inventory/holds/h1:release")
	allowed("inventory.hold.write", "POST", "/v1/inventory/holds:release-source")
	not allowed("inventory.hold.write", "GET", "/v1/inventory/holds")
	allowed("inventory.read", "GET", "/v1/inventory/holds")
	not allowed("inventory.read", "POST", "/v1/inventory/holds")
	data.duties["inventory.hold.apply"].service_only
	data.service_grants.quality.inventory == ["inventory.hold.apply"]
	allowed("inventory.hold.apply", "POST", "/v1/inventory/holds")
	allowed("inventory.hold.apply", "POST", "/v1/inventory/holds:release-source")
	not allowed("inventory.hold.apply", "POST", "/v1/inventory/holds/h1:release")
	not allowed("inventory.hold.apply", "GET", "/v1/inventory/holds")
	not allowed("inventory.hold.apply", "POST", "/v1/inventory/blockings")
	not allowed("inventory.hold.apply", "POST", "/v1/inventory/journals")
}

test_inventory_issue_claim_is_service_only_and_opens_only_its_routes if {
	data.duties["inventory.issue.claim"].service_only
	every svc in ["warehouse", "fulfillment", "production"] {
		data.service_grants[svc].inventory == ["inventory.issue.claim"]
	}
	allowed("inventory.issue.claim", "POST", "/v1/inventory/issue-claims")
	allowed("inventory.issue.claim", "POST", "/v1/inventory/issue-claims:release")
	not allowed("inventory.issue.claim", "GET", "/v1/inventory/onhand")
	not allowed("inventory.issue.claim", "POST", "/v1/inventory/holds")
	not allowed("inventory.issue.claim", "POST", "/v1/inventory/journals")
	not allowed("inventory.read", "POST", "/v1/inventory/issue-claims")
}

test_credit_reservation_routes_are_service_only if {
	data.duties["billing.credit-reservation.write"].service_only
	data.service_grants.receivables.billing == ["billing.invoice.read", "billing.credit-reservation.write"]
	allowed("billing.credit-reservation.write", "POST", "/v1/billing/invoices/i1/credit-reservations")
	allowed("billing.credit-reservation.write", "POST", "/v1/billing/credit-reservations/r1:commit")
	allowed("billing.credit-reservation.write", "POST", "/v1/billing/credit-reservations/r1:release")
	not allowed("billing.credit-reservation.write", "POST", "/v1/billing/credit-notes")
	every duty in ["billing.invoice.maintain", "billing.invoice.post", "billing.creditnote.post", "billing.invoice.view"] {
		not allowed(duty, "POST", "/v1/billing/invoices/i1/credit-reservations")
		not allowed(duty, "POST", "/v1/billing/credit-reservations/r1:commit")
		not allowed(duty, "POST", "/v1/billing/credit-reservations/r1:release")
	}
}

test_billing_sales_report_holds_only_the_service_only_invoice_duty if {
	data.service_principals["billing-sales-report"] == ["sales.order.record-invoice"]
	data.duties["sales.order.record-invoice"].service_only
	"sales.order.record-invoice" in data.service_grants.billing.sales
	not "sales.order.record-invoice" in data.service_grants.fulfillment.sales
	allowed("sales.order.record-invoice", "POST", "/v1/sales/orders/o1:record-progress")
	not allowed("sales.order.record-invoice", "PATCH", "/v1/sales/orders/o1")
	not allowed("sales.order.record-invoice", "POST", "/v1/sales/orders/o1:confirm")
	not allowed("sales.order.record-invoice", "POST", "/v1/sales/orders/o1:cancel")
	not allowed("sales.order.record-invoice", "GET", "/v1/sales/order-progress")
	not allowed("sales.order.read", "POST", "/v1/sales/orders/o1:record-progress")
	not allowed("billing.invoice.post", "POST", "/v1/sales/orders/o1:record-progress")
}

test_billing_sales_report_operator_routes_split_view_and_post if {
	allowed("billing.invoice.view", "GET", "/v1/billing/sales-reports")
	allowed("billing.invoice.post", "POST", "/v1/billing/sales-reports/r1:retry")
	not allowed("billing.invoice.view", "POST", "/v1/billing/sales-reports/r1:retry")
	not allowed("billing.invoice.post", "GET", "/v1/billing/sales-reports")
	not allowed("billing.invoice.maintain", "POST", "/v1/billing/sales-reports/r1:retry")
	not allowed("billing.invoice.maintain", "GET", "/v1/billing/sales-reports")
	not allowed("billing.invoice.post", "POST", "/v1/billing/sales-reports/r1:post")
	not allowed("sales.order.record-invoice", "POST", "/v1/billing/sales-reports/r1:retry")
}

test_billing_tax_report_holds_only_the_tax_transaction_duty if {
	data.service_principals["billing-tax-report"] == ["tax.transaction.record"]
	data.duties["tax.transaction.record"].service_only
	"tax.transaction.record" in data.service_grants.billing.tax
	allowed("tax.transaction.record", "POST", "/v1/tax/transactions")
	not allowed("tax.transaction.record", "POST", "/v1/tax/settlements:run")
}

test_billing_tax_report_operator_routes_split_view_and_post if {
	allowed("billing.invoice.view", "GET", "/v1/billing/tax-reports")
	allowed("billing.invoice.post", "POST", "/v1/billing/tax-reports/r1:retry")
	not allowed("billing.invoice.view", "POST", "/v1/billing/tax-reports/r1:retry")
	not allowed("billing.invoice.post", "GET", "/v1/billing/tax-reports")
	not allowed("billing.invoice.maintain", "POST", "/v1/billing/tax-reports/r1:retry")
	not allowed("tax.transaction.record", "POST", "/v1/billing/tax-reports/r1:retry")
}

test_edi_source_read_duties_open_only_their_get_routes if {
	data.service_grants.edi == {
		"fulfillment": ["fulfillment.shipment.read"],
		"billing": ["billing.invoice.read"],
		"sales": ["sales.order.lookup", "sales.channel.intake"],
	}
	every duty in ["fulfillment.shipment.read", "billing.invoice.read", "sales.order.lookup", "sales.channel.intake"] {
		data.duties[duty].service_only
	}
	allowed("fulfillment.shipment.read", "GET", "/v1/fulfillment/shipments/s1")
	not allowed("fulfillment.shipment.read", "POST", "/v1/fulfillment/shipments/s1:confirm-pick")
	not allowed("fulfillment.shipment.read", "GET", "/v1/fulfillment/packing-slips/p1")
	allowed("billing.invoice.read", "GET", "/v1/billing/invoices/i1")
	not allowed("billing.invoice.read", "POST", "/v1/billing/invoices/i1:post")
	not allowed("billing.invoice.read", "GET", "/v1/billing/invoices/i1/trace")
	allowed("sales.order.lookup", "GET", "/v1/sales/orders/o1")
	not allowed("sales.order.lookup", "POST", "/v1/sales/orders/o1:confirm")
	allowed("edi.operate", "POST", "/edi/v1/partners/p1/outbound:from-source")
	allowed("edi.admin", "PUT", "/edi/v1/partners/p1/trade-profile")
	not allowed("edi.operate", "PUT", "/edi/v1/partners/p1/trade-profile")
}

test_edi_item_xref_and_order_posting_routes if {
	allowed("edi.admin", "POST", "/edi/v1/partners/p1/item-xrefs")
	allowed("edi.admin", "GET", "/edi/v1/partners/p1/item-xrefs")
	allowed("edi.admin", "PUT", "/edi/v1/partners/p1/item-xrefs/x1")
	allowed("edi.admin", "DELETE", "/edi/v1/partners/p1/item-xrefs/x1")
	allowed("edi.admin", "PUT", "/edi/v1/partners/p1/sales-defaults")
	not allowed("edi.operate", "POST", "/edi/v1/partners/p1/item-xrefs")
	not allowed("edi.operate", "PUT", "/edi/v1/partners/p1/sales-defaults")
	allowed("edi.operate", "POST", "/edi/v1/messages/m1:post")
	not allowed("edi.admin", "POST", "/edi/v1/messages/m1:post")
	allowed("sales.channel.intake", "POST", "/v1/sales/orders")
	not allowed("sales.channel.intake", "GET", "/v1/sales/orders/o1")
}

test_edi_transport_routes if {
	allowed("edi.admin", "PUT", "/edi/v1/partners/p1/transport")
	allowed("edi.admin", "GET", "/edi/v1/partners/p1/transport")
	not allowed("edi.operate", "PUT", "/edi/v1/partners/p1/transport")
	not allowed("edi.operate", "GET", "/edi/v1/partners/p1/transport")
	allowed("edi.operate", "POST", "/edi/v1/interchanges/i1:resend")
	not allowed("edi.admin", "POST", "/edi/v1/interchanges/i1:resend")
}

test_edi_partner_update_and_edifact_inbound_routes if {
	allowed("edi.admin", "PATCH", "/edi/v1/partners/p1")
	not allowed("edi.operate", "PATCH", "/edi/v1/partners/p1")
	not allowed("edi.admin", "PUT", "/edi/v1/partners/p1")
	not allowed("edi.admin", "DELETE", "/edi/v1/partners/p1")
	allowed("edi.operate", "POST", "/edi/v1/inbound/edifact")
	not allowed("edi.admin", "POST", "/edi/v1/inbound/edifact")
}

granted_duty_known(duty) if data.duties[duty]

test_service_principals_hold_only_known_unprivileged_duties if {
	every _, duties in data.service_principals {
		count(duties) > 0
		every duty in duties {
			granted_duty_known(duty)
			not duty in {"org.switcher.all-scope", "tenant.administer"}
		}
	}
}

test_service_grants_allow_only_their_declared_routes if {
	every _, targets in data.service_grants {
		every _, duties in targets {
			count(duties) > 0
			every duty in duties {
				granted_duty_known(duty)
			}
		}
	}
	allowed("ledger.journal.post", "POST", "/v1/ledger/journal-entries")
	allowed("ledger.journal.reverse", "POST", "/v1/ledger/journal-entries/je-1/reverse")
	allowed("period.check", "POST", "/v1/close/period-statuses:check")
	allowed("period.check", "GET", "/v1/close/periods:resolve")
	not allowed("period.check", "POST", "/v1/close/periods:resolve")
	allowed("period.maintain", "PUT", "/v1/close/periods/p-1")
	allowed("period.maintain", "GET", "/v1/close/periods")
	allowed("period.maintain", "PATCH", "/v1/close/periods/p-1/modules/gl")
	not allowed("period.maintain", "POST", "/v1/close/periods/p-1/modules/gl:request-reopen")
	allowed("period.reopen", "POST", "/v1/close/periods/p-1/modules/gl:request-reopen")
	allowed("period.reopen", "POST", "/v1/close/reopen-requests/r-1:approve")
	allowed("close.checklist.write", "POST", "/v1/close/runs")
	allowed("close.checklist.read", "GET", "/v1/close/runs/r-1/tasks")
	allowed("close.allocation.run", "POST", "/v1/close/allocation-runs")
	allowed("close.allocation.run", "POST", "/v1/close/allocation-runs/r-1:post")
	not allowed("close.allocation.write", "POST", "/v1/close/allocation-runs")
	allowed("close.accrual.run", "POST", "/v1/close/accrual-schemes/rent:apply")
	allowed("close.accrual.run", "POST", "/v1/close/accrual-runs/r-1:cancel")
	not allowed("close.accrual.write", "POST", "/v1/close/accrual-schemes/rent:apply")
	not allowed("close.accrual.write", "POST", "/v1/close/accrual-runs/r-1:cancel")
	not allowed("close.accrual.run", "PUT", "/v1/close/accrual-schemes/rent")
	allowed("close.regrouping.run", "POST", "/v1/close/regroupings")
	data.service_grants.ledger.fx == ["fx.rate.lookup"]
	allowed("fx.rate.lookup", "GET", "/v1/fx/rates")
	not allowed("fx.rate.lookup", "POST", "/v1/fx/rates")
	allowed("ledger.ledger.write", "PATCH", "/v1/ledger/ledgers/l-1")
	not allowed("ledger.ledger.write", "DELETE", "/v1/ledger/ledgers/l-1")
	not allowed("period.check", "PUT", "/v1/close/periods/p-1")
	allowed("ledger.account.lookup", "GET", "/v1/ledger/charts/c1/accounts")
	not allowed("ledger.account.lookup", "PUT", "/v1/ledger/charts/c1/accounts/1000")
	not allowed("ledger.account.lookup", "GET", "/v1/ledger/journal-lines")
	data.duties["ledger.account.lookup"].service_only
	data.service_grants.close.ledger == ["ledger.ledger.lookup", "ledger.entry.post", "ledger.journal.reverse", "ledger.balance.read", "ledger.account.lookup", "ledger.period.postclosed"]
	every duty in data.service_grants.close.ledger {
		not allowed(duty, "POST", "/v1/ledger/ledgers")
		not allowed(duty, "POST", "/v1/ledger/balances:rebuild")
		not allowed(duty, "POST", "/v1/ledger/validate")
	}
	allowed("close.yearend.run", "POST", "/v1/close/year-end-runs")
	not allowed("close.yearend.run", "GET", "/v1/close/year-end-runs/r-1")
	allowed("close.yearend.read", "GET", "/v1/close/year-end-runs/r-1")
	not allowed("close.yearend.read", "POST", "/v1/close/year-end-runs")
	every duty in data.service_grants.receiving.ledger {
		not allowed(duty, "POST", "/v1/ledger/ledgers")
		not allowed(duty, "PUT", "/v1/ledger/charts/c1/accounts/1000")
		not allowed(duty, "POST", "/v1/ledger/balances:rebuild")
	}
	every duty in data.service_grants.payables.ledger {
		not allowed(duty, "POST", "/v1/ledger/journal-entries/je-1/reverse")
		not allowed(duty, "POST", "/v1/ledger/ledgers")
		not allowed(duty, "POST", "/v1/ledger/balances:rebuild")
	}
	umbrella := {"ledger.journal.post", "ledger.journal.read", "numbering.write"}
	every _, targets in data.service_grants {
		every _, duties in targets {
			every duty in duties {
				not duty in umbrella
			}
		}
	}
	data.service_grants.receiving.ledger == ["ledger.entry.post", "ledger.journal.reverse"]
	data.service_grants.payables.ledger == ["ledger.balance.read", "ledger.ledger.lookup", "ledger.entry.lookup", "ledger.entry.post"]
	data.service_grants.party.numbering == ["numbering.sequence.allocate"]
	data.service_grants.hr.numbering == ["numbering.sequence.allocate"]
	data.service_grants.fieldservice.dispatch == ["dispatch.run.create", "dispatch.run.read"]
	allowed("dispatch.run.create", "POST", "/v1/dispatch/runs")
	allowed("dispatch.run.read", "GET", "/v1/dispatch/runs/r1")
	not allowed("dispatch.run.create", "GET", "/v1/dispatch/runs/r1")
	not allowed("dispatch.run.read", "POST", "/v1/dispatch/runs")
	allowed("fieldservice.technician.dispatch", "POST", "/v1/service/dispatch/optimizations/o1:accept")
	not allowed("fieldservice.inquire", "POST", "/v1/service/dispatch/optimizations")
	data.service_grants.productcost.ledger == ["ledger.entry.post", "ledger.journal.reverse"]
	data.service_grants.productcost.inventory == ["inventory.read"]
	every grant in [data.service_grants.receiving.ledger, data.service_grants.payables.ledger] {
		every duty in grant {
			not allowed(duty, "POST", "/v1/ledger/validate")
			not allowed(duty, "POST", "/v1/ledger/journal-batches/b1/post")
			not allowed(duty, "POST", "/v1/ledger/ledger-sets/s1/journal-entries")
		}
	}
	allowed("ledger.journal.post", "POST", "/v1/ledger/journal-entries")
	not allowed("ledger.balance.read", "POST", "/v1/ledger/journal-entries")
	not allowed("ledger.journal.read", "POST", "/v1/ledger/journal-entries")
	data.service_grants["workflow-ts"].payables == ["payment-run.execute"]
	every duty in data.service_grants["workflow-ts"].payables {
		not allowed(duty, "POST", "/v1/payables/payment-proposals/p1:approve")
		not allowed(duty, "POST", "/v1/payables/payment-proposals")
		not allowed(duty, "GET", "/v1/payables/payment-runs/r1")
	}
}

service_granted(duty) if {
	some _, targets in data.service_grants
	some _, duties in targets
	duty in duties
}

test_service_only_duties_are_service_granted if {
	data.duties["procurement.invoicing-plan.settle"].service_only
	orphans := {d | data.duties[d].service_only; not service_granted(d)}
	orphans - awaiting_service_caller == set()
	every duty in awaiting_service_caller {
		data.duties[duty].service_only
		not service_granted(duty)
	}
}

awaiting_service_caller := {
	"costing.cost-transaction.post",
	"costing.ppv.post",
	"shopfloor.machine.ingest",
	"transport.tracking-event.ingest",
	"shopfloor.time.autoclockout",
	"inventory.transaction.apply",
	"pricing.contract.accrue",
	"hub.client.consume",
}

test_vendor_portal_duties_are_service_only_and_open_only_their_routes if {
	data.duties["procurement.vendor-portal.read"].service_only
	data.duties["procurement.vendor-portal.acknowledge"].service_only
	allowed("procurement.vendor-portal.read", "GET", "/v1/procurement/vendor/purchase-orders")
	allowed("procurement.vendor-portal.read", "GET", "/v1/procurement/vendor/purchase-orders/po-1")
	allowed("procurement.vendor-portal.acknowledge", "POST", "/v1/procurement/vendor/purchase-orders/po-1/acknowledgements")
	not allowed("procurement.vendor-portal.read", "POST", "/v1/procurement/vendor/purchase-orders/po-1/acknowledgements")
	not allowed("procurement.vendor-portal.read", "GET", "/v1/procurement/purchase-orders/po-1")
	not allowed("procurement.vendor-portal.read", "GET", "/v1/procurement/acknowledgements")
	not allowed("procurement.vendor-portal.acknowledge", "GET", "/v1/procurement/vendor/purchase-orders/po-1")
	not allowed("procurement.vendor-portal.acknowledge", "POST", "/v1/procurement/acknowledgements/a1:approve")
	not allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/vendor/purchase-orders")
	not allowed("procurement.purchase-order.confirm", "POST", "/v1/procurement/vendor/purchase-orders/po-1/acknowledgements")
	not allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/vendor/purchase-orders/po-1/acknowledgements")
}

# The supplier portal service (#511) acts for one vendor, and holds only vendor-portal duties on the services it calls.
test_supplier_portal_holds_only_service_only_vendor_duties if {
	every _, duties in data.service_grants["supplier-portal"] {
		every duty in duties {
			data.duties[duty].service_only
			contains(duty, "vendor-portal")
		}
	}
	data.service_grants["supplier-portal"].purchasing == ["procurement.vendor-portal.read", "procurement.vendor-portal.acknowledge"]
	data.service_grants["supplier-portal"].receiving == ["receiving.vendor-portal.asn", "receiving.vendor-portal.invoice"]
	data.service_grants["supplier-portal"].party == ["party.vendor-portal.read", "party.vendor-portal.write"]
}

test_receiving_vendor_portal_duties_open_only_vendor_routes if {
	allowed("receiving.vendor-portal.asn", "POST", "/v1/receiving/vendor/asns")
	allowed("receiving.vendor-portal.asn", "GET", "/v1/receiving/vendor/asns/a1")
	allowed("receiving.vendor-portal.invoice", "POST", "/v1/receiving/vendor/invoice-captures")
	allowed("receiving.vendor-portal.invoice", "GET", "/v1/receiving/vendor/invoices/i1")
	not allowed("receiving.vendor-portal.asn", "GET", "/v1/receiving/asns")
	not allowed("receiving.vendor-portal.asn", "POST", "/v1/receiving/asns/a1:start-receipt")
	not allowed("receiving.vendor-portal.asn", "POST", "/v1/receiving/vendor/invoice-captures")
	not allowed("receiving.vendor-portal.invoice", "GET", "/v1/receiving/invoices")
	not allowed("receiving.vendor-portal.invoice", "POST", "/v1/receiving/invoices:post")
	not allowed("receiving.asn.maintain", "POST", "/v1/receiving/vendor/asns")
	not allowed("receiving.vendor-invoice.post", "POST", "/v1/receiving/vendor/invoice-captures")
}

test_party_vendor_portal_duties_are_service_only_and_open_only_their_routes if {
	data.duties["party.vendor-portal.read"].service_only
	data.duties["party.vendor-portal.write"].service_only
	allowed("party.vendor-portal.read", "GET", "/v1/party/vendor-portal/requests")
	allowed("party.vendor-portal.read", "GET", "/v1/party/vendor-portal/documents/d1/content")
	allowed("party.vendor-portal.read", "GET", "/v1/party/vendor-portal/distributions/d1/content")
	allowed("party.vendor-portal.write", "POST", "/v1/party/vendor-portal/requests/r1:submit")
	allowed("party.vendor-portal.write", "POST", "/v1/party/vendor-portal/documents")
	allowed("party.vendor-portal.write", "POST", "/v1/party/vendor-portal/distributions/d1:acknowledge")
	not allowed("party.vendor-portal.read", "POST", "/v1/party/vendor-portal/documents")
	not allowed("party.vendor-portal.write", "GET", "/v1/party/vendor-portal/documents")
	not allowed("party.vendor-portal.read", "GET", "/v1/party/vendor-requests")
	not allowed("party.vendor-portal.write", "POST", "/v1/party/vendor-requests/r1:approve")
	not allowed("party.vendor-portal.write", "POST", "/v1/party/controlled-documents/c1:distribute")
}

test_party_vendor_requests_are_decided_by_their_own_duty if {
	allowed("party.vendor-request.decide", "POST", "/v1/party/vendor-requests/r1:approve")
	allowed("party.vendor-request.decide", "POST", "/v1/party/vendor-requests/r1:reject")
	not allowed("party.approve", "POST", "/v1/party/vendor-requests/r1:approve")
	not allowed("party.write", "POST", "/v1/party/vendor-requests/r1:approve")
	allowed("party.write", "POST", "/v1/party/controlled-documents/c1:distribute")
	allowed("party.read", "GET", "/v1/party/vendor-documents/d1/content")
	allowed("party.read", "GET", "/v1/party/vendor-requests")
	not allowed("party.read", "POST", "/v1/party/vendor-questionnaires")
}

test_buyer_acknowledgement_routes if {
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/acknowledgements")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/acknowledgements/awaiting")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/purchase-orders/po-1/acknowledgements")
	allowed("procurement.purchase-order.confirm", "POST", "/v1/procurement/acknowledgements/a1:approve")
	allowed("procurement.purchase-order.confirm", "POST", "/v1/procurement/acknowledgements/a1:decline")
	not allowed("procurement.purchase-order.inquire", "POST", "/v1/procurement/acknowledgements/a1:approve")
	not allowed("procurement.purchase-order.inquire", "POST", "/v1/procurement/acknowledgements/a1:decline")
	not allowed("procurement.purchase-order.confirm", "POST", "/v1/procurement/acknowledgements/a1:release")
	not allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/acknowledgements/a1:approve")
	not allowed("procurement.purchase-order.confirm", "GET", "/v1/procurement/acknowledgements")
}

test_internal_only_duties_are_service_only if {
	internal_only := {d |
		some d, spec in data.duties
		count(spec.permissions) > 0
		every permission in spec.permissions {
			startswith(permission.resource, "/internal/")
		}
	}
	count(internal_only) > 0
	every duty in internal_only {
		data.duties[duty].service_only == true
	}
}

test_service_grants_hold_no_flag_or_all_scope_duty_except_close_postclosed if {
	every caller, targets in data.service_grants {
		every target, duties in targets {
			every duty in duties {
				flag_grant_permitted(caller, target, duty)
				duty != "org.switcher.all-scope"
			}
		}
	}
}

flag_grant_permitted(caller, target, duty) if not data.duties[duty].flag

flag_grant_permitted("close", "ledger", "ledger.period.postclosed")

test_only_close_may_post_into_a_closed_period if {
	data.duties["ledger.period.postclosed"].flag
	granted := {caller |
		some caller, targets in data.service_grants
		"ledger.period.postclosed" in targets.ledger
	}
	granted == {"close"}
	count(data.duties["ledger.period.postclosed"].permissions) == 0
}

test_receiving_purchasing_grant_reaches_only_the_plan_row_settlement_route if {
	data.service_grants.receiving.purchasing == ["procurement.invoicing-plan.settle", "procurement.purchase-order.lookup"]
	allowed("procurement.invoicing-plan.settle", "PUT", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement")
	not allowed("procurement.invoicing-plan.settle", "GET", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement")
	not allowed("procurement.invoicing-plan.settle", "PUT", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan")
	not allowed("procurement.invoicing-plan.settle", "DELETE", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan")
	not allowed("procurement.invoicing-plan.settle", "PATCH", "/v1/procurement/purchase-orders/po-1")
	not allowed("procurement.invoicing-plan.settle", "POST", "/v1/procurement/purchase-orders")
	not allowed("procurement.invoicing-plan.settle", "POST", "/v1/ledger/journal-entries")
	not allowed("procurement.purchase-order.maintain", "PUT", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement")
}

test_service_only_read_duties_are_not_offered_as_the_missing_duty if {
	every duty in ["procurement.purchase-order.lookup", "inventory.ownership-change.read", "fx.rate.lookup"] {
		data.duties[duty].service_only
	}
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/fx/rates",
	}
		with data.assignments as {"tenant-a": {"user-a": []}}
	result.duty_missing == "fx.rate.view"
	result2 := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/procurement/purchase-orders/po-1/invoicing-plans",
	}
		with data.assignments as {"tenant-a": {"user-a": []}}
	result2.duty_missing == "procurement.purchase-order.inquire"
	result3 := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement",
	}
		with data.assignments as {"tenant-a": {"user-a": []}}
	result3.duty_missing == "procurement.invoicing-plan.settle"
}

test_receiving_purchasing_lookup_grant_reaches_only_the_order_and_plan_reads if {
	"procurement.purchase-order.lookup" in data.service_grants.receiving.purchasing
	allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/purchase-orders/po-1")
	allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/purchase-orders/po-1/invoicing-plans")
	not allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/purchase-orders")
	not allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/purchase-orders/po-1/versions")
	not allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan")
	not allowed("procurement.purchase-order.lookup", "GET", "/v1/procurement/scheduling-agreements")
	not allowed("procurement.purchase-order.lookup", "POST", "/v1/procurement/purchase-orders")
	not allowed("procurement.purchase-order.lookup", "PATCH", "/v1/procurement/purchase-orders/po-1")
	not allowed("procurement.purchase-order.lookup", "POST", "/v1/procurement/purchase-orders/po-1:approve")
	not allowed("procurement.purchase-order.lookup", "PUT", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement")
	not allowed("procurement.purchase-order.lookup", "POST", "/v1/ledger/journal-entries")
	not allowed("procurement.invoicing-plan.settle", "GET", "/v1/procurement/purchase-orders/po-1")
}

test_receiving_inventory_grant_reaches_only_journal_reads if {
	data.service_grants.receiving.inventory == ["inventory.ownership-change.read"]
	allowed("inventory.ownership-change.read", "GET", "/v1/inventory/journals")
	allowed("inventory.ownership-change.read", "GET", "/v1/inventory/journals/j-1")
	not allowed("inventory.ownership-change.read", "POST", "/v1/inventory/journals")
	not allowed("inventory.ownership-change.read", "POST", "/v1/inventory/journals/j-1:post")
	not allowed("inventory.ownership-change.read", "POST", "/v1/inventory/journals/j-1:reverse")
	not allowed("inventory.ownership-change.read", "GET", "/v1/inventory/onhand")
	not allowed("inventory.ownership-change.read", "GET", "/v1/inventory/transactions")
	not allowed("inventory.ownership-change.read", "GET", "/v1/inventory/consignment/stock")
	not allowed("inventory.ownership-change.read", "POST", "/v1/ledger/journal-entries")
}

test_payables_fx_grant_reaches_only_rate_lookup if {
	data.service_grants.payables.fx == ["fx.rate.lookup"]
	allowed("fx.rate.lookup", "GET", "/v1/fx/rates")
	not allowed("fx.rate.lookup", "POST", "/v1/fx/rates")
	not allowed("fx.rate.lookup", "GET", "/v1/fx/rates/history")
	not allowed("fx.rate.lookup", "GET", "/v1/fx/rate-types")
	not allowed("fx.rate.lookup", "POST", "/v1/fx/convert")
	not allowed("fx.rate.lookup", "POST", "/v1/fx/providers/ecb:import")
	not allowed("fx.rate.lookup", "POST", "/v1/ledger/journal-entries")
}

test_batch_service_grant_reaches_only_the_product_job_routes if {
	data.service_grants.batch.product == ["product.variants.explode", "product.released.read", "product.jobs.write"]
	allowed("product.released.read", "GET", "/v1/product/released-products")
	allowed("product.variants.explode", "POST", "/v1/product/products/p-1/variants:explode")
	allowed("product.jobs.write", "POST", "/v1/product/jobs/group-change")
	every duty in data.service_grants.batch.product {
		not allowed(duty, "POST", "/v1/product/products")
		not allowed(duty, "POST", "/v1/product/releases")
		not allowed(duty, "PUT", "/v1/product/released-products/r-1")
		not allowed(duty, "POST", "/v1/ledger/journal-entries")
	}
	not allowed("product.released.read", "POST", "/v1/product/jobs/group-change")
	not allowed("product.variants.explode", "GET", "/v1/product/released-products")
	not allowed("product.jobs.write", "GET", "/v1/product/released-products")
}

test_workflow_ts_eco_and_capa_service_duties if {
	data.service_grants["workflow-ts"].engineering == ["engineering.eco.workflow"]
	data.service_grants["workflow-ts"].quality == ["quality.capa.execute"]
	data.duties["engineering.eco.workflow"].service_only
	data.duties["quality.capa.execute"].service_only
	every action in ["approve", "reject", "cancel", "release"] {
		allowed("engineering.eco.workflow", "POST", sprintf("/v1/engineering/ecos/e1:%s", [action]))
	}
	allowed("engineering.eco.approve", "POST", "/v1/engineering/ecos/e1:release")
	every action in ["approve", "reject", "cancel"] {
		not allowed("engineering.eco.approve", "POST", sprintf("/v1/engineering/ecos/e1:%s", [action]))
	}
	not allowed("engineering.eco.workflow", "POST", "/v1/engineering/ecos")
	not allowed("engineering.eco.workflow", "GET", "/v1/engineering/ecos/e1")
	not allowed("engineering.eco.workflow", "POST", "/v1/engineering/bom-versions")
	allowed("quality.capa.execute", "GET", "/v1/quality/nonconformances/n1")
	allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances/n1/corrections")
	allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances/n1:complete-step")
	allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances/n1:check-effectiveness")
	not allowed("quality.capa.execute", "GET", "/v1/quality/nonconformances")
	not allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances")
	not allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances/n1:approve")
	not allowed("quality.capa.execute", "POST", "/v1/quality/nonconformances/n1:refuse")
}

test_budget_plan_workflow_is_service_only_and_reaches_only_sign_off_and_reject if {
	data.duties["budget.plan.workflow"].service_only
	data.service_grants["workflow-ts"].budgeting == ["budget.plan.workflow"]
	allowed("budget.plan.workflow", "POST", "/v1/budget/plans/p1/stages/review:sign-off")
	allowed("budget.plan.workflow", "POST", "/v1/budget/plans/p1/stages/approve:sign-off")
	allowed("budget.plan.workflow", "POST", "/v1/budget/plans/p1:reject")
	not allowed("budget.plan.workflow", "GET", "/v1/budget/plans/p1")
	not allowed("budget.plan.workflow", "POST", "/v1/budget/plans")
	not allowed("budget.plan.workflow", "POST", "/v1/budget/plans/p1:submit")
	not allowed("budget.plan.workflow", "PUT", "/v1/budget/plans/p1/scenarios/s1/grid")
	not allowed("budget.plan.workflow", "POST", "/v1/budget/register-entries")
}

test_treasury_cash_desk_writes_and_reads_are_separate if {
	allowed("treasury.cashdesk.write", "POST", "/v1/treasury/cash-desks")
	allowed("treasury.cashdesk.write", "POST", "/v1/treasury/cash-desks/d1/days")
	allowed("treasury.cashdesk.write", "POST", "/v1/treasury/cash-desks/d1/days/2026-11-10:close")
	allowed("treasury.cashdesk.write", "POST", "/v1/treasury/cash-desks/d1/entries")
	not allowed("treasury.cashdesk.write", "GET", "/v1/treasury/cash-desks")
	allowed("treasury.view", "GET", "/v1/treasury/cash-desks/d1/days/2026-11-10/entries")
	allowed("treasury.view", "GET", "/v1/treasury/cash-desks/d1/days")
	not allowed("treasury.view", "POST", "/v1/treasury/cash-desks/d1/entries")
	not allowed("treasury.cashdesk.write", "POST", "/v1/treasury/checks")
}

test_treasury_forecast_reads_open_items_only if {
	data.duties["payables.forecast.read"].service_only
	data.duties["receivables.forecast.read"].service_only
	data.service_grants.treasury.payables == ["payables.forecast.read"]
	data.service_grants.treasury.receivables == ["receivables.forecast.read"]
	allowed("payables.forecast.read", "GET", "/v1/payables/open-items")
	allowed("receivables.forecast.read", "GET", "/v1/receivables/open-items")
	not allowed("payables.forecast.read", "POST", "/v1/payables/open-items/o1:hold")
	not allowed("payables.forecast.read", "GET", "/v1/payables/payment-proposals")
	not allowed("receivables.forecast.read", "POST", "/v1/receivables/payments")
}

test_budget_plan_generation_needs_the_maintain_duty if {
	allowed("budget.plan.maintain", "POST", "/v1/budget/plans/p1/scenarios/s1:generate")
	not allowed("budget.view", "POST", "/v1/budget/plans/p1/scenarios/s1:generate")
	not allowed("budget.plan.workflow", "POST", "/v1/budget/plans/p1/scenarios/s1:generate")
}

test_budget_conversion_is_service_only_and_reaches_only_the_convert_routes if {
	data.duties["budget.encumbrance.convert"].service_only
	data.service_grants.purchasing.budgeting == ["budget.encumbrance.convert"]
	allowed("budget.encumbrance.convert", "POST", "/v1/budget/encumbrances/e1:convert")
	allowed("budget.encumbrance.convert", "POST", "/v1/budget/encumbrances:convert")
	not allowed("budget.encumbrance.convert", "POST", "/v1/budget/checks")
	not allowed("budget.encumbrance.convert", "POST", "/v1/budget/encumbrances/e1:release")
	not allowed("budget.check", "POST", "/v1/budget/encumbrances/e1:convert")
	not allowed("budget.view", "POST", "/v1/budget/encumbrances/e1:convert")
}

test_budget_view_reads_the_scenario_comparison_and_cannot_write_it if {
	allowed("budget.view", "GET", "/v1/budget/plans/p1/scenarios:compare")
	not allowed("budget.view", "POST", "/v1/budget/plans/p1/scenarios:compare")
	not allowed("budget.plan.workflow", "GET", "/v1/budget/plans/p1/scenarios:compare")
}

test_budget_plan_workflow_routes_are_refused_to_every_person_duty if {
	routes := [
		"/v1/budget/plans/p1/stages/review:sign-off",
		"/v1/budget/plans/p1/stages/approve:sign-off",
		"/v1/budget/plans/p1:reject",
	]
	every route in routes {
		granting := granting_duties with input as {"action": "POST", "resource": route}
		granting == {"budget.plan.workflow"}
		person := person_duties with input as {"action": "POST", "resource": route}
		person == set()
		every duty in ["budget.plan.maintain", "budget.plan.decide", "budget.view", "budget.maintain", "budget.override"] {
			not allowed(duty, "POST", route)
		}
	}
}

test_budget_plan_decide_routes_are_the_workflow_ts_decision_routes_only if {
	not data.duties["budget.plan.decide"].service_only
	allowed("budget.plan.decide", "POST", "/v1/workflows/budget-plans/p1/stages/review:sign-off")
	allowed("budget.plan.decide", "POST", "/v1/workflows/budget-plans/p1/stages/approve:sign-off")
	allowed("budget.plan.decide", "POST", "/v1/workflows/budget-plans/p1:reject")
	not allowed("budget.plan.decide", "GET", "/v1/workflows/budget-plans/p1")
	not allowed("budget.plan.decide", "POST", "/v1/workflows/budget-plans/p1:submit")
	not allowed("budget.plan.decide", "POST", "/v1/workflows/budget-plans/p1/stages/final:sign-off/extra")
	not allowed("budget.plan.decide", "POST", "/v1/budget/plans/p1/stages/approve:sign-off")
	not allowed("budget.plan.decide", "POST", "/v1/budget/plans/p1:reject")
	not allowed("budget.plan.decide", "POST", "/v1/workflows/eco-approvals/e1:approve")
	not allowed("budget.plan.maintain", "POST", "/v1/workflows/budget-plans/p1/stages/approve:sign-off")
	not allowed("budget.plan.maintain", "POST", "/v1/workflows/budget-plans/p1:reject")
}

test_workflow_ts_transport_service_duty if {
	data.service_grants["workflow-ts"].transport == ["transport.tender.execute"]
	data.duties["transport.tender.execute"].service_only
	allowed("transport.tender.execute", "POST", "/v1/transport/loads/l1:tender")
	allowed("transport.tender.execute", "GET", "/v1/transport/tenders/t1")
	allowed("transport.tender.execute", "POST", "/v1/transport/tenders:expire")
	allowed("transport.tender.execute", "GET", "/v1/transport/tenders")
	not allowed("transport.tender.execute", "POST", "/v1/transport/tenders/t1:respond")
	not allowed("transport.tender.execute", "POST", "/v1/transport/tracking-events")
	not allowed("transport.tender.execute", "POST", "/v1/transport/freight-bills")
	not allowed("transport.tender.execute", "POST", "/v1/transport/loads/l1:release")
}

# #426: a planner releases a load for tendering under the human tender duty; workflow-ts then starts the cascade.
test_transport_load_release_is_the_human_tender_duty if {
	allowed("transport.load.tender", "POST", "/v1/transport/loads/l1:release")
	not allowed("transport.load.tender", "GET", "/v1/transport/loads/l1:release")
	not allowed("transport.load.plan", "POST", "/v1/transport/loads/l1:release")
	not allowed("transport.tender.respond", "POST", "/v1/transport/loads/l1:release")
}

test_workflow_ts_party_decision_grant if {
	data.service_grants["workflow-ts"].party == ["party.approve", "party.merge.execute"]
	not data.duties["party.approve"].service_only
	allowed("party.approve", "POST", "/v1/party/pending-changes/c1:approve")
	allowed("party.approve", "POST", "/v1/party/pending-changes/c1:reject")
	not allowed("party.approve", "GET", "/v1/party/pending-changes/c1")
	not allowed("party.approve", "POST", "/v1/party/customers/c1/credit-limit")
	not allowed("party.approve", "PUT", "/v1/party/legal-entities/le1/protected-fields")
	not allowed("party.approve", "GET", "/v1/party/vendors/v1/bank-accounts:reveal")
}

# #450: a merge is requested (the gateway opens :merge under party.write, party checks the party.merge flag),
# approved by another person through workflow-ts (party.merge.approve), and executed by workflow-ts alone
# (service-only party.merge.execute). Payment approvers and party-change approvers cannot reach merge approval.
test_party_merge_goes_through_a_separate_approver if {
	allowed("party.write", "POST", "/v1/party/parties/p1:merge")
	not allowed("party.merge", "POST", "/v1/party/parties/p1:merge")
	not allowed("party.merge", "POST", "/v1/workflows/party-merges/m1:approve")
	not allowed("party.merge", "POST", "/v1/party/merge-requests/m1:approve")
	not allowed("party.write", "POST", "/v1/workflows/party-merges/m1:approve")
	not allowed("party.write", "POST", "/v1/party/merge-requests/m1:approve")
	allowed("party.read", "GET", "/v1/party/merge-requests/m1")
	not allowed("party.read", "POST", "/v1/party/merge-requests/m1:approve")

	not data.duties["party.merge.approve"].service_only
	allowed("party.merge.approve", "POST", "/v1/workflows/party-merges/m1:approve")
	allowed("party.merge.approve", "POST", "/v1/workflows/party-merges/m1:reject")
	allowed("party.merge.approve", "POST", "/v1/workflows/party-merges/m1:delegate")
	not allowed("party.merge.approve", "POST", "/v1/party/merge-requests/m1:approve")
	not allowed("party.merge.approve", "POST", "/v1/party/parties/p1:merge")
	not allowed("party.merge.approve", "POST", "/v1/payables/payment-proposals/p1:approve")
	not allowed("party.merge.approve", "POST", "/v1/payables/payment-runs")

	data.duties["party.merge.execute"].service_only
	allowed("party.merge.execute", "POST", "/v1/party/merge-requests/m1:approve")
	allowed("party.merge.execute", "POST", "/v1/party/merge-requests/m1:reject")
	not allowed("party.merge.execute", "GET", "/v1/party/merge-requests/m1")
	not allowed("party.merge.execute", "POST", "/v1/party/parties/p1:merge")
	not allowed("party.merge.execute", "POST", "/v1/workflows/party-merges/m1:approve")

	not allowed("payment-proposal.approve", "POST", "/v1/workflows/party-merges/m1:approve")
	not allowed("payment-run.execute", "POST", "/v1/workflows/party-merges/m1:approve")
	not allowed("party.approve", "POST", "/v1/workflows/party-merges/m1:approve")
	not allowed("party.approve", "POST", "/v1/party/merge-requests/m1:approve")
}

test_party_merge_execution_hint_names_no_person_duty if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/party/merge-requests/m1:approve",
	}
		with data.assignments as {"tenant-a": {"user-a": ["party.write"]}}
	result == {"allow": false, "duty_missing": "party.merge.execute"}
}

principal_allowed(name, method, resource) if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": sprintf("service:%s", [name])},
		"action": method,
		"resource": resource,
	}
		with data.assignments as {"tenant-a": {sprintf("service:%s", [name]): data.service_principals[name]}}
	result.allow
}

test_migration_service_grant_reaches_only_the_import_routes if {
	data.service_grants.migration == {
		"party": ["party.import.write", "party.import.read"],
		"product": ["product.import.write", "product.import.read"],
		"receivables": ["receivables.import.write", "receivables.import.read"],
		"engineering": ["engineering.import.write", "engineering.import.read"],
	}
	every _, duties in data.service_grants.migration {
		every duty in duties {
			data.duties[duty].service_only
		}
	}
	allowed("receivables.import.write", "POST", "/v1/receivables/open-items:batch")
	not allowed("receivables.import.write", "GET", "/v1/receivables/open-items")
	not allowed("receivables.import.write", "POST", "/v1/receivables/payments")
	not allowed("receivables.import.write", "POST", "/v1/receivables/open-items/o1:dispute")
	allowed("party.import.write", "POST", "/v1/party/parties")
	allowed("party.import.write", "PATCH", "/v1/party/parties/p1")
	allowed("party.import.write", "POST", "/v1/party/parties/p1/vendor")
	allowed("party.import.write", "POST", "/v1/party/parties/p1/customer")
	not allowed("party.import.write", "GET", "/v1/party/parties/p1")
	not allowed("party.import.write", "GET", "/v1/party/parties/p1/pii")
	not allowed("party.import.write", "DELETE", "/v1/party/parties/p1")
	not allowed("party.import.write", "PUT", "/v1/party/parties/p1")
	not allowed("party.import.write", "PATCH", "/v1/party/parties/p1/vendor")
	not allowed("party.import.write", "POST", "/v1/party/parties/p1:merge")
	not allowed("party.import.write", "POST", "/v1/party/tax-registrations:validate")
	not allowed("party.import.write", "POST", "/v1/party/customers/c1/credit-limit")
	not allowed("party.import.write", "POST", "/v1/party/vendors/v1/bank-accounts")
	not allowed("party.import.write", "GET", "/v1/party/vendors/v1/bank-accounts")
	not allowed("party.import.write", "POST", "/v1/product/products")
	allowed("product.import.write", "POST", "/v1/product/products")
	allowed("product.import.write", "PATCH", "/v1/product/products/p1")
	allowed("product.import.write", "POST", "/v1/product/releases")
	not allowed("product.import.write", "GET", "/v1/product/products")
	not allowed("product.import.write", "GET", "/v1/product/products/p1")
	not allowed("product.import.write", "DELETE", "/v1/product/products/p1")
	not allowed("product.import.write", "POST", "/v1/product/products/p1/variants:explode")
	allowed("product.import.write", "POST", "/v1/product/products/p1/attributes")
	allowed("product.import.write", "POST", "/v1/product/products/p1/identifiers")
	allowed("product.import.write", "POST", "/v1/product/products/p1/relations")
	allowed("product.import.write", "PATCH", "/v1/product/released-products/r1")
	not allowed("product.import.write", "PUT", "/v1/product/released-products/r1")
	not allowed("product.import.write", "PUT", "/v1/product/released-products/r1/site-defaults/s1")
	not allowed("product.import.write", "GET", "/v1/product/products/p1/attributes")
	not allowed("product.import.write", "DELETE", "/v1/product/products/p1/relations")
	not allowed("product.import.write", "POST", "/v1/product/jobs/group-change")
	not allowed("product.import.write", "POST", "/v1/product/gtins")
	not allowed("product.import.write", "POST", "/v1/party/parties")
	allowed("party.import.read", "GET", "/v1/party/parties/p1")
	allowed("party.import.read", "GET", "/v1/party/customers/c1")
	allowed("party.import.read", "GET", "/v1/party/vendors/v1")
	not allowed("party.import.read", "GET", "/v1/party/vendors/v1/bank-accounts")
	not allowed("party.import.read", "GET", "/v1/party/vendors/v1/bank-accounts:reveal")
	not allowed("party.import.read", "GET", "/v1/party/pending-changes/x1")
	not allowed("party.import.read", "POST", "/v1/party/parties")
	not allowed("party.import.read", "PATCH", "/v1/party/parties/p1")
	allowed("product.import.read", "GET", "/v1/product/products/p1")
	not allowed("product.import.read", "GET", "/v1/product/products")
	not allowed("product.import.read", "PATCH", "/v1/product/products/p1")
	allowed("receivables.import.read", "GET", "/v1/receivables/open-items/o1")
	not allowed("receivables.import.read", "GET", "/v1/receivables/open-items")
	not allowed("receivables.import.read", "POST", "/v1/receivables/open-items:batch")
	not allowed("receivables.import.read", "GET", "/v1/receivables/payments/p1")
	allowed("engineering.import.write", "POST", "/v1/engineering/bom-versions")
	allowed("engineering.import.write", "POST", "/v1/engineering/route-versions")
	not allowed("engineering.import.write", "POST", "/v1/engineering/bom-versions/b1:activate")
	not allowed("engineering.import.write", "PUT", "/v1/engineering/bom-versions/b1")
	not allowed("engineering.import.write", "POST", "/v1/engineering/route-versions/r1:activate")
	not allowed("engineering.import.write", "POST", "/v1/engineering/ecos")
	not allowed("engineering.import.write", "POST", "/v1/engineering/ebom-imports")
	allowed("engineering.import.read", "GET", "/v1/engineering/bom-versions/b1")
	allowed("engineering.import.read", "GET", "/v1/engineering/route-versions/r1")
	not allowed("engineering.import.read", "GET", "/v1/engineering/bom-versions")
	not allowed("engineering.import.read", "POST", "/v1/engineering/bom-versions")
	every _, duties in data.service_grants.migration {
		every duty in duties {
			not allowed(duty, "POST", "/v1/ledger/journal-entries")
			not allowed(duty, "POST", "/v1/sales/orders")
			not allowed(duty, "POST", "/v1/inventory/journals")
		}
	}
}

test_integration_hub_reaches_only_render_and_xref_record if {
	data.service_grants["integration-hub"] == {"migration": ["migration.outbound.render", "migration.xref.record"]}
	data.service_principals["integration-hub"] == ["migration.outbound.render", "migration.xref.record"]
	data.duties["migration.outbound.render"].service_only
	data.duties["migration.xref.record"].service_only
	allowed("migration.outbound.render", "POST", "/v1/integration/outbound-mappings/m1:render")
	allowed("migration.xref.record", "POST", "/v1/integration/xref:record")
	not allowed("migration.outbound.render", "POST", "/v1/integration/outbound-mappings")
	not allowed("migration.outbound.render", "GET", "/v1/integration/outbound-mappings/m1")
	not allowed("migration.xref.record", "GET", "/v1/integration/xref")
	not allowed("migration.xref.record", "POST", "/v1/integration/migration-runs")
	principal_allowed("integration-hub", "POST", "/v1/integration/xref:record")
	not principal_allowed("integration-hub", "POST", "/v1/receivables/open-items:batch")
}

test_migration_factory_grant_reaches_only_the_receivables_import if {
	data.service_grants["migration-factory"] == {"receivables": ["receivables.import.write", "receivables.view"], "payables": ["opening-item.import", "opening-item.import.read"], "ledger": ["ledger.entry.post", "ledger.balance.read", "ledger.ledger.lookup"]}
	allowed("receivables.import.write", "POST", "/v1/receivables/open-items:batch")
	allowed("receivables.view", "GET", "/v1/receivables/open-item-imports")
	not allowed("receivables.view", "POST", "/v1/receivables/open-items:batch")
}

test_opening_item_duties_are_service_only_and_cover_only_their_route if {
	allowed("opening-item.import", "POST", "/v1/payables/opening-batches")
	not allowed("opening-item.import", "GET", "/v1/payables/opening-batches")
	allowed("opening-item.import.read", "GET", "/v1/payables/opening-batches")
	not allowed("opening-item.import.read", "POST", "/v1/payables/opening-batches")
	not allowed("opening-item.import", "POST", "/v1/payables/payment-proposals")
}

test_migration_loader_principal_holds_only_the_import_duties if {
	data.service_principals["migration-loader"] == ["party.import.write", "party.import.read", "product.import.write", "product.import.read", "receivables.import.write", "receivables.import.read", "engineering.import.write", "engineering.import.read"]
	principal_allowed("migration-loader", "POST", "/v1/receivables/open-items:batch")
	not principal_allowed("migration-loader", "GET", "/v1/receivables/worklist")
	principal_allowed("migration-loader", "POST", "/v1/party/parties")
	principal_allowed("migration-loader", "PATCH", "/v1/party/parties/p1")
	principal_allowed("migration-loader", "POST", "/v1/product/products")
	principal_allowed("migration-loader", "POST", "/v1/product/releases")
	not principal_allowed("migration-loader", "GET", "/v1/party/parties/p1/pii")
	not principal_allowed("migration-loader", "DELETE", "/v1/party/parties/p1")
	not principal_allowed("migration-loader", "POST", "/v1/ledger/journal-entries")
	not principal_allowed("migration-loader", "GET", "/v1/ledger/balances")
	not principal_allowed("migration-loader", "POST", "/v1/sales/orders")
	not principal_allowed("migration-loader", "GET", "/v1/sales/orders/o1")
	not principal_allowed("migration-loader", "POST", "/v1/payables/payment-proposals")
}

test_shopfloor_edge_duties_open_only_their_routes if {
	not data.duties["shopfloor.edge.connect"].service_only
	allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/edge-nodes")
	allowed("shopfloor.edge.configure", "PUT", "/v1/shopfloor/edge-nodes/n1")
	allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/edge-nodes/n1/config-versions")
	not allowed("shopfloor.edge.configure", "GET", "/v1/shopfloor/edge-nodes/n1/config")
	not allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/edge-nodes/n1:heartbeat")
	not allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/machines")
	allowed("shopfloor.edge.connect", "GET", "/v1/shopfloor/edge-nodes/n1/config")
	allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1:heartbeat")
	not allowed("shopfloor.edge.connect", "GET", "/v1/shopfloor/edge-nodes")
	not allowed("shopfloor.edge.connect", "GET", "/v1/shopfloor/edge-nodes/n1")
	not allowed("shopfloor.edge.connect", "PUT", "/v1/shopfloor/edge-nodes/n1")
	not allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1/config-versions")
	not allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/machine-events")
	allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/edge-nodes/n1:enrollment-token")
	not allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1:enrollment-token")
	allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1:renew-certificate")
	not allowed("shopfloor.edge.configure", "POST", "/v1/shopfloor/edge-nodes/n1:renew-certificate")
	not allowed("shopfloor.view", "POST", "/v1/shopfloor/edge-nodes/n1:enrollment-token")
	not allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1:enroll")
	allowed("shopfloor.edge.connect", "GET", "/v1/shopfloor/edge-nodes/n1/control-recipes")
	allowed("shopfloor.edge.connect", "POST", "/v1/shopfloor/edge-nodes/n1/control-recipes/r1:report")
	not allowed("shopfloor.edge.configure", "GET", "/v1/shopfloor/edge-nodes/n1/control-recipes")
}

test_shopfloor_control_recipe_duties_open_only_their_routes if {
	allowed("shopfloor.controlrecipe.create", "POST", "/v1/shopfloor/control-recipes")
	allowed("shopfloor.controlrecipe.create", "POST", "/v1/shopfloor/control-recipes/r1:resend")
	not allowed("shopfloor.controlrecipe.create", "POST", "/v1/shopfloor/control-recipes/r1:report")
	not allowed("shopfloor.controlrecipe.create", "GET", "/v1/shopfloor/control-recipes")
	allowed("shopfloor.controlrecipe.execute", "POST", "/v1/shopfloor/control-recipes/r1:report")
	not allowed("shopfloor.controlrecipe.execute", "POST", "/v1/shopfloor/control-recipes")
	not allowed("shopfloor.controlrecipe.execute", "POST", "/v1/shopfloor/control-recipes/r1:resend")
	allowed("shopfloor.view", "GET", "/v1/shopfloor/control-recipes")
	allowed("shopfloor.view", "GET", "/v1/shopfloor/control-recipes/r1")
	not allowed("shopfloor.view", "POST", "/v1/shopfloor/control-recipes/r1:resend")
	allowed("shopfloor.view", "GET", "/v1/shopfloor/edge-nodes")
	allowed("shopfloor.view", "GET", "/v1/shopfloor/edge-nodes/n1")
	allowed("shopfloor.view", "GET", "/v1/shopfloor/edge-nodes/n1/config-versions")
	not allowed("shopfloor.view", "GET", "/v1/shopfloor/edge-nodes/n1/config")
	not allowed("shopfloor.view", "POST", "/v1/shopfloor/edge-nodes")
	not allowed("shopfloor.setup.maintain", "POST", "/v1/shopfloor/edge-nodes")
	not allowed("shopfloor.setup.maintain", "POST", "/v1/shopfloor/edge-nodes/n1/config-versions")
}

test_procurement_charge_routes_split_maintain_and_inquire if {
	every path in ["/v1/procurement/charge-codes", "/v1/procurement/auto-charges"] {
		allowed("procurement.policy.maintain", "POST", path)
		allowed("procurement.policy.maintain", "GET", path)
		allowed("procurement.purchase-order.inquire", "GET", path)
		not allowed("procurement.purchase-order.inquire", "POST", path)
		not allowed("procurement.purchase-order.maintain", "POST", path)
		allowed("procurement.policy.maintain", "PUT", sprintf("%s/c1", [path]))
		allowed("procurement.policy.maintain", "GET", sprintf("%s/c1", [path]))
		allowed("procurement.purchase-order.inquire", "GET", sprintf("%s/c1", [path]))
		not allowed("procurement.purchase-order.inquire", "PUT", sprintf("%s/c1", [path]))
		not allowed("procurement.policy.maintain", "DELETE", sprintf("%s/c1", [path]))
		not allowed("procurement.policy.maintain", "PATCH", sprintf("%s/c1", [path]))
	}
}

test_hub_duties_split_subscription_writes_from_delivery_reads if {
	allowed("hub.subscription.write", "POST", "/v1/hub/subscriptions")
	allowed("hub.subscription.write", "PATCH", "/v1/hub/subscriptions/s1")
	allowed("hub.subscription.write", "DELETE", "/v1/hub/subscriptions/s1")
	allowed("hub.subscription.write", "POST", "/v1/hub/subscriptions/s1:resume")
	allowed("hub.subscription.write", "POST", "/v1/hub/subscriptions/s1:replay")
	not allowed("hub.subscription.write", "GET", "/v1/hub/subscriptions")
	not allowed("hub.subscription.write", "GET", "/v1/hub/deliveries")
	not allowed("hub.subscription.write", "POST", "/v1/hub/subscriptions/s1:purge")
	allowed("hub.delivery.read", "GET", "/v1/hub/subscriptions")
	allowed("hub.delivery.read", "GET", "/v1/hub/subscriptions/s1")
	allowed("hub.delivery.read", "GET", "/v1/hub/deliveries")
	allowed("hub.delivery.read", "GET", "/v1/hub/dead-letters")
	not allowed("hub.delivery.read", "POST", "/v1/hub/subscriptions")
	not allowed("hub.delivery.read", "PATCH", "/v1/hub/subscriptions/s1")
	not allowed("hub.delivery.read", "POST", "/v1/hub/subscriptions/s1:replay")
	not allowed("hub.delivery.read", "GET", "/v1/hub/subscriptions/s1:replay")
}

test_drop_ship_routes_follow_release_view_and_vendor_confirm if {
	allowed("fulfillment.warehouse.release", "POST", "/v1/fulfillment/drop-ships")
	allowed("fulfillment.warehouse.release", "POST", "/v1/fulfillment/drop-ships/d1:cancel")
	not allowed("fulfillment.warehouse.release", "POST", "/v1/fulfillment/drop-ships/d1:confirm")
	allowed("procurement.purchase-order.confirm", "POST", "/v1/fulfillment/drop-ships/d1:confirm")
	not allowed("procurement.purchase-order.confirm", "POST", "/v1/fulfillment/drop-ships")
	not allowed("procurement.purchase-order.confirm", "POST", "/v1/fulfillment/drop-ships/d1:cancel")
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/drop-ships")
	allowed("fulfillment.warehouse.view", "GET", "/v1/fulfillment/drop-ships/d1")
	not allowed("fulfillment.warehouse.view", "POST", "/v1/fulfillment/drop-ships")
	not allowed("fulfillment.warehouse.view", "POST", "/v1/fulfillment/drop-ships/d1:confirm")
}

test_pricing_parameters_read_with_view_and_change_with_admin if {
	allowed("pricing.view", "GET", "/v1/pricing/parameters")
	not allowed("pricing.view", "PUT", "/v1/pricing/parameters")
	allowed("pricing.admin", "PUT", "/v1/pricing/parameters")
	not allowed("pricing.admin", "GET", "/v1/pricing/parameters")
	not allowed("pricing.admin", "POST", "/v1/pricing/parameters")
}

test_migration_connection_write_reaches_only_the_connection_profile_writes if {
	allowed("migration.connection.write", "POST", "/v1/integration/connection-profiles")
	allowed("migration.connection.write", "PATCH", "/v1/integration/connection-profiles/c1")
	allowed("migration.connection.write", "PUT", "/v1/integration/connection-profiles/c1/secret")
	allowed("migration.connection.write", "POST", "/v1/integration/connection-profiles/c1:test")
	not allowed("migration.connection.write", "GET", "/v1/integration/connection-profiles")
	not allowed("migration.connection.write", "GET", "/v1/integration/connection-profiles/c1")
	not allowed("migration.connection.write", "DELETE", "/v1/integration/connection-profiles/c1")
	not allowed("migration.connection.write", "PUT", "/v1/integration/connection-profiles/c1")
	not allowed("migration.connection.write", "POST", "/v1/integration/migration-runs")
}

test_migration_run_reads_connection_profiles_but_cannot_change_them if {
	allowed("migration.run", "GET", "/v1/integration/connection-profiles")
	allowed("migration.run", "GET", "/v1/integration/connection-profiles/c1")
	not allowed("migration.run", "POST", "/v1/integration/connection-profiles")
	not allowed("migration.run", "PATCH", "/v1/integration/connection-profiles/c1")
	not allowed("migration.run", "PUT", "/v1/integration/connection-profiles/c1/secret")
	not allowed("migration.run", "POST", "/v1/integration/connection-profiles/c1:test")
}

test_product_write_can_patch_products if {
	allowed("product.write", "PATCH", "/v1/product/products/p1")
	allowed("product.write", "POST", "/v1/product/products")
	not allowed("product.write", "PATCH", "/v1/product/released-products/r1")
	not allowed("product.write", "DELETE", "/v1/product/products/p1")
}

test_product_org_grant_reaches_only_org_read_routes if {
	data.service_grants.product.org == ["org.read"]
	allowed("org.read", "GET", "/v1/org/sites/s-1")
	allowed("org.read", "GET", "/v1/org/warehouses/w-1")
	not allowed("org.read", "POST", "/v1/org/sites")
	not allowed("org.read", "PUT", "/v1/org/sites/s-1")
	not allowed("org.read", "DELETE", "/v1/org/warehouses/w-1")
	not allowed("org.read", "POST", "/v1/org/warehouses")
	not allowed("org.read", "POST", "/v1/ledger/journal-entries")
	not allowed("org.read", "GET", "/v1/product/items")
}

test_party_write_reaches_customer_create_only_by_post if {
	allowed("party.write", "POST", "/v1/party/parties/p-1/customer")
	not allowed("party.write", "GET", "/v1/party/parties/p-1/customer")
	not allowed("party.write", "PUT", "/v1/party/parties/p-1/customer")
	not allowed("party.write", "DELETE", "/v1/party/parties/p-1/customer")
	not allowed("party.read", "POST", "/v1/party/parties/p-1/customer")
}

test_hub_subscription_write_reaches_test_send_and_delivery_replay if {
	allowed("hub.subscription.write", "POST", "/v1/hub/subscriptions/s-1:test")
	allowed("hub.subscription.write", "POST", "/v1/hub/deliveries/d-1:replay")
	not allowed("hub.subscription.write", "GET", "/v1/hub/subscriptions/s-1:test")
	not allowed("hub.subscription.write", "GET", "/v1/hub/deliveries/d-1:replay")
	not allowed("hub.subscription.write", "DELETE", "/v1/hub/deliveries/d-1")
	not allowed("hub.delivery.read", "POST", "/v1/hub/subscriptions/s-1:test")
	not allowed("hub.delivery.read", "POST", "/v1/hub/deliveries/d-1:replay")
}

test_tax_configuration_vat_and_intrastat_duties_reach_only_their_routes if {
	allowed("tax.config.read", "GET", "/v1/tax/jurisdictions")
	allowed("tax.config.read", "GET", "/v1/tax/groups/party")
	allowed("tax.config.read", "GET", "/v1/tax/vat-boxes")
	not allowed("tax.config.read", "PUT", "/v1/tax/codes/VAT19")
	allowed("tax.config.manage", "PUT", "/v1/tax/jurisdictions/US-OH")
	allowed("tax.config.manage", "DELETE", "/v1/tax/codes/VAT19")
	allowed("tax.config.manage", "PUT", "/v1/tax/groups/item/STANDARD")
	allowed("tax.config.manage", "PUT", "/v1/tax/vat-boxes")
	not allowed("tax.config.manage", "GET", "/v1/tax/vat-return")
	allowed("tax.vatreturn.read", "GET", "/v1/tax/vat-return")
	allowed("tax.vatreturn.read", "GET", "/v1/tax/esl")
	not allowed("tax.vatreturn.read", "PUT", "/v1/tax/vat-boxes")
	allowed("tax.intrastat.read", "GET", "/v1/tax/intrastat/master/commodity")
	allowed("tax.intrastat.read", "GET", "/v1/tax/intrastat/journals/j-1")
	not allowed("tax.intrastat.read", "GET", "/v1/tax/intrastat/journals/j-1/export")
	allowed("tax.intrastatmaster.manage", "PUT", "/v1/tax/intrastat/master/commodity/84137030")
	not allowed("tax.intrastatmaster.manage", "POST", "/v1/tax/intrastat/journals")
	allowed("tax.intrastat.manage", "POST", "/v1/tax/intrastat/journals")
	allowed("tax.intrastat.manage", "POST", "/v1/tax/intrastat/journals/j-1/validate")
	allowed("tax.intrastat.manage", "PUT", "/v1/tax/intrastat/journals/j-1/lines/2")
	not allowed("tax.intrastat.manage", "GET", "/v1/tax/intrastat/journals/j-1/export")
	allowed("tax.intrastat.export", "GET", "/v1/tax/intrastat/journals/j-1/export")
	not allowed("tax.intrastat.export", "POST", "/v1/tax/intrastat/journals/j-1/validate")
}

test_tax_duties_reach_only_their_routes if {
	allowed("tax.transaction.record", "POST", "/v1/tax/transactions")
	allowed("tax.settlement.run", "POST", "/v1/tax/settlements:run")
	allowed("tax.recommit.run", "POST", "/v1/tax/recommit:run")
	allowed("tax.saft.export", "GET", "/v1/tax/saft")
	not allowed("tax.saft.export", "POST", "/v1/tax/saft")
	allowed("tax.ledgerconfig.manage", "PUT", "/v1/tax/ledger-config")
	not allowed("tax.ledgerconfig.manage", "POST", "/v1/tax/settlements:run")
	not allowed("tax.settlement.run", "PUT", "/v1/tax/ledger-config")
	not allowed("tax.settlement.run", "POST", "/v1/tax/transactions")
	not allowed("tax.transaction.record", "POST", "/v1/tax/settlements:run")
	not allowed("tax.recommit.run", "POST", "/v1/tax/settlements:run")
	not allowed("tax.tax.calculate", "POST", "/v1/tax/settlements:run")
	data.service_grants.tax.ledger == ["ledger.balance.read", "ledger.entry.lookup", "ledger.ledger.lookup", "ledger.account.lookup"]
	not allowed("ledger.balance.read", "POST", "/v1/ledger/journal-entries")
}

test_hr_job_skill_routes_are_granted if {
	allowed("hr.org.inquire", "GET", "/v1/hr/jobs/j-1/skill-requirements")
	allowed("hr.org.inquire", "GET", "/v1/hr/jobs/j-1/skill-gaps")
	allowed("hr.org.maintain", "POST", "/v1/hr/jobs/j-1/skill-requirements")
	allowed("hr.org.maintain", "DELETE", "/v1/hr/jobs/j-1/skill-requirements/WELDING")
	not allowed("hr.org.inquire", "POST", "/v1/hr/jobs/j-1/skill-requirements")
	not allowed("hr.org.inquire", "DELETE", "/v1/hr/jobs/j-1/skill-requirements/WELDING")
	not allowed("hr.org.maintain", "GET", "/v1/hr/jobs/j-1/skill-gaps")
}

test_hr_performance_every_route_is_granted if {
	every duty in ["hr.performance.manage", "hr.performance.admin"] {
		allowed(duty, "GET", "/v1/hr/workers/w-1/goals")
		allowed(duty, "POST", "/v1/hr/workers/w-1/goals")
		allowed(duty, "POST", "/v1/hr/goals/g-1/progress")
		allowed(duty, "GET", "/v1/hr/workers/w-1/reviews")
		allowed(duty, "POST", "/v1/hr/workers/w-1/reviews")
		allowed(duty, "GET", "/v1/hr/reviews/r-1")
		allowed(duty, "POST", "/v1/hr/reviews/r-1/sign-off")
	}
	allowed("hr.performance.self", "GET", "/v1/hr/me/goals")
	allowed("hr.performance.self", "POST", "/v1/hr/me/goals/g-1/progress")
	allowed("hr.performance.self", "GET", "/v1/hr/me/reviews")
	allowed("hr.performance.self", "POST", "/v1/hr/me/reviews/r-1/self-assessment")
}

test_hr_performance_duties_reach_only_their_routes if {
	allowed("hr.performance.manage", "POST", "/v1/hr/workers/w-1/goals")
	allowed("hr.performance.manage", "POST", "/v1/hr/reviews/r-1/sign-off")
	allowed("hr.performance.admin", "GET", "/v1/hr/reviews/r-1")
	not allowed("hr.performance.manage", "POST", "/v1/hr/me/reviews/r-1/self-assessment")
	not allowed("hr.performance.manage", "DELETE", "/v1/hr/reviews/r-1")
	not allowed("hr.performance.manage", "GET", "/v1/hr/workers/w-1/pii")
	allowed("hr.performance.self", "POST", "/v1/hr/me/reviews/r-1/self-assessment")
	allowed("hr.performance.self", "GET", "/v1/hr/me/goals")
	not allowed("hr.performance.self", "POST", "/v1/hr/reviews/r-1/sign-off")
	not allowed("hr.performance.self", "GET", "/v1/hr/workers/w-1/reviews")
	not allowed("hr.performance.self", "POST", "/v1/hr/workers/w-1/goals")
	not allowed("hr.performance.self", "POST", "/v1/hr/goals/g-1/progress")
	not allowed("hr.performance.manage", "POST", "/v1/hr/me/goals/g-1/progress")
}

test_hr_comp_cycle_approval_is_segregated_from_comp_admin if {
	allowed("hr.compensation.approve", "POST", "/v1/hr/compensation/cycles/c-1/approve")
	not allowed("hr.compensation.approve", "POST", "/v1/hr/compensation/cycles/c-1/apply")
	not allowed("hr.compensation.approve", "POST", "/v1/hr/compensation/cycles")
	not allowed("hr.compensation.approve", "PUT", "/v1/hr/compensation/cycles/c-1/worksheets/m-1")
	not allowed("hr.compensation.approve", "POST", "/v1/hr/compensation/cycles/c-1/worksheets/m-1/submit")
	allowed("hr.compensation.admin", "POST", "/v1/hr/compensation/cycles/c-1/apply")
	not allowed("hr.compensation.admin", "POST", "/v1/hr/compensation/cycles/c-1/approve")
	not allowed("hr.team.approve", "POST", "/v1/hr/compensation/cycles/c-1/approve")
}

test_compliance_legal_hold_duties_reach_only_their_routes if {
	allowed("compliance.legal-hold.place", "POST", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.place", "GET", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.place", "POST", "/v1/privacy/holds/h-1/release")
	allowed("compliance.legal-hold.release", "POST", "/v1/privacy/holds/h-1/release")
	not allowed("compliance.legal-hold.release", "POST", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.release", "GET", "/v1/privacy/holds/h-1")
	allowed("compliance.legal-hold.read", "GET", "/v1/privacy/holds")
	allowed("compliance.legal-hold.read", "GET", "/v1/privacy/holds/h-1")
	not allowed("compliance.legal-hold.read", "GET", "/v1/privacy/holds:check")
	not allowed("compliance.legal-hold.read", "POST", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.read", "DELETE", "/v1/privacy/holds/h-1")
}

test_compliance_hold_check_is_service_only_and_granted_to_receiving if {
	allowed("compliance.legal-hold.check", "GET", "/v1/privacy/holds:check")
	not allowed("compliance.legal-hold.check", "GET", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.check", "POST", "/v1/privacy/holds")
	not allowed("compliance.legal-hold.check", "POST", "/v1/privacy/erasures")
	data.duties["compliance.legal-hold.check"].service_only
	data.service_grants.receiving.compliance == ["compliance.legal-hold.check"]
	data.service_principals["receiving-erasure"] == ["compliance.legal-hold.check"]
}

test_receiving_may_calculate_and_record_input_tax if {
	data.service_grants.receiving.tax == ["tax.tax.calculate", "tax.transaction.record"]
}

test_compliance_erasure_and_retention_duties if {
	allowed("compliance.erasure.request", "POST", "/v1/privacy/erasures")
	not allowed("compliance.erasure.request", "GET", "/v1/privacy/erasures")
	not allowed("compliance.erasure.request", "POST", "/v1/privacy/holds")
	allowed("compliance.retention.read", "GET", "/v1/privacy/retention-policies")
	not allowed("compliance.retention.read", "POST", "/v1/privacy/retention-policies")
	not allowed("compliance.retention.read", "POST", "/v1/privacy/erasures")
}

test_receiving_payee_erase_reaches_only_the_erase_route if {
	allowed("receiving.payee.erase", "POST", "/v1/receiving/one-time-payees:erase")
	not allowed("receiving.payee.erase", "POST", "/v1/receiving/one-time-payees")
	not allowed("receiving.payee.erase", "GET", "/v1/receiving/one-time-payees:erase")
	not allowed("receiving.vendor-invoice.post", "POST", "/v1/receiving/one-time-payees:erase")
}

test_treasury_reconciliation_write_reaches_only_its_post_routes if {
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-rules")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-rules/r-1:deactivate")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-runs")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-matches")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-matches/m-1:unmatch")
	not allowed("treasury.reconciliation.write", "GET", "/v1/treasury/reconciliation-rules")
	not allowed("treasury.reconciliation.write", "GET", "/v1/treasury/reconciliation-matches/m-1")
	not allowed("treasury.reconciliation.write", "DELETE", "/v1/treasury/reconciliation-rules/r-1")
	not allowed("treasury.reconciliation.write", "PUT", "/v1/treasury/reconciliation-rules/r-1:deactivate")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/reconciliation-matches/m-1")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-accounts")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/statements/import")
	not allowed("treasury.view", "POST", "/v1/treasury/reconciliation-runs")
	not allowed("treasury.view", "POST", "/v1/treasury/reconciliation-matches/m-1:unmatch")
	not allowed("treasury.view", "POST", "/v1/treasury/reconciliation-rules/r-1:deactivate")
	not allowed("treasury.statement.import", "POST", "/v1/treasury/reconciliation-runs")
}

test_treasury_cash_position_import_and_read_are_separate if {
	allowed("treasury.statement.import", "POST", "/v1/treasury/intraday-reports/import")
	not allowed("treasury.view", "POST", "/v1/treasury/intraday-reports/import")
	allowed("treasury.view", "GET", "/v1/treasury/cash-position")
	not allowed("treasury.statement.import", "GET", "/v1/treasury/cash-position")
}

test_treasury_bank_reconciliation_duties_are_distinct if {
	allowed("treasury.reconciliation.view", "GET", "/v1/treasury/bank-reconciliations")
	allowed("treasury.reconciliation.view", "GET", "/v1/treasury/bank-reconciliations/r-1")
	allowed("treasury.reconciliation.close", "POST", "/v1/treasury/bank-reconciliations/r-1:close")
	allowed("treasury.reconciliation.reopen", "POST", "/v1/treasury/bank-reconciliations/r-1:reopen")
	allowed("treasury.reconciliation.post", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:create-transaction")
	allowed("treasury.reconciliation.abandon", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:abandon")
	not allowed("treasury.reconciliation.post", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:abandon")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:abandon")
	not allowed("treasury.reconciliation.abandon", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:create-transaction")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1:set-balances")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:exclude")
	allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:include")
	not allowed("treasury.reconciliation.close", "POST", "/v1/treasury/bank-reconciliations/r-1:reopen")
	not allowed("treasury.reconciliation.reopen", "POST", "/v1/treasury/bank-reconciliations/r-1:close")
	not allowed("treasury.reconciliation.close", "GET", "/v1/treasury/bank-reconciliations/r-1")
	not allowed("treasury.reconciliation.close", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:create-transaction")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1:close")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1:reopen")
	not allowed("treasury.reconciliation.write", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:create-transaction")
	not allowed("treasury.reconciliation.post", "POST", "/v1/treasury/bank-reconciliations/r-1/lines/3:exclude")
	not allowed("treasury.reconciliation.view", "POST", "/v1/treasury/bank-reconciliations/r-1:close")
	not allowed("treasury.reconciliation.view", "DELETE", "/v1/treasury/bank-reconciliations/r-1")
	not allowed("treasury.view", "GET", "/v1/treasury/bank-reconciliations")
	not allowed("treasury.view", "POST", "/v1/treasury/bank-reconciliations/r-1:close")
}

# Treasury posts to the ledger and reads the open items of payables and receivables for the cash forecast, nothing else.
test_treasury_service_grants_are_the_ledger_post_and_the_forecast_reads if {
	data.service_grants.treasury == {
		"ledger": ["ledger.entry.post"],
		"payables": ["payables.forecast.read"],
		"receivables": ["receivables.forecast.read"],
	}
}

test_treasury_ebics_duties_split_ceremony_from_confirmation if {
	allowed("treasury.ebics.write", "POST", "/v1/treasury/ebics-subscribers")
	not allowed("treasury.ebics.write", "POST", "/v1/treasury/ebics-subscribers/s-1:send-ini")
	allowed("treasury.ebics.keys", "POST", "/v1/treasury/ebics-subscribers/s-1:send-ini")
	allowed("treasury.ebics.keys", "POST", "/v1/treasury/ebics-subscribers/s-1:send-hia")
	allowed("treasury.ebics.keys", "POST", "/v1/treasury/ebics-subscribers/s-1:fetch-bank-keys")
	not allowed("treasury.ebics.keys", "POST", "/v1/treasury/ebics-subscribers/s-1:confirm-bank-keys")
	not allowed("treasury.ebics.keys", "POST", "/v1/treasury/ebics-subscribers/s-1:unpark")
	allowed("treasury.ebics.approve", "POST", "/v1/treasury/ebics-subscribers/s-1:confirm-bank-keys")
	allowed("treasury.ebics.approve", "POST", "/v1/treasury/ebics-subscribers/s-1:unpark")
	not allowed("treasury.ebics.approve", "POST", "/v1/treasury/ebics-subscribers/s-1:send-ini")
	not allowed("treasury.ebics.approve", "POST", "/v1/treasury/ebics-subscribers")
	allowed("treasury.view", "GET", "/v1/treasury/ebics-subscribers/s-1")
	allowed("treasury.view", "GET", "/v1/treasury/ebics-subscribers/s-1/letters/INI")
	not allowed("treasury.view", "POST", "/v1/treasury/ebics-subscribers/s-1:confirm-bank-keys")
}

test_payroll_run_duties_split_maintain_finalize_post_and_read if {
	allowed("payroll.run.maintain", "POST", "/v1/payroll/runs")
	allowed("payroll.run.maintain", "POST", "/v1/payroll/runs/r-1/review")
	not allowed("payroll.run.maintain", "POST", "/v1/payroll/runs/r-1/finalize")
	not allowed("payroll.run.maintain", "POST", "/v1/payroll/runs/r-1/post")
	allowed("payroll.run.finalize", "POST", "/v1/payroll/runs/r-1/finalize")
	not allowed("payroll.run.finalize", "POST", "/v1/payroll/runs/r-1/post")
	allowed("payroll.run.post", "POST", "/v1/payroll/runs/r-1/post")
	not allowed("payroll.run.post", "POST", "/v1/payroll/runs")
	allowed("payroll.run.read", "GET", "/v1/payroll/runs/r-1")
	allowed("payroll.run.read", "GET", "/v1/payroll/gl-reconciliation")
	not allowed("payroll.run.read", "POST", "/v1/payroll/runs")
}

test_payroll_posting_profile_write_is_its_own_duty if {
	allowed("payroll.posting-profile.maintain", "PUT", "/v1/payroll/posting-profiles/le-1")
	not allowed("payroll.posting-profile.maintain", "GET", "/v1/payroll/posting-profiles/le-1")
	not allowed("payroll.posting-profile.maintain", "POST", "/v1/payroll/runs/r-1/post")
	not allowed("payroll.run.maintain", "PUT", "/v1/payroll/posting-profiles/le-1")
	not allowed("payroll.run.post", "PUT", "/v1/payroll/posting-profiles/le-1")
	allowed("payroll.run.read", "GET", "/v1/payroll/posting-profiles/le-1")
	not allowed("payroll.run.read", "PUT", "/v1/payroll/posting-profiles/le-1")
}

test_payroll_service_grant_reaches_only_ledger_post_balance_and_lookup if {
	data.service_grants.payroll.ledger == ["ledger.entry.post", "ledger.balance.read", "ledger.ledger.lookup"]
	count(data.service_grants.payroll) == 1
}

test_posting_accounts_are_maintained_by_costing_maintain_and_read_by_costing_view if {
	allowed("productcost.maintain", "PUT", "/v1/product-costing/posting-accounts/le-1")
	not allowed("productcost.view", "PUT", "/v1/product-costing/posting-accounts/le-1")
	not allowed("productcost.activate", "PUT", "/v1/product-costing/posting-accounts/le-1")
	allowed("productcost.view", "GET", "/v1/product-costing/posting-accounts/le-1")
	not allowed("productcost.maintain", "GET", "/v1/product-costing/posting-accounts/le-1")
}

test_activation_attempts_are_resolved_only_by_the_operator_duty if {
	allowed("productcost.activation-attempt.resolve", "GET", "/v1/product-costing/activation-attempts")
	allowed("productcost.activation-attempt.resolve", "POST", "/v1/product-costing/versions/v-1/activation-attempt/resolve")
	not allowed("productcost.activation-attempt.resolve", "POST", "/v1/product-costing/versions/v-1/activate")
	not allowed("productcost.activation-attempt.resolve", "PUT", "/v1/product-costing/posting-accounts/le-1")
	not allowed("productcost.activate", "GET", "/v1/product-costing/activation-attempts")
	not allowed("productcost.activate", "POST", "/v1/product-costing/versions/v-1/activation-attempt/resolve")
	not allowed("productcost.maintain", "POST", "/v1/product-costing/versions/v-1/activation-attempt/resolve")
	not allowed("productcost.view", "GET", "/v1/product-costing/activation-attempts")
}

test_shopfloor_andon_routes if {
	allowed("shopfloor.oee.rollup", "POST", "/v1/shopfloor/andon:sweep")
	allowed("shopfloor.check.override", "POST", "/v1/shopfloor/andon/a1:clear")
	not allowed("shopfloor.view", "POST", "/v1/shopfloor/andon/a1:clear")
	not allowed("shopfloor.check.override", "POST", "/v1/shopfloor/andon:sweep")
}

test_hub_client_routes_split_write_consume_and_read if {
	allowed("hub.client.write", "POST", "/v1/hub/clients")
	allowed("hub.client.write", "PATCH", "/v1/hub/clients/c1")
	allowed("hub.client.write", "POST", "/v1/hub/clients/c1:revoke")
	not allowed("hub.client.write", "POST", "/v1/hub/clients:consume")
	not allowed("hub.client.write", "GET", "/v1/hub/clients")
	allowed("hub.coexistence.operate", "POST", "/v1/hub/coexistence:retry-parked")
	not allowed("hub.subscription.write", "POST", "/v1/hub/coexistence:retry-parked")
	not allowed("hub.coexistence.operate", "GET", "/v1/hub/coexistence")
	allowed("hub.delivery.read", "GET", "/v1/hub/coexistence")
	allowed("hub.client.consume", "POST", "/v1/hub/clients:consume")
	not allowed("hub.client.consume", "POST", "/v1/hub/clients")
	data.duties["hub.client.consume"].service_only
	allowed("hub.delivery.read", "GET", "/v1/hub/clients")
	allowed("hub.delivery.read", "GET", "/v1/hub/clients/c1")
	allowed("hub.delivery.read", "GET", "/v1/hub/clients/c1/usage")
	not allowed("hub.delivery.read", "POST", "/v1/hub/clients:consume")
	not allowed("hub.delivery.read", "PATCH", "/v1/hub/clients/c1")
}

test_finreport_ledger_consumer_reads_only_journal_lines if {
	data.service_principals["finreport-ledger"] == ["ledger.entry.lookup"]
	data.service_grants.finreport == {"ledger": ["ledger.entry.lookup", "ledger.balance.read"]}
	data.duties["ledger.entry.lookup"].service_only
	allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-lines")
	not allowed("ledger.entry.lookup", "GET", "/v1/ledger/journal-entries/e1")
	allowed("ledger.balance.read", "GET", "/v1/ledger/balances")
}

test_reporting_library_share_links if {
	allowed("reporting.library.share", "GET", "/v1/reporting/library/f-1/share-links")
	allowed("reporting.library.share", "POST", "/v1/reporting/library/f-1/share-links")
	allowed("reporting.library.share", "POST", "/v1/reporting/share-links/l-1:revoke")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/library/f-1/share-links")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/share-links/l-1:revoke")
	not allowed("reporting.library.share", "GET", "/v1/reporting/shared/tok")
}

test_reporting_balance_verification_routes if {
	allowed("reporting.balance.verify", "POST", "/v1/reporting/balance-verifications")
	not allowed("reporting.balance.verify", "GET", "/v1/reporting/balance-verifications")
	allowed("reporting.audit.view", "GET", "/v1/reporting/balance-verifications")
	allowed("reporting.audit.view", "GET", "/v1/reporting/balance-verifications/v1")
	not allowed("reporting.audit.view", "POST", "/v1/reporting/balance-verifications")
}

test_org_demo_company_manage_reaches_only_the_demo_load if {
	allowed("org.demo-company.manage", "POST", "/v1/org/demo-company")
	not allowed("org.demo-company.manage", "GET", "/v1/org/demo-company")
	not allowed("org.demo-company.manage", "POST", "/v1/org/import")
	not allowed("org.package.manage", "POST", "/v1/org/demo-company")
	not allowed("org.read", "POST", "/v1/org/demo-company")
}

test_org_package_manage_reaches_only_export_and_import if {
	allowed("org.package.manage", "POST", "/v1/org/export")
	allowed("org.package.manage", "POST", "/v1/org/import")
	not allowed("org.package.manage", "GET", "/v1/org/legal-entities")
	not allowed("org.read", "POST", "/v1/org/export")
	not allowed("org.read", "POST", "/v1/org/import")
}

test_pricing_tenant_parameters_read_with_view_and_change_with_admin if {
	allowed("pricing.view", "GET", "/v1/pricing/tenant-parameters")
	not allowed("pricing.view", "PUT", "/v1/pricing/tenant-parameters")
	allowed("pricing.admin", "PUT", "/v1/pricing/tenant-parameters")
	not allowed("pricing.admin", "GET", "/v1/pricing/tenant-parameters")
}

test_pricing_cache_principal_holds_only_its_flag if {
	data.service_principals["pricing-cache"] == ["pricing.cache.invalidate"]
	not principal_allowed("pricing-cache", "GET", "/v1/pricing/parameters")
}

test_scheduling_run_compare_and_rollback_routes if {
	allowed("scheduling.read", "GET", "/v1/scheduling/runs/r1/compare")
	allowed("scheduling.run", "POST", "/v1/scheduling/runs/r1:rollback")
	not allowed("scheduling.read", "POST", "/v1/scheduling/runs/r1:rollback")
}

test_purchasing_action_routes_match_their_colon_suffix if {
	allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/inbound-changes/c1:retry")
	allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/inbound-changes/c1:acknowledge")
	allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/requisition-lines/l1:split")
	not allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/inbound-changes/c1:apply")
}

# #472: expense posts the reimbursement liability with a signed token whose only ledger grant is
# ledger.entry.post; the user-facing duties open only their own routes.
test_expense_reimbursement_liability_and_employee_vendor_register if {
	data.service_grants.expense == {"ledger": ["ledger.entry.post"]}
	allowed("ledger.entry.post", "POST", "/v1/ledger/journal-entries")
	not allowed("ledger.entry.post", "POST", "/v1/ledger/journal-entries/je-1/reverse")
	not allowed("ledger.entry.post", "GET", "/v1/ledger/balances")
	allowed("expense.gl.post", "POST", "/v1/expense/gl-postings")
	not allowed("expense.gl.post", "POST", "/v1/expense/reports/r1/decide")
	not allowed("expense.gl.post", "POST", "/v1/ledger/journal-entries")
	not allowed("expense.approve", "POST", "/v1/expense/gl-postings")
	not allowed("expense.report.write", "POST", "/v1/expense/gl-postings")
	allowed("employee-vendor.maintain", "PUT", "/v1/payables/employee-vendors/w1")
	not allowed("employee-vendor.maintain", "POST", "/v1/payables/payment-proposals")
	not allowed("employee-vendor.maintain", "POST", "/v1/payables/vendors/v1:block")
	not allowed("vendor-block.manage", "PUT", "/v1/payables/employee-vendors/w1")
	not allowed("payment-proposal.write", "PUT", "/v1/payables/employee-vendors/w1")
}

test_carrier_gateway_duties_are_service_only_and_open_only_their_routes if {
	every duty in ["carrier.rate.quote", "carrier.label.create", "carrier.tracking.read"] {
		data.duties[duty].service_only
	}
	data.service_grants.fulfillment["carrier-gateway"] == ["carrier.rate.quote", "carrier.label.create", "carrier.tracking.read"]
	data.service_grants.transport["carrier-gateway"] == ["carrier.rate.quote", "carrier.tracking.read"]
	allowed("carrier.rate.quote", "POST", "/v1/carrier/rates")
	allowed("carrier.rate.quote", "GET", "/v1/carrier/carriers")
	allowed("carrier.label.create", "POST", "/v1/carrier/labels")
	allowed("carrier.tracking.read", "GET", "/v1/carrier/tracking/sandbox/SBX1")
	not allowed("carrier.tracking.read", "GET", "/v1/carrier/tracking/sandbox")
	not allowed("carrier.rate.quote", "POST", "/v1/carrier/labels")
	not allowed("carrier.label.create", "GET", "/v1/carrier/tracking/sandbox/SBX1")
	allowed("fulfillment.warehouse.ship", "POST", "/v1/fulfillment/shipments/s-1:carrier-rates")
	allowed("fulfillment.warehouse.ship", "POST", "/v1/fulfillment/shipments/s-1:carrier-label")
	allowed("fulfillment.warehouse.ship", "POST", "/v1/fulfillment/shipments/s-1:carrier-track")
	not allowed("fulfillment.warehouse.view", "POST", "/v1/fulfillment/shipments/s-1:carrier-label")
}

# Wave 23 slice 36: cleanout counters and their manual reset.
test_scheduling_campaign_place_unplace if {
	allowed("scheduling.schedule.write", "POST", "/v1/scheduling/campaigns/c-1:place")
	allowed("scheduling.schedule.write", "POST", "/v1/scheduling/campaigns/c-1:unplace")
	not allowed("scheduling.read", "POST", "/v1/scheduling/campaigns/c-1:place")
	not allowed("scheduling.campaign.write", "POST", "/v1/scheduling/campaigns/c-1:unplace")
}

test_scheduling_cleanout_counters if {
	allowed("scheduling.read", "GET", "/v1/scheduling/cleanout-counters")
	allowed("scheduling.read", "GET", "/v1/scheduling/resources/r-1/cleanout-counter")
	allowed("scheduling.campaign.write", "POST", "/v1/scheduling/resources/r-1/cleanout-counter:reset")
	not allowed("scheduling.read", "POST", "/v1/scheduling/resources/r-1/cleanout-counter:reset")
}

# Wave 17 slice 29: requisition approval, rejection, reopen and cancel.
test_procurement_requisition_approval if {
	allowed("procurement.requisition.approve", "GET", "/v1/procurement/requisitions/r-1")
	allowed("procurement.requisition.approve", "POST", "/v1/procurement/requisitions/r-1:approve")
	allowed("procurement.requisition.approve", "POST", "/v1/procurement/requisitions/r-1:reject")
	allowed("procurement.requisition.approve", "GET", "/v1/procurement/requisition-approvals")
	allowed("procurement.requisition.create", "POST", "/v1/procurement/requisitions/r-1:reopen")
	allowed("procurement.requisition.create", "POST", "/v1/procurement/requisitions/r-1:cancel")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/requisition-approvals")
	not allowed("procurement.requisition.create", "POST", "/v1/procurement/requisitions/r-1:approve")
	not allowed("procurement.requisition.approve", "POST", "/v1/procurement/requisitions/r-1:cancel")
}

# Wave 23 slice 33: network feasibility inquiry.
test_scheduling_network_feasibility_read if {
	allowed("scheduling.read", "GET", "/v1/scheduling/network-feasibility")
	not allowed("scheduling.read", "POST", "/v1/scheduling/network-feasibility")
}

# Wave 26 slice 26: free goods rule revision.
test_pricing_free_goods_revise if {
	allowed("pricing.maintain", "POST", "/v1/pricing/free-goods-rules/x:revise")
	not allowed("pricing.view", "POST", "/v1/pricing/free-goods-rules/x:revise")
}

# Wave 27 slice 34: sales reads the customer's group from party on order, quotation and release entry.
test_sales_delivery_tolerances if {
	allowed("sales.parameter.maintain", "POST", "/v1/sales/delivery-tolerances")
	allowed("sales.parameter.maintain", "PUT", "/v1/sales/delivery-tolerances/t-1")
	allowed("sales.parameter.maintain", "DELETE", "/v1/sales/delivery-tolerances/t-1")
	allowed("sales.order.read", "GET", "/v1/sales/delivery-tolerances")
	allowed("sales.order.read", "GET", "/v1/sales/delivery-tolerances/t-1")
	allowed("sales.order.read", "GET", "/v1/sales/delivery-tolerances:resolve")
	not allowed("sales.order.read", "POST", "/v1/sales/delivery-tolerances")
	not allowed("sales.order.read", "PUT", "/v1/sales/delivery-tolerances/t-1")
}

test_sales_reads_party_customer if {
	data.service_grants.sales.party == ["party.read"]
	allowed("party.read", "GET", "/v1/party/customers/c-1")
	not allowed("party.read", "POST", "/v1/party/customers/c-1")
}

# Wave 6 slice 47: operating-unit CSV bulk import.
test_org_legal_entity_copy if {
	allowed("org.legal-entity.write", "POST", "/v1/org/legal-entities/le-1:copy")
	not allowed("org.read", "POST", "/v1/org/legal-entities/le-1:copy")
	not allowed("org.legal-entity.write", "GET", "/v1/org/legal-entities/le-1:copy")
}

test_org_operating_unit_import if {
	allowed("org.operating-unit.write", "POST", "/v1/org/operating-units:import")
	not allowed("org.read", "POST", "/v1/org/operating-units:import")
}

# Wave 15 slice 18: ad-hoc statement run retention sweeps.
test_reporting_run_sweeps if {
	allowed("reporting.statement.view", "GET", "/v1/reporting/run-sweeps")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/run-sweeps")
}

# Wave 26 slice 24: commission settlement export records.
test_pricing_commission_settlement_exports if {
	allowed("pricing.commission.maintain", "POST", "/v1/pricing/commission/settlement-exports")
	allowed("pricing.commission.maintain", "POST", "/v1/pricing/commission/settlement-exports/e-1:void")
	allowed("pricing.commission.view", "GET", "/v1/pricing/commission/settlement-exports")
	allowed("pricing.commission.view", "GET", "/v1/pricing/commission/settlement-exports/e-1")
	allowed("pricing.commission.view", "GET", "/v1/pricing/commission/settlement-exports/e-1/file")
	not allowed("pricing.commission.view", "POST", "/v1/pricing/commission/settlement-exports/e-1:void")
}

# Wave 6 slice 48: name translations of legal entities, operating units, sites and warehouses.
test_org_name_translation_writes if {
	allowed("org.legal-entity.write", "PUT", "/v1/org/legal-entities/le-1/translations/fr")
	allowed("org.operating-unit.write", "DELETE", "/v1/org/operating-units/ou-1/translations/fr")
	allowed("org.site.write", "PUT", "/v1/org/sites/s-1/translations/fr-CA")
	allowed("org.site.write", "DELETE", "/v1/org/warehouses/w-1/translations/fr")
	not allowed("org.read", "PUT", "/v1/org/sites/s-1/translations/fr")
}

# Wave 17 slice 28: requisition release runs.
test_procurement_requisition_edit_withdraw_cancel_line if {
	allowed("procurement.requisition.create", "PATCH", "/v1/procurement/requisitions/r-1")
	allowed("procurement.requisition.create", "POST", "/v1/procurement/requisitions/r-1:withdraw")
	allowed("procurement.requisition.create", "POST", "/v1/procurement/requisitions/r-1:cancel-line")
	not allowed("procurement.requisition.approve", "PATCH", "/v1/procurement/requisitions/r-1")
	not allowed("procurement.requisition.approve", "POST", "/v1/procurement/requisitions/r-1:cancel-line")
}

test_procurement_requisition_releases if {
	allowed("procurement.purchase-order.maintain", "POST", "/v1/procurement/requisition-releases")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/requisition-releases")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/procurement/requisition-releases/r-1")
	not allowed("procurement.purchase-order.inquire", "POST", "/v1/procurement/requisition-releases")
}

# Wave 15 slice 16: report library and schedule firings.
test_reporting_library_and_firings if {
	allowed("reporting.statement.view", "GET", "/v1/reporting/library")
	allowed("reporting.statement.view", "GET", "/v1/reporting/library/l-1")
	allowed("reporting.statement.view", "GET", "/v1/reporting/library/l-1/content")
	allowed("reporting.statement.schedule", "GET", "/v1/reporting/schedule-firings")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/library")
}

# Wave 27 slice 38: carry a revised agreement price to released orders.
test_sales_agreement_reprice_orders if {
	allowed("sales.agreement.maintain", "POST", "/v1/sales/agreements/a-1:reprice-orders")
	not allowed("sales.order.read", "POST", "/v1/sales/agreements/a-1:reprice-orders")
}

# Wave 23 slice 34: block utilization report across resources.
test_scheduling_block_utilization_report if {
	allowed("scheduling.read", "GET", "/v1/scheduling/block-utilization")
	not allowed("scheduling.read", "POST", "/v1/scheduling/block-utilization")
}

# Wave 15 slice 17: report library legal holds and retention sweeps.
test_reporting_legal_holds if {
	allowed("reporting.library.hold", "POST", "/v1/reporting/legal-holds")
	allowed("reporting.library.hold", "POST", "/v1/reporting/legal-holds/h-1:release")
	allowed("reporting.statement.view", "GET", "/v1/reporting/legal-holds")
	allowed("reporting.statement.view", "GET", "/v1/reporting/legal-holds/h-1")
	allowed("reporting.statement.view", "GET", "/v1/reporting/library-sweeps")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/legal-holds")
	not allowed("reporting.statement.view", "POST", "/v1/reporting/legal-holds/h-1:release")
	not allowed("reporting.library.hold", "GET", "/v1/reporting/legal-holds")
}

# Wave 27 slice 37: credit exposure report.
test_sales_credit_exposure if {
	allowed("sales.order.read", "GET", "/v1/sales/credit-exposure")
	not allowed("sales.order.read", "POST", "/v1/sales/credit-exposure")
}

# Wave 21 slice 29: production order end-readiness check.
test_production_order_end_check if {
	allowed("production.order.read", "GET", "/v1/production/orders/o-1/end-check")
	not allowed("production.order.read", "POST", "/v1/production/orders/o-1/end-check")
}

test_mobile_service_routes if {
	allowed("warehouse.work.execute", "POST", "/v1/mobile/sessions")
	allowed("warehouse.work.execute", "POST", "/v1/mobile/sessions/s-1:heartbeat")
	allowed("warehouse.work.execute", "POST", "/v1/mobile/sessions/s-1/runs/current/steps/scan_item:submit")
	allowed("warehouse.work.execute", "GET", "/v1/mobile/menus/default")
	allowed("warehouse.work.execute", "POST", "/v1/mobile/offline:replay")
	not allowed("warehouse.work.execute", "GET", "/v1/mobile/sessions")
	not allowed("warehouse.work.execute", "POST", "/v1/mobile/sessions/s-1:force-signout")
	not allowed("warehouse.work.execute", "POST", "/v1/mobile/flows")
	allowed("warehouse.work.supervise", "GET", "/v1/mobile/sessions")
	allowed("warehouse.work.supervise", "POST", "/v1/mobile/sessions/s-1:force-signout")
	not allowed("warehouse.work.supervise", "POST", "/v1/mobile/offline:replay")
	allowed("warehouse.mobile-flow.admin", "POST", "/v1/mobile/flows/f-1:publish")
	allowed("warehouse.mobile-flow.admin", "POST", "/v1/mobile/flows:install-core")
	allowed("warehouse.mobile-flow.admin", "POST", "/v1/mobile/menus")
	not allowed("warehouse.mobile-flow.admin", "POST", "/v1/mobile/offline/conflicts/dev-1:3:resolve")
	allowed("warehouse.offline-conflict.resolve", "GET", "/v1/mobile/offline/conflicts")
	allowed("warehouse.offline-conflict.resolve", "POST", "/v1/mobile/offline/conflicts/dev-1:3:resolve")
	not allowed("warehouse.offline-conflict.resolve", "POST", "/v1/mobile/flows")
}

test_global_search_route_is_open_to_each_source_read_duty_only if {
	allowed("party.read", "GET", "/v1/search")
	allowed("product.read", "GET", "/v1/search")
	allowed("procurement.purchase-order.inquire", "GET", "/v1/search")
	allowed("receiving.receipt.maintain", "GET", "/v1/search")
	allowed("receiving.ap.inquire", "GET", "/v1/search")
	allowed("sales.order.read", "GET", "/v1/search")
	allowed("fulfillment.warehouse.view", "GET", "/v1/search")
	allowed("billing.invoice.view", "GET", "/v1/search")
	not allowed("party.read", "POST", "/v1/search")
	not allowed("party.write", "GET", "/v1/search")
	not allowed("inbox.read", "GET", "/v1/search")
	not allowed("ledger.journal.read", "GET", "/v1/search")
	not allowed("search.index.write", "GET", "/v1/search")
	not principal_allowed("search-index", "GET", "/v1/search")
	data.duties["search.index.write"].flag
	count(data.duties["search.index.write"].permissions) == 0
	data.service_principals["search-index"] == ["search.index.write"]
}

# Issue #495: the dev seed (deploy/local/seed.json) holds an admin and a clerk; admin routes are the admin's alone.
dev_seed_assignments := {"tenant-1": {
	"admin": [
		"audit.read", "numbering.read", "numbering.sequence.maintain", "numbering.write",
		"identity.user.maintain", "identity.security-role.maintain", "identity.feature.view",
	],
	"clerk": ["receiving.receipt.maintain", "ledger.journal.read", "party.read", "identity.feature.view"],
}}

dev_seed_decision(user, action, resource) := decision if {
	decision := data.authz.decision with input as {
		"subject": {"tenant_id": "tenant-1", "user_id": user},
		"action": action,
		"resource": resource,
	}
		with data.assignments as dev_seed_assignments
}

test_dev_seed_admin_reaches_admin_routes if {
	dev_seed_decision("admin", "POST", "/v1/numbering/sequences").allow
	dev_seed_decision("admin", "GET", "/v1/numbering/sequences/PARTY").allow
	dev_seed_decision("admin", "POST", "/v1/identity/users").allow
}

test_dev_seed_clerk_is_denied_admin_routes_with_a_missing_duty if {
	numbering := dev_seed_decision("clerk", "POST", "/v1/numbering/sequences")
	not numbering.allow
	numbering.duty_missing in {"numbering.write", "numbering.sequence.maintain"}
	users := dev_seed_decision("clerk", "POST", "/v1/identity/users")
	not users.allow
	users.duty_missing != ""
	not dev_seed_decision("clerk", "GET", "/v1/numbering/sequences/PARTY").allow
}

test_dev_seed_clerk_keeps_its_own_routes if {
	dev_seed_decision("clerk", "GET", "/v1/ledger/journal-lines").allow
	dev_seed_decision("clerk", "GET", "/v1/party/parties/p-1").allow
}

test_dev_seed_unknown_user_is_denied_everything if {
	not dev_seed_decision("stranger", "GET", "/v1/ledger/journal-lines").allow
}

test_tenant_features_view_reads_only if {
	allowed("identity.feature.view", "GET", "/v1/tenant/features")
	not allowed("identity.feature.view", "PUT", "/v1/tenant/features")
	not allowed("identity.feature.view", "POST", "/v1/tenant/features")
}

test_tenant_features_maintain_reads_and_replaces if {
	allowed("identity.feature.maintain", "GET", "/v1/tenant/features")
	allowed("identity.feature.maintain", "PUT", "/v1/tenant/features")
	not allowed("identity.feature.maintain", "DELETE", "/v1/tenant/features")
}

test_tenant_features_are_not_reachable_through_other_identity_duties if {
	not allowed("identity.tenant.maintain", "PUT", "/v1/tenant/features")
	not allowed("identity.legal-entity.maintain", "PUT", "/v1/tenant/features")
	not allowed("identity.security-role.maintain", "PUT", "/v1/tenant/features")
	not allowed("identity.feature.maintain", "GET", "/v1/tenants")
	not allowed("identity.feature.maintain", "PUT", "/v1/tenant/features/extra")
}

test_tenant_setup_view_reads_only if {
	allowed("identity.setup.view", "GET", "/v1/tenant/setup")
	allowed("identity.setup.view", "GET", "/v1/tenant/workspace-config")
	not allowed("identity.setup.view", "PUT", "/v1/tenant/setup/steps/company")
	not allowed("identity.setup.view", "PUT", "/v1/tenant/workspace-config")
}

test_tenant_setup_maintain_reads_and_writes_setup_only if {
	allowed("identity.setup.maintain", "PUT", "/v1/tenant/setup/steps/company")
	allowed("identity.setup.maintain", "PUT", "/v1/tenant/workspace-config")
	not allowed("identity.setup.maintain", "DELETE", "/v1/tenant/workspace-config")
	not allowed("identity.setup.maintain", "PUT", "/v1/tenant/features")
	not allowed("identity.feature.maintain", "PUT", "/v1/tenant/setup/steps/company")
	not allowed("identity.setup.maintain", "GET", "/v1/tenants")
}

test_dev_seed_every_user_reads_features_but_cannot_change_them if {
	dev_seed_decision("admin", "GET", "/v1/tenant/features").allow
	dev_seed_decision("clerk", "GET", "/v1/tenant/features").allow
	not dev_seed_decision("admin", "PUT", "/v1/tenant/features").allow
	not dev_seed_decision("clerk", "PUT", "/v1/tenant/features").allow
}

test_processmfg_service_grant_reads_features_only if {
	data.service_grants.processmfg == {"identity": ["identity.feature.view"]}
	allowed("identity.feature.view", "GET", "/v1/tenant/features")
	not allowed("identity.feature.view", "PUT", "/v1/tenant/features")
	not allowed("identity.feature.view", "GET", "/v1/tenants")
	not allowed("identity.feature.view", "GET", "/v1/legal-entities")
	not allowed("identity.feature.view", "GET", "/v1/authz/assignments")
	not allowed("identity.feature.view", "POST", "/v1/identity/users")
	not "identity.feature.maintain" in data.service_grants.processmfg.identity
}

inbox_routes := {
	"procurement.inbox.read": "/v1/procurement/requisition-approvals",
	"party.inbox.read": "/v1/party/pending-changes",
	"ledger.inbox.read": "/v1/ledger/journal-batches",
	"payables.inbox.read": "/v1/payables/open-items",
}

test_inbox_read_duties_are_service_only if {
	every duty, _ in inbox_routes {
		data.duties[duty].service_only == true
	}
}

test_batch_grants_exactly_the_inbox_and_product_duties if {
	data.service_grants.batch == {
		"product": ["product.variants.explode", "product.released.read", "product.jobs.write"],
		"purchasing": ["procurement.inbox.read"],
		"party": ["party.inbox.read"],
		"ledger": ["ledger.inbox.read"],
		"payables": ["payables.inbox.read"],
	}
}

test_inbox_read_duties_reach_only_their_list_route if {
	every duty, route in inbox_routes {
		allowed(duty, "GET", route)
		not allowed(duty, "GET", concat("/", [route, "item-1"]))
		not allowed(duty, "POST", route)
		not allowed(duty, "POST", concat("/", [route, "item-1"]))
		not allowed(duty, "GET", "/v1/inbox")
	}
}

test_inbox_read_reaches_only_the_inbox if {
	allowed("inbox.read", "GET", "/v1/inbox")
	not allowed("inbox.read", "POST", "/v1/inbox")
	not allowed("inbox.read", "GET", "/v1/inbox/item-1")
	not allowed("inbox.read", "GET", "/v1/ledger/journal-batches")
	not allowed("ledger.journal.read", "GET", "/v1/inbox")
}

test_receivables_billing_consumer_holds_only_a_flag_duty_and_no_route if {
	data.service_principals["receivables-billing"] == ["receivables.openitem.from-billing"]
	data.duties["receivables.openitem.from-billing"].flag
	count(data.duties["receivables.openitem.from-billing"].permissions) == 0
	every _, targets in data.service_grants {
		not "receivables.openitem.from-billing" in object.get(targets, "receivables", [])
	}
	not principal_allowed("receivables-billing", "POST", "/v1/receivables/open-items")
	not principal_allowed("receivables-billing", "POST", "/v1/receivables/open-items:batch")
	not principal_allowed("receivables-billing", "POST", "/v1/receivables/payments")
	not principal_allowed("receivables-billing", "GET", "/v1/receivables/open-items")
	not principal_allowed("receivables-billing", "POST", "/v1/ledger/journal-entries")
	not principal_allowed("receivables-billing", "POST", "/v1/billing/invoices")
	not allowed("receivables.openitem.from-billing", "POST", "/v1/receivables/open-items")
}

test_rma_credit_operator_routes_belong_to_the_payment_duty if {
	allowed("receivables.payment.post", "POST", "/v1/receivables/rma-credits/c1:cancel")
	allowed("receivables.payment.post", "POST", "/v1/receivables/rma-credits/c1:backfill-reservation")
	not allowed("receivables.view", "POST", "/v1/receivables/rma-credits/c1:cancel")
	not allowed("receivables.view", "POST", "/v1/receivables/rma-credits/c1:backfill-reservation")
}

test_remittance_advice_belongs_to_the_payment_post_duty if {
	allowed("receivables.payment.post", "POST", "/v1/receivables/remittance-advices")
	not allowed("receivables.view", "POST", "/v1/receivables/remittance-advices")
	not allowed("receivables.payment-file.import", "POST", "/v1/receivables/remittance-advices")
}

test_credit_settlement_belongs_to_the_settle_duty if {
	allowed("receivables.payment.settle", "POST", "/v1/receivables/credit-settlements")
	not allowed("receivables.payment.post", "POST", "/v1/receivables/credit-settlements")
	not allowed("receivables.view", "POST", "/v1/receivables/credit-settlements")
}

test_subscription_usage_and_due_job_belong_to_invoice_maintain if {
	allowed("billing.invoice.maintain", "POST", "/v1/billing/subscriptions/s1/usage")
	allowed("billing.invoice.maintain", "POST", "/v1/billing/subscriptions:bill-due")
	not allowed("billing.invoice.view", "POST", "/v1/billing/subscriptions/s1/usage")
	not allowed("billing.invoice.view", "POST", "/v1/billing/subscriptions:bill-due")
}

test_gl_journal_routes_split_between_view_and_payment_duties if {
	allowed("receivables.payment.post", "POST", "/v1/receivables/gl-journals/j1:retry")
	allowed("receivables.payment.post", "POST", "/v1/receivables/gl-journals:sweep")
	allowed("receivables.view", "GET", "/v1/receivables/gl-journals")
	allowed("receivables.view", "GET", "/v1/receivables/gl-reconciliation")
	not allowed("receivables.view", "POST", "/v1/receivables/gl-journals:sweep")
	not allowed("receivables.view", "POST", "/v1/receivables/gl-journals/j1:retry")
}

test_asn_duties_do_not_cross if {
	not allowed("receiving.asn.maintain", "POST", "/v1/receiving/asns/a1:start-receipt")
	not allowed("receiving.asn.receive", "POST", "/v1/receiving/asns")
}

test_analytics_ux_record_writes_events_and_cannot_read_funnels if {
	allowed("analytics.ux.record", "POST", "/v1/analytics/ux-events")
	not allowed("analytics.ux.record", "GET", "/v1/analytics/ux-funnels")
	not allowed("analytics.ux.record", "GET", "/v1/analytics/ux-signals")
	not allowed("analytics.ux.record", "GET", "/v1/analytics/metrics")
}

test_analytics_ux_view_reads_funnels_and_cannot_write_events if {
	allowed("analytics.ux.view", "GET", "/v1/analytics/ux-funnels")
	allowed("analytics.ux.view", "GET", "/v1/analytics/ux-signals")
	not allowed("analytics.ux.view", "POST", "/v1/analytics/ux-events")
	not allowed("analytics.ux.view", "POST", "/v1/analytics/query")
}

test_print_document_render_is_its_own_duty if {
	allowed("print.document.render", "POST", "/print/v1/documents:render")
	not allowed("print.document.render", "POST", "/print/v1/jobs")
	not allowed("print.job.submit", "POST", "/print/v1/documents:render")
	not allowed("print.admin", "POST", "/print/v1/documents:render")
}

test_print_queue_management_is_admin_only if {
	allowed("print.admin", "PATCH", "/print/v1/printers/p-1")
	allowed("print.admin", "POST", "/print/v1/printers/p-1:retire")
	allowed("print.admin", "PUT", "/print/v1/routing-rules/r-1")
	allowed("print.admin", "DELETE", "/print/v1/routing-rules/r-1")
	allowed("print.admin", "POST", "/print/v1/jobs:sweep")
	not allowed("print.job.submit", "PATCH", "/print/v1/printers/p-1")
	not allowed("print.job.submit", "POST", "/print/v1/printers/p-1:retire")
	not allowed("print.job.submit", "DELETE", "/print/v1/routing-rules/r-1")
	not allowed("print.job.submit", "POST", "/print/v1/jobs:sweep")
}

test_receiving_document_flow_is_read_only_and_separate if {
	allowed("receiving.document-flow.read", "GET", "/v1/receiving/related-documents")
	not allowed("receiving.document-flow.read", "POST", "/v1/receiving/related-documents")
	not allowed("receiving.document-flow.read", "GET", "/v1/receiving/invoices")
	not allowed("receiving.ap.inquire", "GET", "/v1/receiving/related-documents")
}

# #45: the technician's day and capture routes belong to the technician duty; reading a capture
# back is an inquiry; the checklist is set by office staff who maintain work orders.
test_fieldservice_technician_day_and_captures if {
	allowed("fieldservice.work-order.execute", "GET", "/v1/service/technicians/me/day")
	allowed("fieldservice.work-order.execute", "GET", "/v1/service/technicians/t1/day")
	allowed("fieldservice.work-order.execute", "PUT", "/v1/service/work-orders/w1/captures/c1")
	not allowed("fieldservice.work-order.execute", "GET", "/v1/service/work-orders/w1/captures/c1")
	not allowed("fieldservice.work-order.execute", "PUT", "/v1/service/work-orders/w1/checklist")
	allowed("fieldservice.inquire", "GET", "/v1/service/work-orders/w1/captures/c1")
	not allowed("fieldservice.inquire", "PUT", "/v1/service/work-orders/w1/captures/c1")
	not allowed("fieldservice.inquire", "GET", "/v1/service/technicians/me/day")
	allowed("fieldservice.work-order.maintain", "PUT", "/v1/service/work-orders/w1/checklist")
	not allowed("fieldservice.work-order.maintain", "PUT", "/v1/service/work-orders/w1/captures/c1")
}
