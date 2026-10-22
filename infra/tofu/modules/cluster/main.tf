# One module, every provider. k3d is created by the command in create_command
# (the same command make deploy-local runs). EKS, GKE, AKS, and bare-metal
# envs select themselves with provider_name; their provider resources are added
# in this module when a cloud account exists, behind the same variables.
locals {
  k3d_command = "k3d cluster create ${var.cluster_name} --agents ${var.agent_count} --port 18080:80@loadbalancer --wait"
  cloud_note  = "provider ${var.provider_name} uses the cluster module interface; configure that provider's credentials before apply"
}
