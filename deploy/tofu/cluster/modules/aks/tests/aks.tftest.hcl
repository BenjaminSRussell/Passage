# Offline tests for the AKS module (#600, #601): the Azure provider is mocked, so no subscription,
# credentials or network are needed. Run: tofu -chdir=deploy/tofu/cluster/modules/aks init -backend=false && tofu -chdir=deploy/tofu/cluster/modules/aks test
# They prove the plan builds every resource the issue lists, that the cloud profile's allowed_regions is enforced,
# and that private DNS zones come from the profile's endpoint table. They do not prove an apply against Azure
# (live subscription runs are tracked on #213).

mock_provider "azurerm" {
  mock_resource "azurerm_kubernetes_cluster" {
    defaults = {
      id              = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.ContainerService/managedClusters/passage-test"
      oidc_issuer_url = "https://eastus2.oic.prod-aks.azure.com/00000000-0000-0000-0000-000000000002/stub/"
      kube_config     = [{ host = "https://stub.hcp.eastus2.azmk8s.io:443", cluster_ca_certificate = "c3R1Yg==", client_certificate = "", client_key = "", password = "", username = "" }]
    }
  }
  mock_resource "azurerm_user_assigned_identity" {
    defaults = {
      id           = "/subscriptions/s/resourceGroups/r/providers/Microsoft.ManagedIdentity/userAssignedIdentities/stub"
      client_id    = "00000000-0000-0000-0000-0000000000cc"
      principal_id = "00000000-0000-0000-0000-0000000000dd"
    }
  }
  mock_resource "azurerm_key_vault_key" {
    defaults = {
      id                      = "https://kv-stub.vault.azure.net/keys/stub/0123456789abcdef"
      versionless_id          = "https://kv-stub.vault.azure.net/keys/stub"
      resource_versionless_id = "https://kv-stub.vault.azure.net/keys/fieldcrypt-kek"
    }
  }
  mock_resource "azurerm_key_vault_managed_hardware_security_module" {
    defaults = {
      id      = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.KeyVault/managedHSMs/hsm-stub"
      hsm_uri = "https://hsm-stub.managedhsm.usgovcloudapi.net/"
    }
  }
  mock_resource "azurerm_key_vault_managed_hardware_security_module_key" {
    defaults = {
      id           = "https://hsm-stub.managedhsm.usgovcloudapi.net/keys/stub"
      versioned_id = "https://hsm-stub.managedhsm.usgovcloudapi.net/keys/stub/0123456789abcdef"
    }
  }
  mock_resource "azurerm_disk_encryption_set" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.Compute/diskEncryptionSets/des-stub"
    }
  }
  mock_resource "azurerm_log_analytics_cluster" {
    defaults = {
      id       = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.OperationalInsights/clusters/lac-stub"
      identity = { principal_id = "00000000-0000-0000-0000-0000000000a1", tenant_id = "00000000-0000-0000-0000-000000000002" }
    }
  }
  mock_resource "azurerm_log_analytics_workspace" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.OperationalInsights/workspaces/law-stub"
    }
  }
  mock_resource "azurerm_subscription_policy_assignment" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/providers/Microsoft.Authorization/policyAssignments/passage-hipaa-hitrust"
    }
  }
  mock_resource "azurerm_private_dns_zone" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.Network/privateDnsZones/stub.zone"
    }
  }
  mock_resource "azurerm_virtual_network" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.Network/virtualNetworks/vnet-stub"
    }
  }
  mock_resource "azurerm_subnet" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.Network/virtualNetworks/vnet-stub/subnets/stub"
    }
  }
  mock_resource "azurerm_key_vault" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.KeyVault/vaults/kv-stub"
    }
  }
  mock_resource "azurerm_storage_account" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.Storage/storageAccounts/ststub"
    }
  }
  mock_resource "azurerm_container_registry" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.ContainerRegistry/registries/acrstub"
    }
  }
  mock_resource "azurerm_postgresql_flexible_server" {
    defaults = {
      id = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-passage-test/providers/Microsoft.DBforPostgreSQL/flexibleServers/pgstub"
    }
  }
  mock_data "azurerm_client_config" {
    defaults = {
      subscription_id = "00000000-0000-0000-0000-000000000000"
      object_id       = "00000000-0000-0000-0000-0000000000ee"
    }
  }
}

variables {
  name       = "passage-test"
  tenant_id  = "00000000-0000-0000-0000-000000000002"
  node_pools = [{ name = "system", count = 3 }, { name = "work", count = 2 }]
}

run "commercial_builds_the_whole_stack" {
  command = plan

  variables {
    location = "eastus2"
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = output.cluster_name == "passage-test"
    error_message = "AKS cluster not created"
  }
  assert {
    condition     = output.acr_name != "" && output.key_vault_name != "" && output.backup_storage_account != ""
    error_message = "ACR, Key Vault and backup storage must exist"
  }
  assert {
    condition     = output.backup_container == "pg-backups"
    error_message = "backup container"
  }
  assert {
    condition     = toset(output.azurerm_private_endpoints) == toset(["vault", "blob", "registry"])
    error_message = "private endpoints for Key Vault, storage and the registry (Postgres only with the managed server)"
  }
  assert {
    condition     = azurerm_kubernetes_cluster.this.workload_identity_enabled && azurerm_kubernetes_cluster.this.oidc_issuer_enabled
    error_message = "AKS must run with workload identity"
  }
  assert {
    condition     = azurerm_storage_account.backup.shared_access_key_enabled == false && azurerm_key_vault.this.purge_protection_enabled
    error_message = "backups use workload identity only and Key Vault is purge protected"
  }
}

