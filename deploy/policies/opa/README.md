# OPA sidecar bundle pull

`identity` compiles and signs the authz bundle (`policies/authz` rego and duty catalogue plus every active tenant-wide role assignment, keyed by tenant and IdP subject) and serves it at `GET /v1/identity/policy/bundle`. Each service's OPA sidecar polls it with `opa-sidecar.yaml`, verifies the RS256 signature, and keeps serving the last verified bundle (`persist: true`) if identity is down. A bundle that fails verification is never activated.

Secrets and keys, supplied per environment (not in git):

- identity: `IDENTITY_BUNDLE_TOKEN` (32+ characters), `IDENTITY_BUNDLE_SIGNING_KEY_PATH` (PEM RSA private key, 2048 bits or more), `IDENTITY_BUNDLE_KEY_ID=identity-policy`, `IDENTITY_OPERATOR_TENANT_ID`, `IDENTITY_BUNDLE_ADDR` (for example `:8443`), `IDENTITY_BUNDLE_TLS_CERT_PATH` and `IDENTITY_BUNDLE_TLS_KEY_PATH`; `IDENTITY_BUNDLE_REGO_PATH` is optional (default: `authz.rego` beside the catalogue).
- each pod that runs the sidecar: a Secret mounted at `/etc/passage/opa-bundle` holding `token` (the same bearer value) and `ca.pem` (the CA that signed identity's bundle certificate), and an env var `OPA_BUNDLE_PUBLIC_KEY` from the same Secret holding the matching public key as one line with `\n` for newlines (OPA substitutes `${...}` before parsing the YAML, so it must be a single line inside the double quotes; `awk '{printf "%s\\n",$0}' public.pem` produces it).

Transport and exposure: the bundle is served over TLS (`https://`, verified against `ca.pem`), never plain http, because it carries a bearer token and every tenant's role assignments. It is served by a dedicated listener (`IDENTITY_BUNDLE_ADDR`, refused if equal to `HTTP_ADDR`) that serves only this route, exposed by its own cluster-internal Service `identity-bundle`; the public listener does not serve the route at all and `deploy/gateway/httproutes.yaml` has no identity route. Add a NetworkPolicy allowing only the sidecar pods to reach it when the identity chart lands.

No `decision_logs` are configured: console decision logging would write every input (subject and tenant) to pod logs. If decision logs are wanted later, add a `system.log.mask` policy that erases `/input/subject` first.

Sidecar container fragment (add to every service pod that calls `RequireOPA`; the service's `IDENTITY_OPA_URL` stays `http://127.0.0.1:8181`):

```yaml
- name: opa
  image: openpolicyagent/opa:<digest-pinned>
  args: [run, --server, --addr=127.0.0.1:8181, --config-file=/etc/opa/config.yaml]
  env:
    - name: OPA_BUNDLE_PUBLIC_KEY
      valueFrom: {secretKeyRef: {name: opa-bundle, key: public-key-oneline}}
  volumeMounts:
    - {name: opa-config, mountPath: /etc/opa, readOnly: true}
    - {name: opa-bundle, mountPath: /etc/passage/opa-bundle, readOnly: true}
```

The bundle endpoint must never get an `HTTPRoute` in `deploy/gateway`.
