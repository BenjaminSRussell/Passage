locals {
  tags       = merge({ "passage.dev/cluster" = var.name, "passage.dev/managed-by" = "opentofu" }, var.tags)
  compact    = substr(replace(var.name, "-", ""), 0, 14)
  nonce      = substr(sha1("${var.name}-${var.location}"), 0, 6)
  default_np = var.node_pools[0]
  cmk        = var.customer_managed_keys
  # Azure's isolated (dedicated-hardware) VM sizes: IL5 in the US Gov regions needs these or a dedicated host group
  # (https://learn.microsoft.com/en-us/azure/azure-government/documentation-government-impact-level-5). Review the list
  # against Microsoft's "isolation in the Azure public cloud" page when a size is added or retired.
  isolated_vm_sizes = [
    "Standard_E80ids_v4", "Standard_E80is_v4", "Standard_E104i_v5", "Standard_E104is_v5", "Standard_E104id_v5", "Standard_E104ids_v5",
    "Standard_M192is_v2", "Standard_M192ims_v2", "Standard_M192ids_v2", "Standard_M192idms_v2", "Standard_F72s_v2", "Standard_M128ms",
  ]
  isolation_required = var.require_isolated_nodes && contains(var.isolated_nodes_regions, var.location)
  pool_isolated      = { for p in var.node_pools : p.name => (p.dedicated_host_group != "" || (p.isolated && contains(local.isolated_vm_sizes, coalesce(p.vm_size, var.node_vm_size)))) }
  extra_pools        = { for p in slice(var.node_pools, 1, length(var.node_pools)) : p.name => p }
  # Private DNS zone names are derived from the cloud profile, never typed here.
  zones = {
    vault    = var.cloud_endpoints.keyVaultPrivateLinkZone
    blob     = "privatelink.${var.cloud_endpoints.blobSuffix}"
    registry = "privatelink.${var.cloud_endpoints.containerRegistrySuffix}"
    postgres = "privatelink.${var.cloud_endpoints.postgresSuffix}"
    hsm      = "privatelink.${var.cloud_endpoints.managedHsmSuffix}"
  }
  zone_names = [for k in keys(local.zones) : k if(k != "postgres" || var.postgres_flexible_server) && (k != "hsm" || var.managed_hsm)]
}

resource "azurerm_resource_group" "this" {
  name     = "rg-${var.name}"
  location = var.location
  tags     = local.tags

  lifecycle {
    precondition {
      condition     = length(var.allowed_regions) == 0 || contains(var.allowed_regions, var.location)
      error_message = "location ${var.location} is not in the cloud profile's allowed_regions (${join(", ", var.allowed_regions)}); data, backups and keys must stay in those regions."
    }
  }
}

# ---- network ----------------------------------------------------------------------------------------------------

resource "azurerm_virtual_network" "this" {
  name                = "vnet-${var.name}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  address_space       = [var.vnet_cidr]
  tags                = local.tags
}

resource "azurerm_subnet" "nodes" {
  name                 = "snet-nodes"
  resource_group_name  = azurerm_resource_group.this.name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [cidrsubnet(var.vnet_cidr, 4, 0)]
}

resource "azurerm_subnet" "private_endpoints" {
  name                              = "snet-private-endpoints"
  resource_group_name               = azurerm_resource_group.this.name
  virtual_network_name              = azurerm_virtual_network.this.name
  address_prefixes                  = [cidrsubnet(var.vnet_cidr, 8, 250)]
  private_endpoint_network_policies = "Enabled"
}