run "gcc_rejects_a_region_outside_the_us_list" {
  command = plan

  variables {
    location        = "westeurope"
    fips            = true
    allowed_regions = ["eastus", "eastus2", "centralus"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  expect_failures = [azurerm_resource_group.this]
}

run "gcc_accepts_us_regions_and_turns_fips_on" {
  command = plan

  variables {
    location        = "centralus"
    fips            = true
    allowed_regions = ["eastus", "eastus2", "centralus"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = azurerm_kubernetes_cluster.this.default_node_pool[0].fips_enabled && azurerm_kubernetes_cluster_node_pool.extra["work"].fips_enabled
    error_message = "a fips profile needs FIPS node pools"
  }
}

run "gov_cloud_zones_come_from_the_profile" {
  command = plan

  variables {
    location        = "usgovvirginia"
    fips            = true
    allowed_regions = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  assert {
    condition     = output.private_dns_zone_names == toset(["privatelink.vaultcore.usgovcloudapi.net", "privatelink.blob.core.usgovcloudapi.net", "privatelink.azurecr.us"])
    error_message = "private DNS zones must be derived from the cloud profile's endpoint table"
  }
}

run "managed_postgres_adds_its_private_endpoint" {
  command = plan

  variables {
    location                       = "eastus2"
    postgres_flexible_server       = true
    postgres_admin_group_object_id = "00000000-0000-0000-0000-0000000000ff"
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = contains(output.private_dns_zone_names, "privatelink.postgres.database.azure.com") && contains(output.azurerm_private_endpoints, "postgres")
    error_message = "a Postgres private DNS zone and private endpoint are required with the managed server"
  }
}

# ---- GCC High, DoD, HIPAA (#602 #603 #606) ----------------------------------------------------------------------------

run "gcc_high_builds_a_private_cluster_with_customer_managed_keys_in_a_managed_hsm" {
  command = plan

  variables {
    location                     = "usgovvirginia"
    fips                         = true
    private_cluster              = true
    customer_managed_keys        = true
    managed_hsm                  = true
    managed_hsm_admin_object_ids = ["00000000-0000-0000-0000-0000000000ff"]
    allowed_regions              = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  assert {
    condition     = azurerm_kubernetes_cluster.this.private_cluster_enabled && length(azurerm_kubernetes_cluster.this.key_management_service) == 1
    error_message = "a private cluster with the etcd KMS plugin on the customer-managed key"
  }
  assert {
    condition     = toset(keys(azurerm_key_vault_key.cmk)) == toset(["etcd", "acr", "postgres", "loganalytics"]) && toset(keys(azurerm_key_vault_managed_hardware_security_module_key.cmk)) == toset(["disk", "storage"])
    error_message = "etcd, registry, PostgreSQL and Log Analytics keys in the Premium vault; disk and storage keys in the Managed HSM"
  }
  assert {
    condition     = length(azurerm_storage_account.backup.customer_managed_key) == 1 && azurerm_storage_account.backup.infrastructure_encryption_enabled
    error_message = "backup storage takes its customer-managed key when the account is created"
  }
  assert {
    condition     = length(azurerm_container_registry.this.encryption) == 1 && length(azurerm_log_analytics_cluster_customer_managed_key.this) == 1 && azurerm_disk_encryption_set.this[0].auto_key_rotation_enabled
    error_message = "registry, Log Analytics and the disk encryption set use customer-managed keys"
  }
  assert {
    condition     = length(azurerm_key_vault_managed_hardware_security_module_key_rotation_policy.this) == 3 && alltrue([for k in azurerm_key_vault_key.cmk : length(k.rotation_policy) == 1])
    error_message = "every customer-managed key rotates on a policy (versionless ids, so rotation needs no apply)"
  }
  assert {
    condition     = azurerm_key_vault.this.network_acls[0].bypass == "AzureServices"
    error_message = "trusted services read the customer-managed keys"
  }
  assert {
    condition     = contains(output.azurerm_private_endpoints, "hsm") && contains(output.private_dns_zone_names, "privatelink.managedhsm.usgovcloudapi.net")
    error_message = "the Managed HSM has a private endpoint and a zone derived from the profile"
  }
  assert {
    condition     = length(azurerm_key_vault_key.fieldcrypt) == 0 && length(azurerm_key_vault_managed_hardware_security_module_key.fieldcrypt) == 1 && output.managed_hsm_name != ""
    error_message = "the fieldcrypt wrapping key lives in the Managed HSM"
  }
}

run "managed_hsm_needs_administrators" {
  command = plan

  variables {
    location              = "usgovvirginia"
    customer_managed_keys = true
    managed_hsm           = true
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  expect_failures = [azurerm_kubernetes_cluster.this]
}

run "commercial_without_customer_managed_keys_builds_none_of_them" {
  command = plan

  variables {
    location = "eastus2"
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = length(azurerm_key_vault_key.cmk) == 0 && length(azurerm_disk_encryption_set.this) == 0 && length(azurerm_log_analytics_cluster.this) == 0 && length(azurerm_key_vault_managed_hardware_security_module.this) == 0
    error_message = "no customer-managed key resources without the profile switch"
  }
  assert {
    condition     = length(azurerm_key_vault_key.fieldcrypt) == 1 && azurerm_key_vault.this.network_acls[0].bypass == "None" && !azurerm_kubernetes_cluster.this.private_cluster_enabled
    error_message = "the commercial defaults are unchanged"
  }
  assert {
    condition     = length(azurerm_key_vault_key.fieldcrypt[0].rotation_policy) == 1
    error_message = "the fieldcrypt wrapping key rotates"
  }
}

run "every_fieldcrypt_service_account_gets_a_federated_credential" {
  command = plan

  variables {
    location = "eastus2"
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = toset(output.federated_fieldcrypt_service_accounts) == toset(["passage-hr", "passage-party", "passage-payables", "passage-receiving", "passage-sales", "passage-compliance", "passage-migration"])
    error_message = "one federated credential per service that unwraps fieldcrypt keys"
  }
  assert {
    condition     = azurerm_federated_identity_credential.fieldcrypt["passage-hr"].subject == "system:serviceaccount:passage:passage-hr"
    error_message = "subject is namespace and service account"
  }
}

run "dod_in_the_us_gov_regions_refuses_a_non_isolated_vm_size" {
  command = plan

  variables {
    location               = "usgovvirginia"
    require_isolated_nodes = true
    isolated_nodes_regions = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  expect_failures = [azurerm_kubernetes_cluster.this]
}

run "dod_refuses_an_isolated_claim_on_a_size_that_is_not_isolated" {
  command = plan

  variables {
    location               = "usgovtexas"
    require_isolated_nodes = true
    isolated_nodes_regions = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    node_pools             = [{ name = "system", count = 3, vm_size = "Standard_D16ds_v5", isolated = true }]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  expect_failures = [azurerm_kubernetes_cluster.this]
}

run "dod_accepts_isolated_sizes_and_dedicated_host_groups" {
  command = plan

  variables {
    location               = "usgovarizona"
    require_isolated_nodes = true
    isolated_nodes_regions = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    node_pools = [
      { name = "system", count = 3, vm_size = "Standard_E104is_v5", isolated = true },
      { name = "work", count = 2, dedicated_host_group = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Compute/hostGroups/hg1" },
    ]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  assert {
    condition     = output.node_pool_isolation == { system = true, work = true }
    error_message = "both pools are isolated"
  }
  assert {
    condition     = azurerm_kubernetes_cluster.this.default_node_pool[0].vm_size == "Standard_E104is_v5" && azurerm_kubernetes_cluster_node_pool.extra["work"].host_group_id != null
    error_message = "vm_size and host group reach the node pools"
  }
}

run "the_us_dod_regions_need_no_extra_isolation" {
  command = plan

  variables {
    location               = "usdodeast"
    require_isolated_nodes = true
    isolated_nodes_regions = ["usgovvirginia", "usgovarizona", "usgovtexas"]
    cloud_endpoints = {
      blobSuffix              = "blob.core.usgovcloudapi.net"
      containerRegistrySuffix = "azurecr.us"
      postgresSuffix          = "postgres.database.usgovcloudapi.net"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.usgovcloudapi.net"
      managedHsmSuffix        = "managedhsm.usgovcloudapi.net"
    }
  }

  assert {
    condition     = output.cluster_name == "passage-test"
    error_message = "a non-isolated pool is accepted in US DoD East"
  }
}

run "hipaa_assigns_the_azure_policy_initiative_to_the_subscription" {
  command = plan

  variables {
    location                   = "eastus2"
    hipaa                      = true
    azure_policy_initiative_id = "a169a624-5599-4385-a696-c8d643089fab"
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  assert {
    condition     = azurerm_subscription_policy_assignment.hipaa[0].policy_definition_id == "/providers/Microsoft.Authorization/policySetDefinitions/a169a624-5599-4385-a696-c8d643089fab"
    error_message = "the initiative is a policy set definition at tenant scope"
  }
}

run "hipaa_needs_the_initiative_id_from_the_profile" {
  command = plan

  variables {
    location = "eastus2"
    hipaa    = true
    cloud_endpoints = {
      blobSuffix              = "blob.core.windows.net"
      containerRegistrySuffix = "azurecr.io"
      postgresSuffix          = "postgres.database.azure.com"
      keyVaultPrivateLinkZone = "privatelink.vaultcore.azure.net"
      managedHsmSuffix        = "managedhsm.azure.net"
    }
  }

  expect_failures = [azurerm_subscription_policy_assignment.hipaa]
}
