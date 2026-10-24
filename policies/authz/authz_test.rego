package authz

import rego.v1

test_all_seeded_duties_allow_their_declared_permissions if {
	count(data.duties) == 20
	every duty, spec in data.duties {
		count(spec.permissions) > 0
		every permission in spec.permissions {
			method := permission.methods[0]
			result := decision with input as {
				"subject": {"tenant_id": "tenant-a", "user_id": "user-a", "duties": [duty]},
				"action": method,
				"resource": permission.resource,
			}
			result.allow
		}
	}
}

test_maintain_vendors_can_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a", "duties": ["Maintain vendors"]},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
	result.allow
}

test_view_vendors_cannot_put if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a", "duties": ["View vendors"]},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
	result == {"allow": false, "duty_missing": "Maintain vendors"}
}

test_unknown_duty_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "tenant-a", "user_id": "user-a", "duties": ["Unrecognized duty"]},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
	result.allow == false
}

test_missing_identity_denied if {
	result := decision with input as {
		"subject": {"tenant_id": "", "user_id": "user-a", "duties": ["Maintain vendors"]},
		"action": "PUT",
		"resource": "/v1/vendors/vendor-a",
	}
	result.allow == false
}
