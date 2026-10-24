package authz

import rego.v1

test_all_seeded_duties_allow_their_declared_permissions if {
	count(data.duties) == 22
	every duty, spec in data.duties {
		count(spec.permissions) > 0
		every permission in spec.permissions {
			method := permission.methods[0]
			result := decision with input as {
				"subject": {"tenant_id": "tenant-a", "user_id": "user-a"},
				"action": method,
				"resource": permission.resource,
			}
				with data.assignments as {"tenant-a": {"user-a": [duty]}}
			result.allow
		}
	}
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
