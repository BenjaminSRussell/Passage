locals {
  agents      = sum([for pool in var.node_pools : pool.count])
  k3s_image   = var.kubernetes_version == "" ? "" : "--image rancher/k3s:v${var.kubernetes_version}-k3s1"
  kube_config = { config_path = var.kubeconfig_path, config_context = "k3d-${var.name}" }
}

resource "null_resource" "cluster" {
  triggers = {
    name   = var.name
    agents = local.agents
    image  = local.k3s_image
    config = filesha256(var.k3d_config_path)
  }

  lifecycle {
    precondition {
      condition     = var.provider_kind == "k3d"
      error_message = "provider_kind ${var.provider_kind} is not implemented yet; see services/platform-health/NOT-IMPLEMENTED.md."
    }
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
  kubernetes = local.kube_config
}

resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = var.cert_manager_chart_version
  namespace        = "cert-manager"
  create_namespace = true
  wait             = true
  values           = [yamlencode({ crds = { enabled = true } })]
  depends_on       = [null_resource.cluster]
}

resource "helm_release" "argocd" {
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
  depends_on = [null_resource.cluster]
}
