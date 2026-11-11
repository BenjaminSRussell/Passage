variable "name" {
  description = "Cluster name; also the prefix of every Azure resource this module creates."
  type        = string
}

variable "location" {
  description = "Azure region, for example eastus2. Must be in allowed_regions when that list is not empty."
  type        = string
}

variable "allowed_regions" {
  description = "The cloud profile's allowedRegions (deploy/cloud-profiles/tofu/<profile>.tfvars.json). Empty means any region."
  type        = list(string)
  default     = []
}

variable "private_cluster" {
  description = "The cloud profile's privateCluster: the Kubernetes API server has no public endpoint."
  type        = bool
  default     = false
}

variable "customer_managed_keys" {
  description = "The cloud profile's customerManagedKeys: AKS (etcd, disks), storage, the registry, PostgreSQL and Log Analytics use keys in the customer's vault or HSM (cmk.tf)."
  type        = bool
  default     = false
}

variable "managed_hsm" {
  description = "The cloud profile's managedHsm: create a Managed HSM for the disk, storage and fieldcrypt keys. Needs customer_managed_keys."
  type        = bool
  default     = false
}

variable "managed_hsm_admin_object_ids" {
  description = "Entra object ids of the Managed HSM administrators (required with managed_hsm)."
  type        = list(string)
  default     = []
}

variable "managed_hsm_security_domain_certificate_ids" {
  description = "Key Vault certificate ids (at least 3 when used) that activate the Managed HSM security domain at creation. Empty leaves the HSM unactivated."
  type        = list(string)
  default     = []
}

variable "managed_hsm_security_domain_quorum" {
  type    = number
  default = 2
}

variable "log_analytics_commitment_gb" {
  description = "Commitment tier of the dedicated Log Analytics cluster that takes the customer-managed key (100 is the Azure minimum)."
  type        = number
  default     = 100
}

variable "log_retention_days" {
  type    = number
  default = 365
}

variable "require_isolated_nodes" {
  description = "The cloud profile's isolatedNodes: in isolated_nodes_regions every node pool must be an isolated size or on a dedicated host group (IL5)."
  type        = bool
  default     = false
}

variable "isolated_nodes_regions" {
  description = "Regions where isolation is required (the US Gov regions; US DoD East and Central need none)."
  type        = list(string)
  default     = []
}

variable "hipaa" {
  description = "The hipaa overlay: assign the HIPAA HITRUST Azure Policy initiative to the subscription."
  type        = bool
  default     = false
}

variable "azure_policy_initiative_id" {
  description = "Policy set definition id of the HIPAA HITRUST initiative (cloud profile hipaa overlay)."
  type        = string
  default     = ""
}

variable "fieldcrypt_service_accounts" {
  description = "Service accounts of the services that unwrap fieldcrypt keys. Each gets a federated credential on the fieldcrypt identity; the Helm charts create the same accounts (global.workloadIdentity)."
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

variable "fips" {
  description = "The cloud profile's fips flag: node pools use FIPS-enabled node images."
  type        = bool
  default     = false
}

variable "cloud_endpoints" {
  description = "The cloud profile's endpoint table. Only the suffixes below are read; no Azure hostname is written in this module."
  type = object({
    blobSuffix              = string
    containerRegistrySuffix = string
    postgresSuffix          = string
    keyVaultPrivateLinkZone = string
    managedHsmSuffix        = string
  })
}

variable "tenant_id" {
  description = "Entra tenant that owns the subscription (Key Vault and workload identity)."
  type        = string
}

variable "kubernetes_version" {
  type    = string
  default = ""
}

variable "node_pools" {
  description = <<-EOT
    Worker pools; the first is the AKS default (system) pool, the rest are user pools. vm_size overrides node_vm_size for
    the pool. isolated declares an isolated (dedicated-hardware) VM size; dedicated_host_group is the id of an Azure
    Dedicated Host group. A profile that requires IL5 isolation (dod, in the US Gov regions) refuses a pool that is
    neither.
  EOT
  type = list(object({
    name                 = string
    count                = number
    vm_size              = optional(string)
    isolated             = optional(bool, false)
    dedicated_host_group = optional(string, "")
  }))
  default = [{ name = "default", count = 3 }]
}

variable "node_vm_size" {
  type    = string
  default = "Standard_D4ds_v5"
}

variable "vnet_cidr" {
  type    = string
  default = "10.40.0.0/16"
}

variable "backup_replication_type" {
  description = "Storage replication for backups. ZRS keeps data inside the region; GRS replicates to the paired region, which is a different region for profiles that pin allowedRegions."
  type        = string
  default     = "ZRS"
  validation {
    condition     = contains(["LRS", "ZRS", "GRS", "RAGRS", "GZRS", "RAGZRS"], var.backup_replication_type)
    error_message = "backup_replication_type must be a storage replication type."
  }
}

variable "postgres_flexible_server" {
  description = "Create Azure Database for PostgreSQL Flexible Server with a private endpoint. Off by default: CNPG on AKS is the reference (deploy/multi-region)."
  type        = bool
  default     = false
}

variable "postgres_admin_group_object_id" {
  description = "Entra group made Flexible Server administrator when postgres_flexible_server is on (password authentication stays off)."
  type        = string
  default     = ""
}

variable "workload_identities" {
  description = "Service accounts that act as Azure identities through AKS workload identity."
  type = object({
    external_secrets = object({ namespace = string, service_account = string })
    backup           = object({ namespace = string, service_account = string })
  })
  default = {
    external_secrets = { namespace = "external-secrets", service_account = "external-secrets" }
    # CNPG names the instance service account after the cluster (deploy/multi-region postgres.clusterName).
    backup = { namespace = "passage-data", service_account = "passage-pg" }
  }
}

variable "tags" {
  type    = map(string)
  default = {}
}
