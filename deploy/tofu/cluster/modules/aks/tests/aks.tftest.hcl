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
      resource_versionless_id = "https://kv-stub.vault.azure.net/keys/fieldcrypt-kek"
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
      object_id = "00000000-0000-0000-0000-0000000000ee"
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
    }
  }

  assert {
    condition     = contains(output.private_dns_zone_names, "privatelink.postgres.database.azure.com") && contains(output.azurerm_private_endpoints, "postgres")
    error_message = "a Postgres private DNS zone and private endpoint are required with the managed server"
  }
}
