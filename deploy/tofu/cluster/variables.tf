variable "provider_kind" {
  description = "Cluster provider. k3d (local) and aks (Azure, #600) are implemented; the others are listed in services/platform-health/NOT-IMPLEMENTED.md."
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

# ---- aks (#600) -------------------------------------------------------------------------------------------------
# Feed the cloud profile with -var-file=../../cloud-profiles/tofu/<profile>.tfvars.json (commercial, gcc, ...).

variable "cloud_profile" {
  description = "Cloud profile name (deploy/cloud-profiles). Informational here; the profile's data arrives through the variables below."
  type        = string
  default     = "commercial"
}

variable "cloud_overlays" {
  type    = list(string)
  default = []
}

variable "cloud_endpoints" {
  description = "The profile's endpoint table, from the generated tfvars file."
  type        = map(string)
  default     = {}
}

variable "allowed_regions" {
  description = "The profile's allowedRegions; location must be one of them when the list is not empty."
  type        = list(string)
  default     = []
}

variable "fips" {
  description = "The profile's fips flag: AKS node pools use FIPS-enabled images."
  type        = bool
  default     = false
}

variable "azure_location" {
  type    = string
  default = "eastus2"

  validation {
    condition     = length(var.allowed_regions) == 0 || contains(var.allowed_regions, var.azure_location)
    error_message = "azure_location is not in the cloud profile's allowed_regions; data, backups and keys must stay in those regions."
  }
}

variable "azure_tenant_id" {
  description = "Entra tenant of the subscription. Required for aks."
  type        = string
  default     = ""
}

variable "azure_subscription_id" {
  description = "Subscription to deploy into. CI plans against a stub value (a well-formed GUID with no credentials)."
  type        = string
  default     = "00000000-0000-0000-0000-000000000000"
}

variable "azure_postgres_flexible_server" {
  type    = bool
  default = false
}

variable "azure_postgres_admin_group_object_id" {
  type    = string
  default = ""
}

variable "azure_backup_replication_type" {
  type    = string
  default = "ZRS"
}
