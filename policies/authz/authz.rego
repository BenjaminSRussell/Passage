package authz

import rego.v1

default allow := false

allow if {
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	some duty in data.assignments[input.subject.tenant_id][input.subject.user_id]
	tenant_may_hold(duty)
	some permission in data.duties[duty].permissions
	input.action in permission.methods
	path_matches(permission.resource, input.resource)
}

# An operator_only duty acts on platform-wide state (the ops control plane): it grants nothing outside the platform
# operator's tenant, whatever a tenant's role assignments say. identity adds operator_tenant_id to the compiled bundle;
# without it no tenant may hold such a duty.
tenant_may_hold(duty) if not data.duties[duty].operator_only

tenant_may_hold(duty) if {
	data.duties[duty].operator_only == true
	input.subject.tenant_id == data.operator_tenant_id
}

decision := {"allow": true} if allow

decision := {"allow": false, "duty_missing": missing_duty} if {
	not allow
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	candidates := hint_candidates
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

granting_duties := {duty |
	some duty, spec in data.duties
	some permission in spec.permissions
	input.action in permission.methods
	path_matches(permission.resource, input.resource)
}

person_duties := {duty |
	some duty in granting_duties
	not data.duties[duty].service_only
}

hint_candidates := person_duties if count(person_duties) > 0

hint_candidates := granting_duties if count(person_duties) == 0

duty_permission_count(duty) := total if {
	counts := [count(permission.methods) |
		some permission in data.duties[duty].permissions
	]
	total := sum(counts)
}

path_matches(pattern, path) if glob.match(pattern, ["/", ":"], path)
