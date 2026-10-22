module "cluster" {
  source        = "../../modules/cluster"
  provider_name = "eks"
  cluster_name  = "passage"
}

output "create_command" {
  value = module.cluster.create_command
}
