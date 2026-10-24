package authz

import rego.v1

test_all_seeded_duties_allow_their_declared_permissions if {
	count(data.duties) == 20
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
