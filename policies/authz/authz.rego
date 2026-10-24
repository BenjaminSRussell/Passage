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
	missing_duty := sort(candidates)[0]
}

default decision := {"allow": false}

path_matches(prefix, path) if path == prefix

path_matches(prefix, path) if startswith(path, sprintf("%s/", [prefix]))
