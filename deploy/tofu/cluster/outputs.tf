output "kubeconfig_path" {
  value      = var.kubeconfig_path
  depends_on = [null_resource.cluster, module.aks]
}

output "kubeconfig_context" {
  description = "k3d: the context name. aks: get credentials with `az aks get-credentials --resource-group <aks.resource_group> --name <aks.cluster_name>`."
  value       = local.is_aks ? var.name : "k3d-${var.name}"
  depends_on  = [null_resource.cluster, module.aks]
}

output "aks" {
  description = "Everything the charts need from the AKS build (resource group, ACR, Key Vault, backup storage, workload identity client ids). Null for other providers."
  value = local.is_aks ? {
    resource_group                      = module.aks[0].resource_group
    cluster_name                        = module.aks[0].cluster_name
    oidc_issuer_url                     = module.aks[0].oidc_issuer_url
    acr_login_server                    = module.aks[0].acr_login_server
    key_vault_name                      = module.aks[0].key_vault_name
    fieldcrypt_key_id                   = module.aks[0].fieldcrypt_key_id
    backup_storage_account              = module.aks[0].backup_storage_account
    backup_storage_account_region       = module.aks[0].backup_storage_account_region
    backup_container                    = module.aks[0].backup_container
    external_secrets_identity_client_id = module.aks[0].external_secrets_identity_client_id
    backup_identity_client_id           = module.aks[0].backup_identity_client_id
    fieldcrypt_identity_client_id       = module.aks[0].fieldcrypt_identity_client_id
    fieldcrypt_service_accounts         = module.aks[0].federated_fieldcrypt_service_accounts
    managed_hsm_name                    = module.aks[0].managed_hsm_name
    customer_managed_keys               = module.aks[0].customer_managed_keys
    node_pool_isolation                 = module.aks[0].node_pool_isolation
    hipaa_policy_assignment_id          = module.aks[0].hipaa_policy_assignment_id
  } : null
}
