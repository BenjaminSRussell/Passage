# Customer-managed keys (#602 #603), on when the cloud profile says customerManagedKeys: AKS (etcd through the KMS
# plugin, OS and data disks through a disk encryption set), the backup storage account, the container registry,
# Azure Database for PostgreSQL and Log Analytics all encrypt with keys the customer controls.
#
# Where the keys live. Key Vault Premium keys are HSM-backed (RSA-HSM). A Managed HSM (FIPS 140-validated, single
# tenant) holds the keys of the resources whose Azure arguments accept a Managed HSM key (the disk encryption set,
# storage, and the fieldcrypt wrapping key); the etcd KMS plugin, the registry, PostgreSQL and Log Analytics take only
# a Key Vault key id, so theirs stay in the Premium vault. Every key has a rotation policy and the resources read
# the versionless key id (the key id without its version), so a new key version is picked up without an apply or a restart.

locals {
  # Key Vault keys that always live in the Premium vault; disk and storage move to the Managed HSM when one exists.
  vault_cmk_names = concat(
    ["etcd", "acr", "postgres", "loganalytics"],
    var.managed_hsm ? [] : ["disk", "storage"],
  )
  hsm_cmk_names = var.managed_hsm ? ["disk", "storage"] : []
  cmk_key_opts = {
    etcd = ["encrypt", "decrypt", "wrapKey", "unwrapKey"]
  }
  hsm_enabled = local.cmk && var.managed_hsm
}

# ---- identities that read the keys ----------------------------------------------------------------------------------

resource "azurerm_user_assigned_identity" "cmk" {
  count               = local.cmk ? 1 : 0
  name                = "id-${var.name}-cmk"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  tags                = local.tags
}

resource "azurerm_user_assigned_identity" "cluster" {
  count               = local.cmk ? 1 : 0
  name                = "id-${var.name}-cluster"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  tags                = local.tags
}

resource "azurerm_role_assignment" "cmk_vault" {
  for_each = local.cmk ? {
    cmk     = azurerm_user_assigned_identity.cmk[0].principal_id
    cluster = azurerm_user_assigned_identity.cluster[0].principal_id
    law     = azurerm_log_analytics_cluster.this[0].identity[0].principal_id
  } : {}
  scope                = azurerm_key_vault.this.id
  role_definition_name = each.key == "cluster" ? "Key Vault Crypto User" : "Key Vault Crypto Service Encryption User"
  principal_id         = each.value
}

# ---- Managed HSM ----------------------------------------------------------------------------------------------------

resource "azurerm_key_vault_managed_hardware_security_module" "this" {
  count                         = local.hsm_enabled ? 1 : 0
  name                          = "hsm-${local.compact}-${local.nonce}"
  location                      = azurerm_resource_group.this.location
  resource_group_name           = azurerm_resource_group.this.name
  sku_name                      = "Standard_B1"
  tenant_id                     = var.tenant_id
  admin_object_ids              = var.managed_hsm_admin_object_ids
  purge_protection_enabled      = true
  soft_delete_retention_days    = 90
  public_network_access_enabled = false
  tags                          = local.tags

  # The security domain is activated at creation when certificates are given. Without them the HSM is created
  # unactivated and its keys cannot be made until an operator downloads the security domain (docs/ops/dod-il5.md).
  security_domain_key_vault_certificate_ids = length(var.managed_hsm_security_domain_certificate_ids) > 0 ? var.managed_hsm_security_domain_certificate_ids : null
  security_domain_quorum                    = length(var.managed_hsm_security_domain_certificate_ids) > 0 ? var.managed_hsm_security_domain_quorum : null

  network_acls {
    default_action = "Deny"
    bypass         = "AzureServices"
  }
}

locals {
  # Built-in Managed HSM local RBAC role definitions (the ids are fixed by Microsoft).
  hsm_roles = {
    crypto_officer                 = "515eb02d-2335-4d2d-92f2-b1cbdf9c3778"
    crypto_user                    = "21dbd100-6940-42c2-9190-5d6cb909625b"
    crypto_service_encryption_user = "33413926-3206-4cdd-b39a-83574fe37a17"
  }
  hsm_role_prefix = "/Microsoft.KeyVault/providers/Microsoft.Authorization/roleDefinitions"
}

resource "azurerm_key_vault_managed_hardware_security_module_role_assignment" "deployer" {
  count              = local.hsm_enabled ? 1 : 0
  managed_hsm_id     = azurerm_key_vault_managed_hardware_security_module.this[0].id
  name               = uuidv5("dns", "${var.name}-hsm-deployer")
  scope              = "/keys"
  role_definition_id = "${local.hsm_role_prefix}/${local.hsm_roles.crypto_officer}"
  principal_id       = data.azurerm_client_config.current.object_id
}

resource "azurerm_key_vault_managed_hardware_security_module_role_assignment" "cmk" {
  count              = local.hsm_enabled ? 1 : 0
  managed_hsm_id     = azurerm_key_vault_managed_hardware_security_module.this[0].id
  name               = uuidv5("dns", "${var.name}-hsm-cmk")
  scope              = "/keys"
  role_definition_id = "${local.hsm_role_prefix}/${local.hsm_roles.crypto_service_encryption_user}"
  principal_id       = azurerm_user_assigned_identity.cmk[0].principal_id
}

