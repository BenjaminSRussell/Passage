# Cloud profiles

Passage runs in several Microsoft cloud environments. They differ in a few settings: which Entra ID authority and Graph root to trust, which Azure service DNS suffixes to call, whether binaries must use FIPS 140-3 validated cryptography, and which regions data may live in. A **cloud profile** is that set of settings, written down once. Tokens from one cloud are not accepted in another and an app is registered separately in each cloud's portal, so a deployment has exactly one profile and there is no runtime fallback list.

Tracking issues: #599 (the profile), #600 (Azure commercial), #601 (Microsoft 365 GCC); the GCC High, DoD, China, EU Data Boundary and HIPAA issues (#602 to #606) fill in the detail marked below.

## One value, defined once

The table is `deploy/cloud-profiles/profiles.json`. Nothing else spells an Azure hostname. A profile is a base profile plus optional overlays:

| Value | Meaning |
|---|---|
| `commercial` | Azure commercial (global) cloud. |
| `gcc` | Microsoft 365 GCC. A Microsoft 365 environment on the worldwide Entra ID, so the authority and Graph are the commercial ones (`login.microsoftonline.com`, `graph.microsoft.com`), never `.us`. Passage adds FIPS builds, US-only regions, a mirror-only registry and a restricted egress allowlist. See [gcc.md](gcc.md). |
| `gcc-high` | GCC High on Azure Government: `login.microsoftonline.us`, `graph.microsoft.us`. |
| `dod` | DoD on Azure Government: `login.microsoftonline.us`, `dod-graph.microsoft.us`. |
| `china` | Azure operated by 21Vianet: `login.partner.microsoftonline.cn` (alias `login.chinacloudapi.cn`), `microsoftgraph.chinacloudapi.cn`. |
| `+eu-data-boundary` | Overlay: only EU or EFTA regions. Cannot be combined with a profile whose regions are all elsewhere. |
| `+hipaa` | Overlay: removes egress to SaaS without a recorded business associate agreement (paging, hosted LLMs other than Azure OpenAI) and sets six-year audit retention. |

Every endpoint in the table carries the Microsoft Learn page it comes from. An endpoint marked `"status": "unverified"` is one those pages do not list (today the China container registry and Azure OpenAI suffixes); confirm it before that cloud ships.

Endpoint keys: `entraAuthority`, `appRegistrationPortal`, `graphRoot`, `armEndpoint`, `keyVaultSuffix`, `keyVaultPrivateLinkZone`, `blobSuffix`, `containerRegistrySuffix`, `postgresSuffix`, `azureOpenAISuffix`. A test fails when a profile lacks a key another has.

Besides endpoints each profile sets `fips`, `allowedRegions` (empty means any), the registry (`requireMirror`), egress (`llmProviders`, `enforceProviders`, `pagerDuty`), and the Keycloak realm minimums (`passwordHash`, `passwordMinLength`, `eventRetentionDays`).

## Where each part reads it

| Reader | Source | How |
|---|---|---|
| Helm charts | `deploy/cloud-profiles/values/<profile>.yaml` | `helm template ... -f deploy/cloud-profiles/values/gcc.yaml`: the `global` block (`cloudProfile`, `cloudOverlays`, `fips`, `imageRegistry`, `endpoints`, `egressAllowlist`, ...). |
| Go | `platform/go/cloudprofile` | `cloudprofile.FromEnv(os.Getenv)` reads `PASSAGE_CLOUD_PROFILE` (`gcc`, `gcc+hipaa`; unset is commercial, an unknown name is an error). |
| TypeScript | `services/agents-ts/src/cloudprofile` | `profileFromEnv(process.env)`, `assertProviderEndpoint(url)`. |
| Python | `deploy/cloud-profiles/cloudprofile.py` | `load()`, `resolve()`; used by `check_region_pair.py` and the render tests. |
| OpenTofu | `deploy/cloud-profiles/tofu/<profile>.tfvars.json` | `tofu apply -var provider_kind=aks -var-file=../../cloud-profiles/tofu/gcc.tfvars.json`. |