resource "azurerm_private_dns_zone" "this" {
  for_each            = toset(local.zone_names)
  name                = local.zones[each.key]
  resource_group_name = azurerm_resource_group.this.name
  tags                = local.tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "this" {
  for_each              = azurerm_private_dns_zone.this
  name                  = "link-${each.key}"
  resource_group_name   = azurerm_resource_group.this.name
  private_dns_zone_name = each.value.name
  virtual_network_id    = azurerm_virtual_network.this.id
  tags                  = local.tags
}

# ---- AKS with workload identity ---------------------------------------------------------------------------------

resource "azurerm_kubernetes_cluster" "this" {
  name                      = var.name
  location                  = azurerm_resource_group.this.location
  resource_group_name       = azurerm_resource_group.this.name
  dns_prefix                = var.name
  kubernetes_version        = var.kubernetes_version == "" ? null : var.kubernetes_version
  oidc_issuer_enabled       = true
  workload_identity_enabled = true
  local_account_disabled    = true
  private_cluster_enabled   = var.private_cluster
  private_dns_zone_id       = var.private_cluster ? "System" : null
  disk_encryption_set_id    = local.cmk ? azurerm_disk_encryption_set.this[0].id : null
  tags                      = local.tags

  default_node_pool {
    name                    = local.default_np.name
    node_count              = local.default_np.count
    vm_size                 = coalesce(local.default_np.vm_size, var.node_vm_size)
    vnet_subnet_id          = azurerm_subnet.nodes.id
    fips_enabled            = var.fips
    host_group_id           = local.default_np.dedicated_host_group == "" ? null : local.default_np.dedicated_host_group
    host_encryption_enabled = local.cmk
  }

  # The etcd KMS plugin reads its key with the cluster identity, which then has to be one we can grant on the vault.
  identity {
    type         = local.cmk ? "UserAssigned" : "SystemAssigned"
    identity_ids = local.cmk ? [azurerm_user_assigned_identity.cluster[0].id] : null
  }

  dynamic "key_management_service" {
    for_each = local.cmk ? [1] : []
    content {
      key_vault_key_id         = azurerm_key_vault_key.cmk["etcd"].id
      key_vault_network_access = "Private"
    }
  }

  lifecycle {
    # IL5 in the US Gov regions (#603): every node pool is an isolated size or sits on a dedicated host group.
    precondition {
      condition     = !local.isolation_required || alltrue(values(local.pool_isolated))
      error_message = "this profile needs IL5 isolation in ${var.location}: every node pool must set dedicated_host_group or isolated = true with one of the isolated VM sizes (${join(", ", local.isolated_vm_sizes)}); non-isolated pools: ${join(", ", [for n, ok in local.pool_isolated : n if !ok])}."
    }
    precondition {
      condition     = !var.managed_hsm || length(var.managed_hsm_admin_object_ids) > 0
      error_message = "managed_hsm needs managed_hsm_admin_object_ids (Entra object ids of the HSM administrators)."
    }
  }

  azure_active_directory_role_based_access_control {
    azure_rbac_enabled = true
    tenant_id          = var.tenant_id
  }

  network_profile {
    network_plugin      = "azure"
    network_plugin_mode = "overlay"
    network_policy      = "cilium"
    network_data_plane  = "cilium"
    service_cidr        = "10.41.0.0/16"
    dns_service_ip      = "10.41.0.10"
  }
}

resource "azurerm_kubernetes_cluster_node_pool" "extra" {
  for_each                = local.extra_pools
  name                    = each.value.name
  kubernetes_cluster_id   = azurerm_kubernetes_cluster.this.id
  node_count              = each.value.count
  vm_size                 = coalesce(each.value.vm_size, var.node_vm_size)
  vnet_subnet_id          = azurerm_subnet.nodes.id
  fips_enabled            = var.fips
  host_group_id           = each.value.dedicated_host_group == "" ? null : each.value.dedicated_host_group
  host_encryption_enabled = local.cmk
  tags                    = local.tags
}

# ---- container registry (images are mirrored here, cosign signatures copied with them) ---------------------------

resource "azurerm_container_registry" "this" {
  name                          = "acr${local.compact}${local.nonce}"
  location                      = azurerm_resource_group.this.location
  resource_group_name           = azurerm_resource_group.this.name
  sku                           = "Premium" # private endpoints need Premium
  admin_enabled                 = false
  public_network_access_enabled = false
  tags                          = local.tags

  # Customer-managed key (#603): the key is read through the cmk identity; versionless so rotation needs no apply.
  dynamic "identity" {
    for_each = local.cmk ? [1] : []
    content {
      type         = "UserAssigned"
      identity_ids = [azurerm_user_assigned_identity.cmk[0].id]
    }
  }
  dynamic "encryption" {
    for_each = local.cmk ? [1] : []
    content {
      key_vault_key_id   = azurerm_key_vault_key.cmk["acr"].versionless_id
      identity_client_id = azurerm_user_assigned_identity.cmk[0].client_id
    }
  }
}

resource "azurerm_role_assignment" "acr_pull" {
  scope                = azurerm_container_registry.this.id
  role_definition_name = "AcrPull"
  # try() only matters under the offline provider mock (tests/aks.tftest.hcl), which cannot fake a computed block.
  principal_id = try(azurerm_kubernetes_cluster.this.kubelet_identity[0].object_id, "00000000-0000-0000-0000-000000000000")
}

# ---- Key Vault: secrets for External Secrets, and the fieldcrypt key-wrapping key ----------------------------------

resource "azurerm_key_vault" "this" {
  name                          = "kv-${local.compact}-${local.nonce}"
  location                      = azurerm_resource_group.this.location
  resource_group_name           = azurerm_resource_group.this.name
  tenant_id                     = var.tenant_id
  sku_name                      = "premium" # HSM-backed keys
  rbac_authorization_enabled    = true
  purge_protection_enabled      = true
  soft_delete_retention_days    = 90
  public_network_access_enabled = false
  tags                          = local.tags

  # Storage, ACR, Log Analytics and the AKS KMS plugin read customer-managed keys as trusted Microsoft services.
  network_acls {
    default_action = "Deny"
    bypass         = local.cmk ? "AzureServices" : "None"
  }
}

# Wraps the fieldcrypt master keys (platform/go/fieldcrypt/azurekv). The key never leaves the HSM: a master key is
# wrapped once, stored as a wrapped blob, and unwrapped at service start.
resource "azurerm_key_vault_key" "fieldcrypt" {
  count        = var.managed_hsm ? 0 : 1
  name         = "fieldcrypt-kek"
  key_vault_id = azurerm_key_vault.this.id
  key_type     = "RSA-HSM"
  key_size     = 3072
  key_opts     = ["wrapKey", "unwrapKey"]

  # A new key version every year; a wrapped master key names the version that wrapped it, so rotating the vault
  # key does not stop a service from starting (docs/ops/azure-aks.md).
  rotation_policy {
    expire_after         = "P2Y"
    notify_before_expiry = "P60D"
    automatic {
      time_before_expiry = "P90D"
    }
  }

  depends_on = [azurerm_role_assignment.deployer_key_officer]
}

data "azurerm_client_config" "current" {}

resource "azurerm_role_assignment" "deployer_key_officer" {
  scope                = azurerm_key_vault.this.id
  role_definition_name = "Key Vault Crypto Officer"
  principal_id         = data.azurerm_client_config.current.object_id
}

# ---- backup storage (barman-cloud) -------------------------------------------------------------------------------

resource "azurerm_storage_account" "backup" {
  name                              = "st${local.compact}${local.nonce}"
  location                          = azurerm_resource_group.this.location
  resource_group_name               = azurerm_resource_group.this.name
  account_tier                      = "Standard"
  account_replication_type          = var.backup_replication_type
  account_kind                      = "StorageV2"
  min_tls_version                   = "TLS1_2"
  https_traffic_only_enabled        = true
  allow_nested_items_to_be_public   = false
  shared_access_key_enabled         = false # workload identity only: no storage key exists to leak
  public_network_access_enabled     = false
  infrastructure_encryption_enabled = local.cmk
  tags                              = local.tags

  # The key is set when the account is created: Microsoft requires the customer-managed key before any data is written.
  dynamic "identity" {
    for_each = local.cmk ? [1] : []
    content {
      type         = "UserAssigned"
      identity_ids = [azurerm_user_assigned_identity.cmk[0].id]
    }
  }
  dynamic "customer_managed_key" {
    for_each = local.cmk ? [1] : []
    content {
      # key_vault_key_id takes a Managed HSM key id too (managed_hsm_key_id is deprecated in azurerm 4).
      key_vault_key_id          = var.managed_hsm ? azurerm_key_vault_managed_hardware_security_module_key.cmk["storage"].id : azurerm_key_vault_key.cmk["storage"].versionless_id
      user_assigned_identity_id = azurerm_user_assigned_identity.cmk[0].id
    }
  }

  blob_properties {
    versioning_enabled = true
    delete_retention_policy {
      days = 30
    }
  }
}

resource "azurerm_storage_container" "backup" {
  name                  = "pg-backups"
  storage_account_id    = azurerm_storage_account.backup.id
  container_access_type = "private"
}

# ---- private endpoints --------------------------------------------------------------------------------------------

locals {
  endpoints = merge(
    {
      vault    = { resource = azurerm_key_vault.this.id, subresource = "vault" }
      blob     = { resource = azurerm_storage_account.backup.id, subresource = "blob" }
      registry = { resource = azurerm_container_registry.this.id, subresource = "registry" }
    },
    var.postgres_flexible_server ? { postgres = { resource = azurerm_postgresql_flexible_server.this[0].id, subresource = "postgresqlServer" } } : {},
    var.managed_hsm ? { hsm = { resource = azurerm_key_vault_managed_hardware_security_module.this[0].id, subresource = "managedhsm" } } : {},
  )
}

resource "azurerm_private_endpoint" "this" {
  for_each            = local.endpoints
  name                = "pe-${var.name}-${each.key}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  subnet_id           = azurerm_subnet.private_endpoints.id
  tags                = local.tags

  private_service_connection {
    name                           = "psc-${each.key}"
    private_connection_resource_id = each.value.resource
    subresource_names              = [each.value.subresource]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [azurerm_private_dns_zone.this[each.key].id]
  }
}

# ---- optional managed PostgreSQL ----------------------------------------------------------------------------------

resource "azurerm_postgresql_flexible_server" "this" {
  count                         = var.postgres_flexible_server ? 1 : 0
  name                          = "pg-${var.name}-${local.nonce}"
  location                      = azurerm_resource_group.this.location
  resource_group_name           = azurerm_resource_group.this.name
  version                       = "17"
  sku_name                      = "GP_Standard_D4ds_v5"
  storage_mb                    = 262144
  public_network_access_enabled = false
  zone                          = "1"
  tags                          = local.tags

  dynamic "identity" {
    for_each = local.cmk ? [1] : []
    content {
      type         = "UserAssigned"
      identity_ids = [azurerm_user_assigned_identity.cmk[0].id]
    }
  }
  dynamic "customer_managed_key" {
    for_each = local.cmk ? [1] : []
    content {
      key_vault_key_id                  = azurerm_key_vault_key.cmk["postgres"].versionless_id
      primary_user_assigned_identity_id = azurerm_user_assigned_identity.cmk[0].id
    }
  }

  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = false
    tenant_id                     = var.tenant_id
  }

  lifecycle {
    precondition {
      condition     = var.postgres_admin_group_object_id != ""
      error_message = "postgres_admin_group_object_id is required with postgres_flexible_server: password authentication is off."
    }
  }
}

