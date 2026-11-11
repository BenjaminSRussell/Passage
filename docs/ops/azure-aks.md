# Azure AKS (commercial)

`provider_kind = "aks"` in `deploy/tofu/cluster` builds Passage's Azure footprint (#600). The same module serves the other Azure clouds through the [cloud profile](cloud-profiles.md).

## What it creates

Resource group, VNet with a node subnet and a private-endpoint subnet, AKS (Azure CNI overlay with Cilium, OIDC issuer and workload identity on, local accounts off, FIPS node images when the profile says so), ACR (Premium, no public access, no admin user), Key Vault (premium, RBAC, purge protection, no public access) with an `RSA-HSM` key `fieldcrypt-kek` that only wraps and unwraps, a backup storage account (no shared keys, no public access, TLS 1.2, versioned) with container `pg-backups`, private endpoints and private DNS zones for the vault, storage and registry (and Postgres when `azure_postgres_flexible_server` is on), and three user-assigned identities with federated credentials: `external_secrets` (Key Vault Secrets User), `backup` (Storage Blob Data Contributor, for the CNPG service account `passage-pg`), `fieldcrypt` (Key Vault Crypto User on the wrapping key). Private DNS zone names come from the profile's endpoint table.

```
tofu -chdir=deploy/tofu/cluster init
tofu -chdir=deploy/tofu/cluster apply -var provider_kind=aks -var name=passage-prod \
  -var-file=../../cloud-profiles/tofu/commercial.tfvars.json \
  -var azure_tenant_id=<tenant> -var azure_subscription_id=<subscription> -var azure_location=eastus2
```

`azure_location` must be in the profile's `allowed_regions` (gcc: US regions only). The `aks` output lists the values the charts need (ACR login server, Key Vault name, backup storage account and region, identity client ids).

Tests: `make -C deploy test-tofu` (`tofu validate` plus offline `tofu test` with the Azure provider mocked: no subscription, credentials or network). A plan against a stub subscription is not possible: the azurerm provider authenticates when it is configured. The provider is only instantiated for `aks`, so a k3d run never needs Azure.

## Secrets: External Secrets with Key Vault

`deploy/secret-store` renders the `ClusterSecretStore` named `passage` (the name every chart's `externalSecret.storeName` defaults to) with `authType: WorkloadIdentity`, `vaultUrl` from `azureKeyVault.name` and the profile's Key Vault suffix, and the matching `environmentType` (`PublicCloud` for commercial and GCC, `USGovernmentCloud`, `ChinaCloud`). Install it after the External Secrets Operator, with the operator's service account annotated `azure.workload.identity/client-id: <external_secrets_identity_client_id>`.

```
helm upgrade --install passage-secret-store deploy/secret-store -f deploy/cloud-profiles/values/commercial.yaml \
  --set azureKeyVault.name=<vault> --set azureKeyVault.tenantId=<tenant>
```

Key Vault secret names allow only letters, digits and dashes and hold a plain value, so under a cloud profile (`global.secretStoreProvider: azure-key-vault`) the charts turn `passage/dev/identity/keycloak/acme` into the secret name `passage-dev-identity-keycloak-acme` and omit `property: value`. Store each secret under that name as a plain string. Without a profile the Vault paths are unchanged.

## fieldcrypt master keys wrapped by Key Vault

`FIELDCRYPT_KEYS` entries may be `id=wrapped:<base64>`: a 32-byte master key wrapped by the vault key (RSA-OAEP-256, or A256KW for a symmetric Managed HSM key). The service unwraps it once at start through `FIELDCRYPT_AZURE_KEY_ID` (`https://<vault>/keys/fieldcrypt-kek[/<version>]`, a host of the profile's Key Vault suffix) using its workload identity, and holds the plain key in memory only. The sealed-value envelope still names the master key id, so old values open and nothing is re-sealed by wrapping. Charts pass `global.fieldcryptKeyId` as `FIELDCRYPT_AZURE_KEY_ID`.

Create a wrapped key, and rotate:

```
cd platform/go
FIELDCRYPT_AZURE_KEY_ID=https://<vault>/keys/fieldcrypt-kek PASSAGE_CLOUD_PROFILE=commercial \
  go run ./fieldcrypt/cmd/fieldcrypt-wrap -id 2027-01          # prints id=wrapped:...
```

Rotation: wrap a new key under a new id; add its entry to the secret that fills `FIELDCRYPT_KEYS` and keep the old entries; set `FIELDCRYPT_CURRENT_KEY_ID` to the new id; roll the services; run the service's re-seal job until no value names the old id (`hr reseal-pii` today); then drop the old entry. Rotating the vault key itself: create a new key version, wrap the same master keys again under it, and keep the old version enabled until every deployed entry has been replaced.

Not yet wired per service: the pod needs the workload identity label `azure.workload.identity/use: "true"` and a service account annotated with the `fieldcrypt` identity's client id; add them to the chart of each service that holds field-encrypted data.

## Backups to Azure Blob

`deploy/multi-region` takes `backup.store: azure-blob`. With the cloud profile's values the destination is `https://<storageAccount>.<blob suffix>/<container>/`; authentication is workload identity (`backup.azure.useWorkloadIdentity`, `backup.azure.workloadIdentityClientId` = the `backup_identity_client_id` output), which labels the CNPG pods and annotates the instance service account. Without workload identity the chart reads `AZURE_STORAGE_ACCOUNT` and `AZURE_STORAGE_KEY` from `backup.credentialsSecret`.

The US pair is `deploy/multi-region/values/us/eastus2.yaml` (PRIMARY) and `centralus.yaml` (PASSIVE), each with a storage account in its own region:

```
python3 deploy/multi-region/tools/check_region_pair.py --values-dir deploy/multi-region/values/us --cloud-profile commercial
helm template mr deploy/multi-region/chart -f deploy/cloud-profiles/values/commercial.yaml \
  -f deploy/multi-region/values/us/eastus2.yaml --set global.imageRegistry=<acr>.azurecr.io \
  --set backup.azure.workloadIdentityClientId=<client id>
```

A restore drill with `tools/tenant-restore` against Blob storage needs a live subscription (#213).

## Images from ACR

Mirror the images and their signatures into the ACR and set `global.imageRegistry`; every chart pulls `<registry>/<image name>`. ACR has no public access: pods reach it through the private endpoint.
