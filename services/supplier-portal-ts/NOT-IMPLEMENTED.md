# NOT-IMPLEMENTED — services/supplier-portal-ts (#511)

| Item | Status | Reason |
|---|---|---|
| Session BFF, OIDC with PKCE, vendor-scoped service tokens, `/api/v1` proxy for orders, ASNs, invoices, requests, documents | built | |
| Keycloak client `passage-portal-supplier` and roles | JSON in `deploy/keycloak/realm-passage.json`; not applied | live infra, tracked on #213: import the realm, set the client secret from the secret store, give each supplier user the attributes `tenant_id`, `legal_entity_id` and `vendor_id` and the role `supplier-user`, publish the key (`supplier-portal=...`) in `PASSAGE_SERVICE_PUBLIC_KEYS` of purchasing, receiving and party |
| Envoy route for `supplier.<env>.passage.dev` with rate limiting and WAF, Argo CD application, image build | not built | deploy/ and edge are live infra; the chart is `charts/supplier-portal` |
| Separate realm `suppliers` (PRD 20.12) | not built | the architecture note puts portal users in the tenant realm with portal roles (assumption A8); the client issues ID-token claims only and no `passage-api` audience, so its tokens are useless at the API gateway |
| Shared session store, more than one replica | not built | sessions are in memory; the chart runs one pod and `Recreate` |
| Persisted-query GraphQL (`/graphql`) from the architecture note | not built | the app uses the REST routes |
| Logout at Keycloak (end-session), refresh of the identity | not built | the session ends locally; the user signs in again after 8 h |
| Vendor-admin user invitations, MFA enrolment | not built | Keycloak user administration |
| Cross-vendor OPA decision log (AC-20.9) | partial | scoping is enforced in the services and tested there; the OPA decision log for it is not wired |
