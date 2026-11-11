output "resource_group" {
  value = azurerm_resource_group.this.name
}

output "cluster_name" {
  value = azurerm_kubernetes_cluster.this.name
}

output "kube_host" {
  value     = azurerm_kubernetes_cluster.this.kube_config[0].host
  sensitive = true
}

output "kube_cluster_ca_certificate" {
  value     = azurerm_kubernetes_cluster.this.kube_config[0].cluster_ca_certificate
  sensitive = true
}

output "oidc_issuer_url" {
  value = azurerm_kubernetes_cluster.this.oidc_issuer_url
}

output "acr_name" {
  value = azurerm_container_registry.this.name
}

output "acr_login_server" {
  value = azurerm_container_registry.this.login_server
}

output "key_vault_name" {
  value = azurerm_key_vault.this.name
}

output "fieldcrypt_key_id" {
  description = "Versionless Key Vault key id that wraps the fieldcrypt master keys."
  value       = azurerm_key_vault_key.fieldcrypt.resource_versionless_id
}

output "backup_storage_account" {
  value = azurerm_storage_account.backup.name
}

output "backup_storage_account_region" {
  value = azurerm_storage_account.backup.location
}

output "backup_container" {
  value = azurerm_storage_container.backup.name
}

output "external_secrets_identity_client_id" {
  value = azurerm_user_assigned_identity.workload["external_secrets"].client_id
}

output "backup_identity_client_id" {
  description = "backup.azure.workloadIdentityClientId for deploy/multi-region."
  value       = azurerm_user_assigned_identity.workload["backup"].client_id
}

output "fieldcrypt_identity_client_id" {
  value = azurerm_user_assigned_identity.workload["fieldcrypt"].client_id
}

output "azurerm_private_endpoints" {
  description = "Private endpoints created, keyed by service."
  value       = keys(azurerm_private_endpoint.this)
}

output "private_dns_zone_names" {
  description = "Private DNS zones linked to the VNet; derived from the cloud profile's endpoint suffixes."
  value       = toset([for z in azurerm_private_dns_zone.this : z.name])
}