The Go, TypeScript, Helm and OpenTofu copies are generated. After editing `profiles.json`:

```
python3 deploy/cloud-profiles/cloudprofile.py sync          # rewrite the copies
UPDATE_GOLDEN=1 go test ./cloudprofile/                     # in platform/go: refresh deploy/keycloak/idp examples
```

`make -C deploy test-cloud-profiles` fails when a copy has drifted, and a repository scan fails when an Azure hostname appears in code, charts or config outside the table.

## What a profile switches

**Entra ID IdP.** `deploy/keycloak/idp/entra-oidc.<profile>.json` are rendered from the profile (authority, issuer, JWKS, userinfo through the Graph root); `go run ./cloudprofile/cmd/cloudprofile entra-idp -profile gcc-high -entra-tenant <guid> -client-id <id> -tenant-id <passage tenant>` renders one for a real tenant. Tests assert a gcc-high render never contains `login.microsoftonline.com` and a commercial or gcc render never names a `.us` or China authority. App registration steps: [entra-id-federation.md](entra-id-federation.md).

**FIPS.** With `fips: true`, Go images are built with `GOFIPS140=v1.0.0` (the frozen Go cryptographic module, CMVP certificate #5247): `docker build --build-arg GOFIPS140=v1.0.0 -f services/<name>/Dockerfile .`. Every service that serves through `httpx.Serve` runs `cloudprofile.CheckFIPS` first and refuses to start when the profile needs FIPS, or the binary was built with `GOFIPS140`, and `crypto/fips140.Enabled()` is false (for example `GODEBUG=fips140=off`, or an ordinary image deployed under `gcc`). Charts pass the profile to the pod as `PASSAGE_CLOUD_PROFILE`. Keycloak: `deploy/keycloak/Dockerfile.fips` adds the BouncyCastle FIPS jars Keycloak 26.0.8 builds against (checksum-pinned) and builds with `--features=fips --fips-mode=strict`; `deploy/keycloak-fips` is the kustomize overlay. The realm uses `pbkdf2-sha512` and 14-character passwords, which strict mode requires. Postgres on AKS: use a FIPS-enabled node pool (`fips = true` in the profile enables it) or a managed service. CI (`cloud-profiles` job) builds and boots the FIPS Go variant and boots the strict Keycloak image with the realm.

**Regions.** `allowedRegions` is enforced by `deploy/multi-region/tools/check_region_pair.py` (a region outside the list fails; a pair is checked with `--cloud-profile gcc` or a `region.cloudProfile` key in its files) and by the OpenTofu module (the `azure_location` variable and a precondition on the resource group).

**Egress.** The profile's allowlist is derived from its endpoints plus what it keeps (registry, LLM providers, paging). `deploy/cloud-profiles/test_egress_render.py` renders every chart under every profile (with a registry mirror in the profile's own registry suffix) and fails if any image registry or URL host is outside the allowlist. `agents-ts` enforces the same list on provider endpoints when the profile has `enforceProviders` (gcc, gcc-high, dod, china): Azure OpenAI in an allowed region is the documented default.

**Registry.** Every chart takes `global.imageRegistry` (a mirror such as `passage.azurecr.io`; `ghcr.io/benjaminsrussell/<name>` becomes `<registry>/<name>`). A profile with `requireMirror` sets `global.imageRegistryRequired`, and a chart then refuses to render without a mirror. Mirror with the cosign signatures (`cosign copy` or `oras cp -r`) and verify them in the cluster as before.

**Secrets.** Under a profile, `ExternalSecret` references are Key Vault names (letters, digits, dashes; no JSON property); see [azure-aks.md](azure-aks.md).

## Still to do per profile

- `gcc-high`, `dod`: confirm region lists (IL5 scope for DoD), registry and egress detail (#602, #603).
- `china`: confirm the ACR and Azure OpenAI suffixes, mirror and egress detail (#605).
- `eu-data-boundary`, `hipaa`: overlay detail, a rendered realm patch for the six-year event retention, BAA records (#604, #606).
- Live proof on a real cluster and tenant (#213); authorization paperwork (#512).
