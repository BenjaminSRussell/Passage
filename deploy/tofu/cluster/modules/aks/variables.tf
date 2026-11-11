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
  description = "Worker pools; the first is the AKS default (system) pool, the rest are user pools."
  type        = list(object({ name = string, count = number }))
  default     = [{ name = "default", count = 3 }]
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
    fieldcrypt       = object({ namespace = string, service_account = string })
  })
  default = {
    external_secrets = { namespace = "external-secrets", service_account = "external-secrets" }
    # CNPG names the instance service account after the cluster (deploy/multi-region postgres.clusterName).
    backup     = { namespace = "passage-data", service_account = "passage-pg" }
    fieldcrypt = { namespace = "passage", service_account = "passage-fieldcrypt" }
  }
}

variable "tags" {
  type    = map(string)
  default = {}
}
