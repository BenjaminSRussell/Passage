module "cluster" {
  source        = "../../modules/cluster"
  provider_name = "gke"
  cluster_name  = "passage"
}

output "create_command" {
  value = module.cluster.create_command
}