resource "azurerm_key_vault_managed_hardware_security_module_role_assignment" "fieldcrypt" {
  count              = var.managed_hsm ? 1 : 0
  managed_hsm_id     = azurerm_key_vault_managed_hardware_security_module.this[0].id
  name               = uuidv5("dns", "${var.name}-hsm-fieldcrypt")
  scope              = "/keys"
  role_definition_id = "${local.hsm_role_prefix}/${local.hsm_roles.crypto_user}"
  principal_id       = azurerm_user_assigned_identity.workload["fieldcrypt"].principal_id
}

resource "azurerm_key_vault_managed_hardware_security_module_key" "cmk" {
  for_each       = toset(local.hsm_enabled ? local.hsm_cmk_names : [])
  name           = "cmk-${each.key}"
  managed_hsm_id = azurerm_key_vault_managed_hardware_security_module.this[0].id
  key_type       = "RSA-HSM"
  key_size       = 3072
  key_opts       = ["wrapKey", "unwrapKey"]

  depends_on = [azurerm_key_vault_managed_hardware_security_module_role_assignment.deployer]
}

resource "azurerm_key_vault_managed_hardware_security_module_key" "fieldcrypt" {
  count          = var.managed_hsm ? 1 : 0
  name           = "fieldcrypt-kek"
  managed_hsm_id = azurerm_key_vault_managed_hardware_security_module.this[0].id
  key_type       = "RSA-HSM"
  key_size       = 3072
  key_opts       = ["wrapKey", "unwrapKey"]

  depends_on = [azurerm_key_vault_managed_hardware_security_module_role_assignment.deployer]
}

resource "azurerm_key_vault_managed_hardware_security_module_key_rotation_policy" "this" {
  for_each = merge(
    { for k, v in azurerm_key_vault_managed_hardware_security_module_key.cmk : "cmk-${k}" => v.id },
    { for v in azurerm_key_vault_managed_hardware_security_module_key.fieldcrypt : "fieldcrypt" => v.id },
  )
  managed_hsm_key_id = each.value
  expire_after       = "P1Y"
  time_before_expiry = "P90D"
}

# ---- Key Vault keys -------------------------------------------------------------------------------------------------

resource "azurerm_key_vault_key" "cmk" {
  for_each     = toset(local.cmk ? local.vault_cmk_names : [])
  name         = "cmk-${each.key}"
  key_vault_id = azurerm_key_vault.this.id
  key_type     = "RSA-HSM"
  key_size     = 3072
  key_opts     = lookup(local.cmk_key_opts, each.key, ["wrapKey", "unwrapKey"])

  rotation_policy {
    expire_after         = "P1Y"
    notify_before_expiry = "P30D"
    automatic {
      time_before_expiry = "P90D"
    }
  }

  depends_on = [azurerm_role_assignment.deployer_key_officer]
}

# ---- disks: OS and data disks of every node pool --------------------------------------------------------------------

resource "azurerm_disk_encryption_set" "this" {
  count                     = local.cmk ? 1 : 0
  name                      = "des-${var.name}"
  location                  = azurerm_resource_group.this.location
  resource_group_name       = azurerm_resource_group.this.name
  key_vault_key_id          = var.managed_hsm ? azurerm_key_vault_managed_hardware_security_module_key.cmk["disk"].id : azurerm_key_vault_key.cmk["disk"].versionless_id
  encryption_type           = "EncryptionAtRestWithCustomerKey"
  auto_key_rotation_enabled = true
  tags                      = local.tags

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.cmk[0].id]
  }

  depends_on = [azurerm_role_assignment.cmk_vault, azurerm_key_vault_managed_hardware_security_module_role_assignment.cmk]
}

# ---- Log Analytics: a dedicated cluster is what takes a customer-managed key -------------------------------------------

resource "azurerm_log_analytics_cluster" "this" {
  count               = local.cmk ? 1 : 0
  name                = "lac-${var.name}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  size_gb             = var.log_analytics_commitment_gb
  tags                = local.tags

  identity {
    type = "SystemAssigned"
  }
}

resource "azurerm_log_analytics_cluster_customer_managed_key" "this" {
  count                    = local.cmk ? 1 : 0
  log_analytics_cluster_id = azurerm_log_analytics_cluster.this[0].id
  key_vault_key_id         = azurerm_key_vault_key.cmk["loganalytics"].id

  depends_on = [azurerm_role_assignment.cmk_vault]
}

resource "azurerm_log_analytics_workspace" "this" {
  count               = local.cmk ? 1 : 0
  name                = "law-${var.name}"
  location            = azurerm_resource_group.this.location
  resource_group_name = azurerm_resource_group.this.name
  sku                 = "PerGB2018"
  retention_in_days   = var.log_retention_days
  tags                = local.tags
}

resource "azurerm_log_analytics_linked_service" "cluster" {
  count               = local.cmk ? 1 : 0
  resource_group_name = azurerm_resource_group.this.name
  workspace_id        = azurerm_log_analytics_workspace.this[0].id
  write_access_id     = azurerm_log_analytics_cluster.this[0].id

  depends_on = [azurerm_log_analytics_cluster_customer_managed_key.this]
}
