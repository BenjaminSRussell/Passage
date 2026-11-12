locals {
  is_k3d      = var.provider_kind == "k3d"
  is_aks      = var.provider_kind == "aks"
  agents      = sum([for pool in var.node_pools : pool.count])
  k3s_image   = var.kubernetes_version == "" ? "" : "--image rancher/k3s:v${var.kubernetes_version}-k3s1"
  kube_config = { config_path = var.kubeconfig_path, config_context = "k3d-${var.name}" }
  # aks: the helm provider talks to the new cluster through the AAD-integrated kubeconfig values the module exports.
  helm_kubernetes = {
    config_path            = local.is_aks ? null : local.kube_config.config_path
    config_context         = local.is_aks ? null : local.kube_config.config_context
    host                   = local.is_aks ? one(module.aks[*].kube_host) : null
    cluster_ca_certificate = local.is_aks ? base64decode(nonsensitive(one(module.aks[*].kube_cluster_ca_certificate))) : null
    exec = local.is_aks ? {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "kubelogin"
      args        = ["get-token", "--login", "azurecli", "--server-id", "6dae42f8-4368-4678-94ff-3960e28e3630"]
    } : null
  }
}

# Only k3d and aks are implemented; any other provider_kind stops here with the pointer to the tracking file.
resource "terraform_data" "unsupported_provider" {
  count = contains(["k3d", "aks"], var.provider_kind) ? 0 : 1

  lifecycle {
    precondition {
      condition     = contains(["k3d", "aks"], var.provider_kind)
      error_message = "provider_kind ${var.provider_kind} is not implemented yet; see services/platform-health/NOT-IMPLEMENTED.md."
    }
  }
}

# One instance only for provider_kind = "aks": a k3d run never configures (or needs credentials for) Azure.
provider "azurerm" {
  alias    = "aks"
  for_each = local.is_aks ? toset(["aks"]) : toset([])

  features {}
  # The cloud the provider talks to: public, usgovernment or china, from the cloud profile (#602). Tokens and
  # endpoints are not interchangeable between them.
  environment                     = var.azure_environment
  subscription_id                 = var.azure_subscription_id
  resource_provider_registrations = "none"
}

module "aks" {
  count  = local.is_aks ? 1 : 0
  source = "./modules/aks"

  providers = {
    azurerm = azurerm.aks["aks"]
  }

  name                                        = var.name
  location                                    = var.azure_location
  allowed_regions                             = var.allowed_regions
  fips                                        = var.fips
  tenant_id                                   = var.azure_tenant_id
  kubernetes_version                          = var.kubernetes_version
  node_pools                                  = var.node_pools
  private_cluster                             = var.private_cluster
  customer_managed_keys                       = var.customer_managed_keys
  managed_hsm                                 = var.managed_hsm
  require_isolated_nodes                      = var.require_isolated_nodes
  isolated_nodes_regions                      = var.isolated_nodes_regions
  hipaa                                       = var.hipaa
  azure_policy_initiative_id                  = var.azure_policy_initiative_id
  fieldcrypt_service_accounts                 = var.fieldcrypt_service_accounts
  managed_hsm_admin_object_ids                = var.azure_managed_hsm_admin_object_ids
  managed_hsm_security_domain_certificate_ids = var.azure_managed_hsm_security_domain_certificate_ids
  backup_replication_type                     = var.azure_backup_replication_type
  postgres_flexible_server                    = var.azure_postgres_flexible_server

  postgres_admin_group_object_id = var.azure_postgres_admin_group_object_id
  cloud_endpoints = {
    blobSuffix              = lookup(var.cloud_endpoints, "blobSuffix", "")
    containerRegistrySuffix = lookup(var.cloud_endpoints, "containerRegistrySuffix", "")
    postgresSuffix          = lookup(var.cloud_endpoints, "postgresSuffix", "")
    keyVaultPrivateLinkZone = lookup(var.cloud_endpoints, "keyVaultPrivateLinkZone", "")
    managedHsmSuffix        = lookup(var.cloud_endpoints, "managedHsmSuffix", "")
  }
}

resource "null_resource" "cluster" {
  count = local.is_k3d ? 1 : 0

  triggers = {
    name   = var.name
    agents = local.agents
    image  = local.k3s_image
    config = filesha256(var.k3d_config_path)
  }

  provisioner "local-exec" {
    command = "k3d cluster create ${var.name} --config ${var.k3d_config_path} --agents ${local.agents} ${local.k3s_image} --wait"
  }

  provisioner "local-exec" {
    when    = destroy
    command = "k3d cluster delete ${self.triggers.name}"
  }
}

provider "helm" {
  kubernetes = local.helm_kubernetes
}

resource "helm_release" "cert_manager" {
  count            = var.install_cluster_addons ? 1 : 0
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.cert_manager_chart_version
  namespace        = "cert-manager"
  create_namespace = true
  wait             = true
  values           = [yamlencode({ crds = { enabled = true } })]
  depends_on       = [null_resource.cluster, module.aks]
}

resource "helm_release" "argocd" {
  count            = var.install_cluster_addons ? 1 : 0
  name             = "argocd"
  repository       = "https://argoproj.github.io/argo-helm"
  chart            = "argo-cd"
  version          = var.argocd_chart_version
  namespace        = "argocd"
  create_namespace = true
  wait             = true
  values = [yamlencode({
    configs = { cm = { "resource.customizations.health.argoproj.io_Application" = <<-LUA
      hs = {}
      hs.status = "Progressing"
      hs.message = ""
      if obj.status ~= nil and obj.status.health ~= nil then
        hs.status = obj.status.health.status
        if obj.status.health.message ~= nil then
          hs.message = obj.status.health.message
        end
      end
      return hs
    LUA
    } }
  })]
  depends_on = [null_resource.cluster, module.aks]
}
