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
	flag_duties := {"ARWriteOffApprove", "ConsolidationView", "EDI_PII_VIEW", "Maintain tax codes", "budget.override", "inventory.count.approve", "ledger.journal.postdirect", "ledger.journal.postledger", "org.hierarchy.backdate", "party.merge", "pii.read", "production.batch.consume-expired", "production.journal.over-consume", "production.order.bom-after-release", "production.order.rework", "production.order.zero-output", "shopfloor.check.override", "tenant.administer", "treasury.facility.override"}
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
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:post"],
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:correct"],
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:cancel"],
	["ReceivingPost", "POST", "/v1/receiving/invoice-adjustments:post"],
	["ReceivingPost", "POST", "/v1/receiving/plan-settlements:run"],
	["ReceivingPost", "POST", "/v1/receiving/consignment-settlements:run"],
	["ReceivingPost", "POST", "/v1/receiving/ers-settlements:run"],
	["VendorInvoicePost", "POST", "/v1/receiving/receipts/r1:post"],
	["VendorInvoicePost", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["VendorInvoicePost", "POST", "/v1/receiving/invoices/i1:reject-audit-block"],
	["MatchingPolicyMaintain", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["InvoiceAuditRelease", "POST", "/v1/receiving/invoices:post"],
	["InvoiceAuditRelease", "PUT", "/v1/receiving/audit-policy"],
	["VendorInvoicePost", "PUT", "/v1/receiving/audit-policy"],
	["VendorInvoicePost", "PUT", "/v1/receiving/ers-vendors/v1"],
	["PurchaseOrderMaintain", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["RFQMaintain", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/outputs/pdf"],
	["reporting.statement.view", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.statement.view", "GET", "/v1/reporting/definitions/d1/export"],
	["View platform releases", "POST", "/v1/platform/releases"],
	["Approve production promotion", "GET", "/v1/platform/releases"],
	["Rollback environment", "DELETE", "/v1/platform/releases/r1"],
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
	["ReceivingPost", "POST", "/v1/receiving/receipts/r1:post"],
	["VendorInvoicePost", "POST", "/v1/receiving/invoice-adjustments:post"],
	["VendorInvoicePost", "POST", "/v1/receiving/plan-settlements:run"],
	["VendorInvoicePost", "POST", "/v1/receiving/consignment-settlements:run"],
	["VendorInvoicePost", "POST", "/v1/receiving/ers-settlements:run"],
	["InvoiceAuditRelease", "POST", "/v1/receiving/invoices/i1:release-audit-block"],
	["InvoiceAuditRelease", "POST", "/v1/receiving/invoices/i1:reject-audit-block"],
	["ErsPriceConfirm", "PUT", "/v1/receiving/receipts/r1/lines/1/ers-price-confirmation"],
	["MatchingPolicyMaintain", "PUT", "/v1/receiving/audit-policy"],
	["MatchingPolicyMaintain", "PUT", "/v1/receiving/ers-vendors/v1"],
	["PurchaseOrderConfirm", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["RFQAward", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.definition.maintain", "GET", "/v1/reporting/definitions/d1/export"],
	["Approve production promotion", "POST", "/v1/platform/releases"],
	["Rollback environment", "POST", "/v1/platform/releases"],
	["Maintain platform releases", "POST", "/v1/platform/releases"],
]

test_verb_routes_granted_to_their_own_duty if {
	every grant in segregation_grants {
		allowed(grant[0], grant[1], grant[2])
	}
}

test_platform_duties_keep_prefix_reach if {
	allowed("Maintain vendors", "GET", "/v1/vendors")
	allowed("Maintain vendors", "PUT", "/v1/vendors/v1/contacts/c1")
	allowed("View exchange rates", "GET", "/v1/fx/rates/history")
	not allowed("View exchange rates", "GET", "/v1/fx/ratesx")
}

test_maintain_vendors_can_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain vendors"]}}
	result.allow
}

test_view_vendors_cannot_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View vendors"]}}
	result == {"allow": false, "duty_missing": "Maintain vendors"}
}

test_missing_vendor_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/vendors/vendor-a",
	}
	result == {"allow": false, "duty_missing": "View vendors"}
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
		with data.assignments as {"tenant-a": {"user-a": ["Maintain vendors"]}}
	result.allow == false
}

test_neighbor_path_does_not_match if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "PUT",
		"resource": "/v1/vendors-extra/vendor-a",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain vendors"]}}
	result.allow == false
}

test_view_products_can_get_but_not_post if {
	allowed := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/product/products/p1",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View products"]}}
	allowed.allow

	denied := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/product/products",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View products"]}}
	denied == {"allow": false, "duty_missing": "product.write"}
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
		with data.assignments as {"tenant-a": {"user-a": ["Maintain number sequences"]}}
	create.allow

	allocate := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/numbering/sequences/INV/next",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain number sequences"]}}
	allocate.allow
}

