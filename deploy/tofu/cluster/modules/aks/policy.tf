# HIPAA / HITRUST (#606): the Azure Policy initiative for the Azure layer is assigned to the subscription, so the
# compliance state of every Passage resource is reported by Azure and can be exported as evidence
# (deploy/tofu/cluster/scripts/policy-compliance-report.sh). The initiative covers Azure only; Passage's own
# safeguards are in docs/ops/hipaa.md. The id comes from the cloud profile's hipaa overlay.

resource "azurerm_subscription_policy_assignment" "hipaa" {
  count                = var.hipaa ? 1 : 0
  name                 = "passage-hipaa-hitrust"
  display_name         = "Passage: HIPAA HITRUST"
  subscription_id      = "/subscriptions/${data.azurerm_client_config.current.subscription_id}"
  policy_definition_id = "/providers/Microsoft.Authorization/policySetDefinitions/${var.azure_policy_initiative_id}"
  description          = "Assigned by OpenTofu under the hipaa overlay of the Passage cloud profile."

  lifecycle {
    precondition {
      condition     = var.azure_policy_initiative_id != ""
      error_message = "hipaa needs azure_policy_initiative_id; take it from the cloud profile (python3 deploy/cloud-profiles/cloudprofile.py tofu-vars <profile>+hipaa)."
    }
  }
}
