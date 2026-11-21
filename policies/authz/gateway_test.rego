package authz

import rego.v1

# B-04: the Envoy ext_authz answer (data.authz.envoy) for the shared route table (testdata/route_table.json) and the
# gateway path mapping. The single-host gateways run the same table in Go against a real OPA.

route_table := data.testdata.cases

route_tenant := data.testdata.tenant

case_user(c) := concat("", ["user-", c.name])

# A token_duties_only case is a machine caller: its duties are in its token, which Envoy never shows OPA, and identity
# holds no assignment for it.
case_assignments(c) := {route_tenant: {case_user(c): c.duties}} if not c.token_duties_only

case_assignments(c) := {} if c.token_duties_only

case_scoped(c) := {route_tenant: {case_user(c): object.get(c, "scoped_duties", {})}}

case_headers(c) := object.union(
	{"x-depot-tenant": route_tenant, "x-depot-user": case_user(c), "x-request-id": "req-1"},
	{"x-depot-legal-entity": c.legal_entity},
) if c.legal_entity

case_headers(c) := {"x-depot-tenant": route_tenant, "x-depot-user": case_user(c), "x-request-id": "req-1"} if {
	not c.legal_entity
}

envoy_input(method, path, headers) := {"attributes": {"request": {"http": {"method": method, "path": path, "headers": headers}}}}

envoy_answer(c) := result if {
	result := envoy with input as envoy_input(c.method, c.path, case_headers(c))
		with data.assignments as case_assignments(c)
		with data.scoped_assignments as case_scoped(c)
}

test_route_table_is_not_empty if count(route_table) >= 10

test_envoy_answers_the_route_table if {
	mismatches := {c.name |
		some c in route_table
		envoy_answer(c).allowed != c.allow
	}
	count(mismatches) == 0
}

test_envoy_refusal_status_of_every_denied_case if {
	mismatches := {c.name |
		some c in route_table
		not c.allow
		envoy_answer(c).http_status != object.get(c, "status", 403)
	}
	count(mismatches) == 0
}

test_envoy_records_every_route_table_decision if {
	every c in route_table {
		record := envoy_answer(c).record
		record.event == "authz.decision"
		record.source == "envoy"
		record.tenant == route_tenant
		record.subject == "[redacted]" # no DEPOT_LOG_PSEUDONYM_KEY in the test environment
		record.request_id == "req-1"
		record.outcome == {true: "allow", false: "deny"}[c.allow]
		not contains(json.marshal(record), case_user(c))
	}
}

test_envoy_deny_carries_the_problem_and_missing_duty if {
	answer := envoy with input as envoy_input("POST", "/api/v1/numbering/sequences", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == false
	answer.http_status == 403
	body := json.unmarshal(answer.body)
	body.code == "AUTH-0003"
	body.errors.duty_missing == "numbering.sequence.maintain"
	answer.record.reason == "policy_denied"
	answer.record.duty == "numbering.sequence.maintain"
	answer.record.route == "POST /v1/numbering/**"
}

test_envoy_allow_names_the_granting_duty_and_route_template if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s-42?x=1", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == true
	answer.record.duty == "numbering.read"
	answer.record.reason == "policy_allowed"
	answer.record.route == "GET /v1/numbering/**"
	not contains(answer.record.route, "s-42")
}

test_envoy_without_jwt_headers_is_unauthenticated if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s1", {})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == false
	answer.record.reason == "unauthenticated"
}

test_envoy_refuses_paths_that_are_not_canonical if {
	every path in [
		"/api/v1/numbering/../identity/users",
		"/api/v1/numbering//sequences",
		"/api/v1/numbering/%2e%2e/identity",
		"/api/v1/numbering%2Fsequences",
		"/api/v1/numbering/sequences/s1%3Anext",
		"/api/v1/numbering/sequences/%73",
		"/api/v1/numbering/sequences/s%201",
		"/api/v1/numbering/sequences/s1%",
		"relative/v1/numbering",
	] {
		answer := envoy with input as envoy_input("GET", path, {"x-depot-tenant": "t", "x-depot-user": "u"})
			with data.assignments as {"t": {"u": ["numbering.sequence.maintain"]}}
		answer.allowed == false
		answer.http_status == 400
		answer.record.reason == "not_canonical"
		answer.record.route == "GET unmatched"
	}
}

