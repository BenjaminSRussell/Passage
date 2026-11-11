# Offline tests for the root module (#600): provider_kind handling and the cloud profile's region check. The AKS
# module itself is tested in modules/aks/tests (the Azure provider is mocked there). Run: tofu -chdir=deploy/tofu/cluster test

mock_provider "helm" {}
mock_provider "null" {}

variables {
  name            = "passage-test"
  azure_tenant_id = "00000000-0000-0000-0000-000000000002"
}

run "other_providers_are_still_refused" {
  command = plan

  variables {
    provider_kind = "eks"
  }

  expect_failures = [terraform_data.unsupported_provider]
}

run "k3d_is_unchanged_and_builds_no_azure_module" {
  command = plan

  variables {
    provider_kind = "k3d"
  }

  assert {
    condition     = length(null_resource.cluster) == 1 && length(module.aks) == 0
    error_message = "k3d must still create the local cluster and nothing in Azure"
  }
}
