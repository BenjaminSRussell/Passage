variable "provider_kind" {
  description = "Cluster provider. Only k3d is implemented; the others are listed in services/platform-health/NOT-IMPLEMENTED.md."
  type        = string
  default     = "k3d"

  validation {
    condition     = contains(["k3d", "eks", "gke", "aks", "baremetal"], var.provider_kind)
    error_message = "provider_kind must be one of k3d, eks, gke, aks, baremetal."
  }
}

variable "name" {
  type    = string
  default = "passage-local"
}

variable "node_pools" {
  description = "Worker pools; k3d runs the summed count as agent nodes."
  type        = list(object({ name = string, count = number }))
  default     = [{ name = "default", count = 3 }]
}

variable "kubernetes_version" {
  description = "Kubernetes version such as 1.31.5; empty uses the provider default."
  type        = string
  default     = ""
}

variable "k3d_config_path" {
  type    = string
  default = "../../k3d/k3d.yaml"
}

variable "kubeconfig_path" {
  type    = string
  default = "~/.kube/config"
}

variable "argocd_chart_version" {
  type    = string
  default = "10.11.1"
}

variable "cert_manager_chart_version" {
  type    = string
  default = "v1.21.3"
}