test_envoy_denies_paths_outside_the_api if {
	answer := envoy with input as envoy_input("GET", "/healthz", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.sequence.maintain"]}}
	answer.allowed == false
	answer.http_status == 403
}

test_gateway_resource_maps_public_paths_to_the_service_base if {
	gateway_resource("/api/v1/numbering/sequences/s1") == "/v1/numbering/sequences/s1"
	gateway_resource("/v1/numbering/sequences/s1") == "/v1/numbering/sequences/s1"
	gateway_resource("/api/v1/numbering") == "/v1/numbering"
	gateway_resource("/api/v1/production/batches:merge") == "/v1/production/batches:merge"
	gateway_resource("/api/v1/agents/a1/runs") == "/api/v1/agents/a1/runs"
	gateway_resource("/v1/agents/a1/runs") == "/api/v1/agents/a1/runs"
	gateway_resource("/api/v1/edi/partners") == "/edi/v1/partners"
	gateway_resource("/api/v1/print/jobs") == "/print/v1/jobs"
	gateway_resource("/api/v1/mcp/tools") == "/mcp/tools"
	not gateway_resource("/api/v1/numbering/../x")
	not gateway_resource("/api/v2/numbering")
	not gateway_resource("/api/v1/Numbering")
}

# Envoy decides a D22 route on the service's own base, the path the duty catalogue names.
test_envoy_decides_a_d22_route_on_the_service_base if {
	answer := envoy with input as envoy_input("POST", "/api/v1/agents/a1/runs", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["agents.invoke"]}}
	answer.allowed == true
	answer.record.route == "POST /api/v1/agents/**"
}

test_envoy_decides_the_copilot_conversation_paths if {
	every public in ["/api/v1", "/v1"] {
		every rest in ["/agents/copilot/conversations", "/agents/copilot/conversations/c1/messages"] {
			path := concat("", [public, rest])
			allowed := envoy with input as envoy_input("POST", path, {"x-depot-tenant": "t", "x-depot-user": "u"})
				with data.assignments as {"t": {"u": ["mcp.copilot.use"]}}
			allowed.allowed == true
			allowed.record.duty == "mcp.copilot.use"
			allowed.record.route == "POST /api/v1/agents/**"
			refused := envoy with input as envoy_input("POST", path, {"x-depot-tenant": "t", "x-depot-user": "u"})
				with data.assignments as {"t": {"u": ["agents.invoke", "agents.run.read"]}}
			refused.allowed == false
			refused.http_status == 403
			anonymous := envoy with input as envoy_input("POST", path, {})
				with data.assignments as {"t": {"u": ["mcp.copilot.use"]}}
			anonymous.allowed == false
			anonymous.record.reason == "unauthenticated"
		}
	}
}

test_envoy_decides_the_agent_budget_paths if {
	every public in ["/api/v1", "/v1"] {
		every case in [
			{"method": "GET", "rest": "/agents/budgets", "duty": "agentbudget.read"},
			{"method": "PUT", "rest": "/agents/budgets/ap_coding", "duty": "agentbudget.write"},
		] {
			path := concat("", [public, case.rest])
			allowed := envoy with input as envoy_input(case.method, path, {"x-depot-tenant": "t", "x-depot-user": "u"})
				with data.assignments as {"t": {"u": [case.duty]}}
			allowed.allowed == true
			allowed.record.duty == case.duty
			refused := envoy with input as envoy_input(case.method, path, {"x-depot-tenant": "t", "x-depot-user": "u"})
				with data.assignments as {"t": {"u": ["agents.run.read"]}}
			refused.allowed == false
			refused.http_status == 403
			anonymous := envoy with input as envoy_input(case.method, path, {})
				with data.assignments as {"t": {"u": [case.duty]}}
			anonymous.allowed == false
			anonymous.record.reason == "unauthenticated"
		}
	}
}

agents_edge_ops := [op |
	some op in agents_api_ops
	not startswith(op[1], "/api/v1/knowledge/")
]

edge_headers := {"x-depot-tenant": "t", "x-depot-user": "u"}

test_envoy_decides_every_agents_api_path if {
	every op in agents_edge_ops {
		base := concat("/", array.slice(split(op[1], "/"), 0, 4))
		every path in [op[1], trim_prefix(op[1], "/api")] {
			every duty in op[2] {
				allowed := envoy with input as envoy_input(op[0], path, edge_headers)
					with data.assignments as {"t": {"u": [duty]}}
				allowed.allowed == true
				allowed.record.duty == duty
				allowed.record.route == concat("", [op[0], " ", base, "/**"])
			}
			refused := envoy with input as envoy_input(op[0], path, edge_headers)
				with data.assignments as {"t": {"u": ["numbering.read"]}}
			refused.allowed == false
			refused.http_status == 403
			anonymous := envoy with input as envoy_input(op[0], path, {})
				with data.assignments as {"t": {"u": op[2]}}
			anonymous.allowed == false
			anonymous.record.reason == "unauthenticated"
		}
	}
}

test_envoy_refuses_a_person_duty_on_the_workflow_step_route if {
	every public in ["/api/v1", "/v1"] {
		every duty in ["agents.invoke", "agents.run.read", "agents.admin"] {
			answer := envoy with input as envoy_input("POST", concat("", [public, "/agents/runs/r1/steps/plan"]), edge_headers)
				with data.assignments as {"t": {"u": [duty]}}
			answer.allowed == false
			answer.http_status == 403
		}
	}
}

test_the_knowledge_paths_have_no_duty_on_the_edge_base if {
	every path in ["/api/v1/knowledge/search", "/api/v1/knowledge/chunks"] {
		count(agents_api_grants("POST", gateway_resource(path))) == 0
	}
}

