output "kubeconfig_path" {
  value      = var.kubeconfig_path
  depends_on = [null_resource.cluster]
}

output "kubeconfig_context" {
  value      = "k3d-${var.name}"
  depends_on = [null_resource.cluster]
}
