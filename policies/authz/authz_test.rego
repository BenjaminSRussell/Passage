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
	count(data.duties) == 107
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
	flag_duties := {"ledger.journal.postdirect", "ledger.journal.postledger", "inventory.count.approve", "pii.read"}
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
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:post"],
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:correct"],
	["ReceivingMaintain", "POST", "/v1/receiving/receipts/r1:cancel"],
	["ReceivingPost", "POST", "/v1/receiving/invoice-adjustments:post"],
	["ReceivingPost", "POST", "/v1/receiving/plan-settlements:run"],
	["ReceivingPost", "POST", "/v1/receiving/consignment-settlements:run"],
	["VendorInvoicePost", "POST", "/v1/receiving/receipts/r1:post"],
	["PurchaseOrderMaintain", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["RFQMaintain", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/outputs/pdf"],
	["reporting.statement.view", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.statement.view", "GET", "/v1/reporting/definitions/d1/export"],
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
	["ledger.setup.write", "POST", "/v1/ledger/ledgers/l1/account-mappings"],
	["ReceivingPost", "POST", "/v1/receiving/receipts/r1:post"],
	["VendorInvoicePost", "POST", "/v1/receiving/invoice-adjustments:post"],
	["VendorInvoicePost", "POST", "/v1/receiving/plan-settlements:run"],
	["VendorInvoicePost", "POST", "/v1/receiving/consignment-settlements:run"],
	["PurchaseOrderConfirm", "POST", "/v1/procurement/purchase-orders/p1:approve"],
	["RFQAward", "POST", "/v1/procurement/rfqs/q1:award"],
	["ledger.journal.read", "GET", "/v1/reporting/runs/r1/cells/a/b/lines"],
	["reporting.definition.maintain", "GET", "/v1/reporting/definitions/d1/export"],
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
	denied == {"allow": false, "duty_missing": "Maintain products"}
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
}

granted_duty_known(duty) if data.duties[duty]

granted_duty_known("numbering.write")

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
	every duty in data.service_grants.receiving.ledger {
		not allowed(duty, "POST", "/v1/ledger/ledgers")
		not allowed(duty, "PUT", "/v1/ledger/charts/c1/accounts/1000")
		not allowed(duty, "POST", "/v1/ledger/balances:rebuild")
	}
	every duty in data.service_grants.payables.ledger {
		not allowed(duty, "POST", "/v1/ledger/journal-entries")
		not allowed(duty, "POST", "/v1/ledger/journal-entries/je-1/reverse")
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
