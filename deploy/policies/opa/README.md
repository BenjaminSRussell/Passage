# OPA sidecar bundle pull

`identity` compiles and signs the authz bundle (`policies/authz` rego and duty catalogue plus every active tenant-wide role assignment, keyed by tenant and IdP subject) and serves it at `GET /v1/identity/policy/bundle`. Each service's OPA sidecar polls it with `opa-sidecar.yaml`, verifies the RS256 signature, and keeps serving the last verified bundle (`persist: true`) if identity is down. A bundle that fails verification is never activated.

Secrets and keys, supplied per environment (not in git):

- identity: `IDENTITY_BUNDLE_TOKEN` (32+ characters), `IDENTITY_BUNDLE_SIGNING_KEY_PATH` (PEM RSA private key), `IDENTITY_BUNDLE_KEY_ID=identity-policy`, `IDENTITY_OPERATOR_TENANT_ID`.
- each pod that runs the sidecar: a Secret mounted at `/etc/passage/opa-bundle` holding `token` (the same bearer value) and `public.pem` (the matching public key).

Sidecar container fragment (add to every service pod that calls `RequireOPA`; the service's `IDENTITY_OPA_URL` stays `http://127.0.0.1:8181`):

```yaml
- name: opa
  image: openpolicyagent/opa:<digest-pinned>
  args: [run, --server, --addr=127.0.0.1:8181, --config-file=/etc/opa/config.yaml]
  volumeMounts:
    - {name: opa-config, mountPath: /etc/opa, readOnly: true}
    - {name: opa-bundle, mountPath: /etc/passage/opa-bundle, readOnly: true}
```

The bundle endpoint is a cluster-internal route: it has no `HTTPRoute` in `deploy/gateway` and must not get one.
