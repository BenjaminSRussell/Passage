variable "provider_name" {
  type        = string
  description = "Cluster provider. k3d is the laptop path; eks, gke, aks, and baremetal share this module interface."

  validation {
    condition     = contains(["k3d", "eks", "gke", "aks", "baremetal"], var.provider_name)
    error_message = "provider_name must be k3d, eks, gke, aks, or baremetal."
  }
}

variable "cluster_name" {
  type        = string
  description = "Name of the Kubernetes cluster."
}

variable "agent_count" {
  type        = number
  description = "Worker nodes for the k3d laptop cluster. Cloud providers size nodes in their own env."
  default     = 1
}
