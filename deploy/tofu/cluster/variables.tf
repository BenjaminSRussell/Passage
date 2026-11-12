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
  description = "Worker pools; k3d runs the summed count as agent nodes. For aks, vm_size, isolated and dedicated_host_group (IL5, #603) are described in modules/aks."
  type = list(object({
    name                 = string
    count                = number
    vm_size              = optional(string)
    isolated             = optional(bool, false)
    dedicated_host_group = optional(string, "")
  }))
  default = [{ name = "default", count = 3 }]
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

variable "azure_environment" {
  description = "Azure cloud the azurerm provider talks to; the cloud profile's deployment.azureEnvironment."
  type        = string
  default     = "public"

  validation {
    condition     = contains(["public", "usgovernment", "china"], var.azure_environment)
    error_message = "azure_environment must be public, usgovernment or china."
  }
}

variable "private_cluster" {
  type    = bool
  default = false
}

variable "customer_managed_keys" {
  type    = bool
  default = false
}

variable "managed_hsm" {
  type    = bool
  default = false
}

variable "require_isolated_nodes" {
  type    = bool
  default = false
}

variable "isolated_nodes_regions" {
  type    = list(string)
  default = []
}

variable "hipaa" {
  type    = bool
  default = false
}

variable "azure_policy_initiative_id" {
  type    = string
  default = ""
}

variable "fieldcrypt_service_accounts" {
  description = "Service accounts that unwrap fieldcrypt keys; empty uses the module default (hr, party, payables, receiving, sales, compliance, migration in namespace passage)."
  type        = list(object({ namespace = string, service_account = string }))
  default = [
    { namespace = "passage", service_account = "passage-hr" },
    { namespace = "passage", service_account = "passage-party" },
    { namespace = "passage", service_account = "passage-payables" },
    { namespace = "passage", service_account = "passage-receiving" },
    { namespace = "passage", service_account = "passage-sales" },
    { namespace = "passage", service_account = "passage-compliance" },
    { namespace = "passage", service_account = "passage-migration" },
  ]
}

variable "azure_managed_hsm_admin_object_ids" {
  type    = list(string)
  default = []
}

variable "azure_managed_hsm_security_domain_certificate_ids" {
  type    = list(string)
  default = []
}

variable "install_cluster_addons" {
  description = "Install cert-manager and Argo CD with the helm provider. A private cluster has no public API endpoint: run the apply from inside the VNet, or set false and install them from a runner that can reach the cluster."
  type        = bool
  default     = true
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
