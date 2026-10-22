output "provider_name" {
  value = var.provider_name
}

output "create_command" {
  value = var.provider_name == "k3d" ? local.k3d_command : local.cloud_note
}