resource "azurerm_postgresql_flexible_server_active_directory_administrator" "this" {
  count               = var.postgres_flexible_server ? 1 : 0
  server_name         = azurerm_postgresql_flexible_server.this[0].name
  resource_group_name = azurerm_resource_group.this.name
  tenant_id           = var.tenant_id
  object_id           = var.postgres_admin_group_object_id
  principal_name      = "passage-postgres-admins"
  principal_type      = "Group"
}

# ---- workload identities ------------------------------------------------------------------------------------------

locals {
  identities = {
    external_secrets = { role = "Key Vault Secrets User", scope = azurerm_key_vault.this.id }
    backup           = { role = "Storage Blob Data Contributor", scope = azurerm_storage_account.backup.id }
    fieldcrypt       = { role = "Key Vault Crypto User", scope = var.managed_hsm ? azurerm_key_vault.this.id : azurerm_key_vault_key.fieldcrypt[0].resource_versionless_id }
  }
  # The fieldcrypt identity may unwrap in every service account that holds field-encrypted data (one federated
  # credential per account; AKS workload identity matches namespace and service account exactly).
  fieldcrypt_accounts  = { for sa in var.fieldcrypt_service_accounts : sa.service_account => sa }
  federated_identities = { for k, v in local.identities : k => v if k != "fieldcrypt" }
}