test_view_number_sequences_can_get_but_cannot_allocate if {
	read := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/numbering/sequences/INV",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View number sequences"]}}
	read.allow

	write := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/numbering/sequences/INV/next",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View number sequences"]}}
	write == {"allow": false, "duty_missing": "Maintain number sequences"}
}

test_legacy_number_sequences_path_is_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/number-sequences",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain number sequences"]}}
	result.allow == false
}

test_view_audit_log_can_get_record_and_inbox if {
	record := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/records/party.vendor/v-1",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View audit log"]}}
	record.allow

	inbox := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/alerts",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View audit log"]}}
	inbox.allow
}

test_view_audit_log_cannot_ingest_change if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/changes",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View audit log"]}}
	result == {"allow": false, "duty_missing": "Record audit changes"}
}

test_record_audit_changes_can_post_ingest_and_alert_rules if {
	ingest := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/changes",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Record audit changes"]}}
	ingest.allow

	rules := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/audit/alert-rules",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Record audit changes"]}}
	rules.allow
}

test_missing_numbering_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/numbering/sequences/INV",
	}
	result == {"allow": false, "duty_missing": "View number sequences"}
}

test_missing_audit_get_duty_is_least_privileged if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/audit/records/party.vendor/v-1",
	}
	result == {"allow": false, "duty_missing": "View audit log"}
}

test_approve_workflow_can_get_inbox_and_act if {
	inbox := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/workitems",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Approve workflow"]}}
	inbox.allow

	act := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/workitems/wi-1/actions",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Approve workflow"]}}
	act.allow
}

test_view_outbound_messages_cannot_enqueue if {
	read := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/notify/messages",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View outbound messages"]}}
	read.allow

	write := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/notify/messages",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View outbound messages"]}}
	write == {"allow": false, "duty_missing": "Send notifications"}
}

test_maintain_batch_jobs_can_start_run_and_replay if {
	run := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/batch/jobs/rows-1m/runs",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain batch jobs"]}}
	run.allow

	replay := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/events/replay",
	}
		with data.assignments as {"tenant-a": {"user-a": ["Maintain batch jobs"]}}
	replay.allow
}

test_view_exchange_rates_cannot_post_rates if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/fx/rates",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View exchange rates"]}}
	result == {"allow": false, "duty_missing": "Maintain exchange rates"}
}

test_view_exchange_rates_can_convert if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "POST",
		"resource": "/v1/fx/convert",
	}
		with data.assignments as {"tenant-a": {"user-a": ["View exchange rates"]}}
	result.allow
}

test_administer_exchange_rates_allows_only_provider_import if {
	allowed("Administer exchange rates", "POST", "/v1/fx/providers/ecb:import")
	not allowed("Administer exchange rates", "GET", "/v1/fx/providers/ecb:import")
	not allowed("Administer exchange rates", "POST", "/v1/fx/rates")
	not allowed("Maintain exchange rates", "POST", "/v1/fx/providers/ecb:import")
}

test_payment_file_preview_duty_allows_only_its_route if {
	allowed("payment-file.preview", "POST", "/v1/payables/payment-files:preview")
	not allowed("payment-file.preview", "POST", "/v1/payables/payments/status:parse")
}

test_payment_status_parse_duty_does_not_open_file_preview if {
	allowed("payment-status.parse", "POST", "/v1/payables/payments/status:parse")
	not allowed("payment-status.parse", "POST", "/v1/payables/payment-files:preview")
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

granted_duty_known(duty) if data.duties[duty]

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
	not allowed("period.check", "PUT", "/v1/close/periods/p-1")
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
	"TMS_TRACK_INGEST",
	"shopfloor.time.autoclockout",
	"sales.channel.intake",
	"inventory.transaction.apply",
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

test_service_grants_hold_no_flag_or_all_scope_duty if {
	every _, targets in data.service_grants {
		every _, duties in targets {
			every duty in duties {
				not data.duties[duty].flag
				duty != "org.switcher.all_scope"
			}
		}
	}
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
	not allowed("PurchaseOrderMaintain", "PUT", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan/rows/2/settlement")
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
	result.duty_missing == "View exchange rates"
	result2 := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
		"action": "GET",
		"resource": "/v1/procurement/purchase-orders/po-1/invoicing-plans",
	}
		with data.assignments as {"tenant-a": {"user-a": []}}
	result2.duty_missing == "PurchaseOrderInquire"
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
