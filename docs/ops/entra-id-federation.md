# Entra ID federation

Each customer tenant signs in through its own Microsoft Entra ID tenant, federated into the tenant's Passage Keycloak realm as an OIDC identity provider (authorization code with PKCE). Endpoints come from the [cloud profile](cloud-profiles.md); this page is the app registration and the rules that keep the tenant boundary intact. Issues: #599, #600, #601.

## Rules

- The Passage `tid` is bound to the realm, never to the Entra `tid` claim. The rendered provider has a hardcoded `tenant_id` mapper (sync mode FORCE) set to the realm's Passage tenant. No identity provider mapper may write `tenant_id`, `legal_entity_id`, `duties` or `tier`, and none may copy the Entra `tid` claim; duties come from Passage role assignments. Tests: `TestEntraIdPTemplatesKeepMFAAndTokenClaimsLocal` in `services/identity/keycloak_test.go` and `platform/go/cloudprofile/idp_test.go`.
- The issuer is pinned to one Entra tenant GUID. `common`, `organizations`, `consumers` and domain names are refused, because they would accept any tenant.
- Conditional Access and MFA stay on the Entra side. Passage still applies `passage-post-broker-mfa` after the broker login (the provider's `postBrokerLoginFlowAlias`), unless the tenant chooses to trust Entra MFA by changing that flow.
- The client secret never appears in the export. Set it through the Keycloak admin API or from Key Vault; prefer a certificate or a federated credential where the tenant allows it.
- A GCC tenant uses the worldwide authority (`login.microsoftonline.com`); GCC High and DoD use `login.microsoftonline.us`. Register the app in the portal of the tenant's own cloud (`portal.azure.com`, `portal.azure.us`, `portal.azure.cn`).

## Steps

1. In the customer's Entra admin center, **App registrations > New registration**. Single tenant. Redirect URI (Web): `https://<keycloak host>/realms/<realm>/broker/entra-oidc/endpoint`.
2. Note the Application (client) ID and the Directory (tenant) ID.
3. **Certificates and secrets**: add a client secret (or a certificate / federated credential). Copy the value once.
4. **API permissions**: Microsoft Graph delegated `openid`, `profile`, `email` (the defaults). Grant admin consent if the tenant requires it.
5. **Token configuration**: add the optional claims `email`, `given_name`, `family_name` to the ID token if the tenant's policy removes them. Group claims are not used for authorization.
6. Render the provider for the deployment's cloud profile:

   ```
   cd platform/go
   go run ./cloudprofile/cmd/cloudprofile entra-idp -profile gcc \
     -entra-tenant <directory id> -client-id <application id> -tenant-id <passage tenant id> > entra-idp.json
   ```

7. Create the provider and its mappers in the realm (`identityProvider` to `POST /admin/realms/<realm>/identity-provider/instances`, each entry of `identityProviderMappers` to `.../instances/entra-oidc/mappers`), then set the client secret on the provider (`config.clientSecret`) through the same API.
8. Sign in once through the realm's login page; the second factor runs after the broker login. The token's `tid` is the Passage tenant.

`deploy/keycloak/idp/entra-oidc.<profile>.json` are rendered examples with placeholders; they ship disabled.

## Not proven from this repository

A live federated login against a real Entra test tenant producing a token `services/identity/jwt.go` accepts is recorded on #213; this repository tests the rendered provider, the mapper rules and, against a local Keycloak, brokered logins through Keycloak stand-ins.
