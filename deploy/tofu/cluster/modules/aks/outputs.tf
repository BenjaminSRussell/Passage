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
  description = "Versionless key id that wraps the fieldcrypt master keys (Key Vault, or Managed HSM when managed_hsm is on)."
  value       = var.managed_hsm ? "${azurerm_key_vault_managed_hardware_security_module.this[0].hsm_uri}keys/${azurerm_key_vault_managed_hardware_security_module_key.fieldcrypt[0].name}" : azurerm_key_vault_key.fieldcrypt[0].resource_versionless_id
}

output "managed_hsm_name" {
  value = var.managed_hsm ? azurerm_key_vault_managed_hardware_security_module.this[0].name : ""
}

output "customer_managed_keys" {
  description = "Resource kind to the key that encrypts it (versionless ids). Empty without customer_managed_keys."
  value = var.customer_managed_keys ? merge(
    { for k, v in azurerm_key_vault_key.cmk : k => v.versionless_id },
    { for k, v in azurerm_key_vault_managed_hardware_security_module_key.cmk : k => v.id },
  ) : {}
}

output "node_pool_isolation" {
  description = "Per node pool: whether it is an isolated size or on a dedicated host group."
  value       = local.pool_isolated
}

output "hipaa_policy_assignment_id" {
  description = "Azure Policy assignment of the HIPAA HITRUST initiative (hipaa overlay); empty otherwise."
  value       = var.hipaa ? azurerm_subscription_policy_assignment.hipaa[0].id : ""
}

output "federated_fieldcrypt_service_accounts" {
  value = sort(keys(local.fieldcrypt_accounts))
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
