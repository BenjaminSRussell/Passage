# NOT-IMPLEMENTED: services/customer-portal-ts (#30)

| Item | Status | Reason |
|---|---|---|
| Session BFF, OIDC with PKCE, customer-scoped service tokens, read-only `/api/v1` proxy for invoices, open items, payments, statement, orders, returns | built | |
| Keycloak client `passage-portal-customer` and role `customer-user` | JSON in `deploy/keycloak/realm-passage.json`; not applied | live infra, tracked on #213: import the realm, set the client secret from the secret store, give each customer contact the attributes `tenant_id`, `legal_entity_id` and `customer_id` and the role `customer-user`, publish the key (`customer-portal=...`) in `PASSAGE_SERVICE_PUBLIC_KEYS` of receivables, billing and sales |
| Envoy route for `customer.<env>.passage.dev` with rate limiting and WAF, image build | not built | deploy/ and edge are live infra; the chart is `charts/customer-portal` and the dev values are in `deploy/envs/dev/values/customer-portal.yaml` |
| Contact invitation, MFA enrolment, contact to customer mapping | not built | Keycloak user administration; the realm is separate from supplier users by role only (assumption A8, as for the supplier portal) |
| Placing orders, requesting a return, paying by card or ACH (FR-30.14, FR-30.25, FR-30.26) | not built | the portal reads only; writes need their own service routes and duties, and payments need the payments gateway (separate service) |
| Invoice and statement PDF download, shipment tracking, documents | not built | need the document store (#512) and the carrier read API |
| Product names on order and return lines | not built | sales lines hold a product id only; the portal shows line number, quantity and amounts until a customer-facing product read exists |
| Bill-to customers (invoices whose invoice account is another party) | not built | an invoice is the contact's when its customer is; the invoice-account relation is not read |
| Shared session store, more than one replica | not built | sessions are in memory; the chart runs one pod and `Recreate` |
| Logout at Keycloak (end-session), refresh of the identity | not built | the session ends locally; the contact signs in again after 8 h |
