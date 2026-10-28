package authz

import rego.v1

default allow := false

allow if {
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	some duty in data.assignments[input.subject.tenant_id][input.subject.user_id]
	some permission in data.duties[duty].permissions
	input.action in permission.methods
	path_matches(permission.resource, input.resource)
}

decision := {"allow": true} if allow

decision := {"allow": false, "duty_missing": missing_duty} if {
	not allow
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	candidates := {duty |
		some duty, spec in data.duties
		some permission in spec.permissions
		input.action in permission.methods
		path_matches(permission.resource, input.resource)
	}
	count(candidates) > 0
	sizes := {duty_permission_count(duty) |
		some duty in candidates
	}
	smallest_size := min(sizes)
	least_privileged := {duty |
		some duty in candidates
		duty_permission_count(duty) == smallest_size
	}
	missing_duty := sort(least_privileged)[0]
}

default decision := {"allow": false}

duty_permission_count(duty) := total if {
	counts := [count(permission.methods) |
		some permission in data.duties[duty].permissions
	]
	total := sum(counts)
}

path_matches(pattern, path) if glob.match(pattern, ["/", ":"], path)