resource "azurerm_user_assigned_identity" "workload" {
  for_each            = local.identities
  name                = "id-${var.name}-${replace(each.key, "_", "-")}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  tags                = local.tags
}

resource "azurerm_federated_identity_credential" "workload" {
  for_each                  = local.federated_identities
  name                      = "fic-${replace(each.key, "_", "-")}"
  user_assigned_identity_id = azurerm_user_assigned_identity.workload[each.key].id
  audience                  = ["api://AzureADTokenExchange"]
  issuer                    = azurerm_kubernetes_cluster.this.oidc_issuer_url
  subject                   = "system:serviceaccount:${var.workload_identities[each.key].namespace}:${var.workload_identities[each.key].service_account}"
}

resource "azurerm_federated_identity_credential" "fieldcrypt" {
  for_each                  = local.fieldcrypt_accounts
  name                      = "fic-fieldcrypt-${each.key}"
  user_assigned_identity_id = azurerm_user_assigned_identity.workload["fieldcrypt"].id
  audience                  = ["api://AzureADTokenExchange"]
  issuer                    = azurerm_kubernetes_cluster.this.oidc_issuer_url
  subject                   = "system:serviceaccount:${each.value.namespace}:${each.value.service_account}"
}

resource "azurerm_role_assignment" "workload" {
  for_each             = { for k, v in local.identities : k => v if !(k == "fieldcrypt" && var.managed_hsm) }
  scope                = each.value.scope
  role_definition_name = each.value.role
  principal_id         = azurerm_user_assigned_identity.workload[each.key].principal_id
}