# One request for each D22 prefix (the routes Envoy rewrites to a base that is not /v1/<prefix>), with a duty the
# catalogue grants under that base. Both public paths are allowed with the duty and refused without it.
d22_cases := {
	"agents": {"method": "GET", "rest": "/budgets/b1", "duty": "agents.admin"},
	"copilot": {"method": "POST", "rest": "/chat", "duty": "mcp.copilot.use"},
	"cutover": {"method": "GET", "rest": "/plans", "duty": "migration.cutover.manage"},
	"edi": {"method": "GET", "rest": "/partners", "duty": "edi.admin"},
	"mcp": {"method": "POST", "rest": "", "duty": "mcp.copilot.use"},
	"migration": {"method": "POST", "rest": "/opening-targets/t1/p1/release", "duty": "migration.opening-target.release"},
	"print": {"method": "POST", "rest": "/jobs", "duty": "print.job.submit"},
}

test_d22_cases_cover_every_rewritten_prefix if object.keys(d22_cases) == object.keys(gateway_service_bases)

d22_answer(prefix, public, duties) := answer if {
	c := d22_cases[prefix]
	answer := envoy with input as envoy_input(c.method, concat("", [public, prefix, c.rest]), {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": duties}}
}

test_envoy_allows_every_d22_prefix_with_its_duty if {
	every prefix, c in d22_cases {
		every public in ["/api/v1/", "/v1/"] {
			answer := d22_answer(prefix, public, [c.duty])
			answer.allowed == true
			answer.record.duty == c.duty
			answer.record.route == concat("", [c.method, " ", gateway_service_bases[prefix], "/**"])
		}
	}
}

test_envoy_refuses_every_d22_prefix_without_its_duty if {
	every prefix, _ in d22_cases {
		every public in ["/api/v1/", "/v1/"] {
			answer := d22_answer(prefix, public, ["numbering.read"])
			answer.allowed == false
			answer.http_status == 403
		}
	}
}

# Every rewritten base has at least one duty permission under it, or Envoy would refuse that service to everyone (the mcp
# entry once named /api/v1/mcp while Envoy decides on /mcp). test_gateway.py checks every routed prefix the same way.
test_every_rewritten_base_has_a_duty_permission_under_it if {
	every _, base in gateway_service_bases {
		some _, spec in data.duties
		some permission in spec.permissions
		base_covers(base, permission.resource)
	}
}

base_covers(base, resource) if resource == base

base_covers(base, resource) if startswith(resource, concat("", [base, "/"]))

base_covers(base, resource) if startswith(resource, concat("", [base, ":"]))

# The rego pseudonym is logpseudo's: a vector computed with Go's logpseudo.PseudonymFor (fieldcrypt key of the bytes
# i*37+200, i = 0..31). services/identity/authztest compares more vectors against Go at run time.
pseudonym_key := "yO0SN1yBpsvwFTpfhKnO8xg9Yoes0fYbQGWKr9T5HkM="

test_log_pseudonym_matches_go if {
	log_pseudonym(pseudonym_key, "00000000-0000-0000-0000-000000000001", "  User-\u00c4B ") == "pn_901e02e95f689c7e"
}

test_envoy_record_carries_the_pseudonym_under_the_configured_key if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s1", {"x-depot-tenant": "00000000-0000-0000-0000-000000000001", "x-depot-user": "  User-\u00c4B "})
		with data.assignments as {"00000000-0000-0000-0000-000000000001": {"  User-\u00c4B ": ["numbering.read"]}}
		with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": pseudonym_key}}
	answer.record.subject == "pn_901e02e95f689c7e"
	not contains(json.marshal(answer.record), "User")
}

# No named rule holds the key (N3): the answer Envoy gets, and so the logged decision, never carries it, and the former
# log_pseudonym_key document is gone. services/identity/authztest evaluates the whole data.authz of the bundle's files
# with the key set (opa test cannot: data.authz holds these tests too).
test_no_answer_or_rule_holds_the_pseudonym_key if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s1", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
		with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": pseudonym_key}}
	startswith(answer.record.subject, "pn_")
	not contains(json.marshal(answer), pseudonym_key)
	not data.authz.log_pseudonym_key with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": pseudonym_key}}
}

test_log_subject_redacts_without_a_valid_key_or_tenant if {
	log_subject("t", "u") == "[redacted]" with opa.runtime as {"env": {}}
	log_subject("t", "u") == "[redacted]" with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": "c2hvcnQ="}}
	log_subject("", "u") == "[redacted]" with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": pseudonym_key}}
	log_subject("t", "") == "" with opa.runtime as {"env": {"DEPOT_LOG_PSEUDONYM_KEY": pseudonym_key}}
}

test_decision_names_the_granting_duty if {
	result := decision with input as {"subject": {"tenant_id": "t", "user_id": "u"}, "action": "GET", "resource": "/v1/numbering/sequences/s1"}
		with data.assignments as {"t": {"u": ["numbering.sequence.maintain", "numbering.read"]}}
	result == {"allow": true, "duty": "numbering.read"}
}

# The mask is the ext_authz OPA's own (deploy/gateway/authz-opa.yaml), never the bundle's: no authz rule may claim it.
test_the_bundle_carries_no_decision_log_mask if not data.authz.decision_log_mask
