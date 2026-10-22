module "cluster" {
  source        = "../../modules/cluster"
  provider_name = "k3d"
  cluster_name  = "passage"
  agent_count   = 0
}

output "create_command" {
  value = module.cluster.create_command
}
