"""The cloud profile and the OpenTofu module agree (#602 #603 #606). Run: python3 -m unittest discover -s deploy/cloud-profiles

- every key a generated `-var-file` sets is a declared root variable (an undeclared one is silently ignored by tofu),
- the root module passes the profile switches through to the AKS module,
- every Azure resource type the AKS module creates is mapped to a service, and under the hipaa overlay each of those
  services is on the business-associate-agreement list kept in the profile (so PHI-bearing Passage infrastructure
  uses only BAA-covered Azure services).
"""

from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cloudprofile as cp  # noqa: E402

TOFU = cp.REPO / "deploy" / "tofu" / "cluster"
MODULE = TOFU / "modules" / "aks"

# Azure resource type -> the Azure service whose BAA coverage applies. A new type fails the test until it is added here
# and its service is confirmed on Microsoft's in-scope list (kept in profiles.json with its source).
SERVICE_OF = {
    "azurerm_resource_group": "Azure Resource Manager",
    "azurerm_virtual_network": "Azure Virtual Network",
    "azurerm_subnet": "Azure Virtual Network",
    "azurerm_private_dns_zone": "Azure Private Link",
    "azurerm_private_dns_zone_virtual_network_link": "Azure Private Link",
    "azurerm_private_endpoint": "Azure Private Link",
    "azurerm_kubernetes_cluster": "Azure Kubernetes Service",
    "azurerm_kubernetes_cluster_node_pool": "Azure Kubernetes Service",
    "azurerm_container_registry": "Azure Container Registry",
    "azurerm_key_vault": "Azure Key Vault",
    "azurerm_key_vault_key": "Azure Key Vault",
    "azurerm_key_vault_managed_hardware_security_module": "Azure Managed HSM",
    "azurerm_key_vault_managed_hardware_security_module_key": "Azure Managed HSM",
    "azurerm_key_vault_managed_hardware_security_module_key_rotation_policy": "Azure Managed HSM",
    "azurerm_key_vault_managed_hardware_security_module_role_assignment": "Azure Managed HSM",
    "azurerm_storage_account": "Azure Storage",
    "azurerm_storage_container": "Azure Storage",
    "azurerm_postgresql_flexible_server": "Azure Database for PostgreSQL",
    "azurerm_postgresql_flexible_server_active_directory_administrator": "Azure Database for PostgreSQL",
    "azurerm_disk_encryption_set": "Azure Managed Disks",
    "azurerm_log_analytics_cluster": "Azure Monitor",
    "azurerm_log_analytics_cluster_customer_managed_key": "Azure Monitor",
    "azurerm_log_analytics_workspace": "Azure Monitor",
    "azurerm_log_analytics_linked_service": "Azure Monitor",
    "azurerm_user_assigned_identity": "Microsoft Entra ID",
    "azurerm_federated_identity_credential": "Microsoft Entra ID",
    "azurerm_role_assignment": "Microsoft Entra ID",
    "azurerm_subscription_policy_assignment": "Azure Policy",
}


def declared_variables(path: Path) -> set[str]:
    return set(re.findall(r'^variable "([^"]+)"', path.read_text(encoding="utf-8"), flags=re.M))


def module_resource_types() -> set[str]:
    types: set[str] = set()
    for tf in MODULE.glob("*.tf"):
        types |= set(re.findall(r'^resource "(azurerm_[a-z0-9_]+)"', tf.read_text(encoding="utf-8"), flags=re.M))
    return types


class TofuVarFilesTests(unittest.TestCase):
    def test_every_generated_variable_is_declared_by_the_root_module(self) -> None:
        declared = declared_variables(TOFU / "variables.tf")
        for name in cp.load()["profiles"]:
            keys = set(json.loads((cp.TOFU_DIR / f"{name}.tfvars.json").read_text(encoding="utf-8")))
            self.assertEqual(keys - declared, set(), f"{name}: variables the root module does not declare")

    def test_the_overlay_selector_output_is_declared_too(self) -> None:
        declared = declared_variables(TOFU / "variables.tf")
        out = json.loads(cp.tofu_vars(cp.resolve(cp.load(), "dod", ["hipaa"])))
        self.assertEqual(set(out) - declared, set())

    def test_the_root_module_passes_every_profile_switch_to_the_aks_module(self) -> None:
        main = (TOFU / "main.tf").read_text(encoding="utf-8")
        for var in ("private_cluster", "customer_managed_keys", "managed_hsm", "require_isolated_nodes", "isolated_nodes_regions", "hipaa", "azure_policy_initiative_id", "fips", "allowed_regions"):
            self.assertRegex(main, rf"\b{var}\s*=\s*var\.{var}\b", var)
        self.assertRegex(main, r"environment\s*=\s*var\.azure_environment")

    def test_azure_environment_matches_the_cloud_of_each_profile(self) -> None:
        expect = {"commercial": "public", "gcc": "public", "gcc-high": "usgovernment", "dod": "usgovernment", "china": "china"}
        for name, env in expect.items():
            got = json.loads((cp.TOFU_DIR / f"{name}.tfvars.json").read_text(encoding="utf-8"))["azure_environment"]
            self.assertEqual(got, env, name)


class BaaCoverageTests(unittest.TestCase):
    def test_every_resource_type_in_the_module_is_mapped_to_a_service(self) -> None:
        self.assertEqual(module_resource_types() - set(SERVICE_OF), set(), "map the new Azure resource type to its service in SERVICE_OF")

    def test_every_mapped_service_is_on_the_hipaa_baa_list(self) -> None:
        covered = set(cp.resolve(cp.load(), "commercial", ["hipaa"])["baaCoveredServices"])
        used = {SERVICE_OF[t] for t in module_resource_types() if t in SERVICE_OF}
        self.assertEqual(used - covered, set(), "an Azure service the module uses is not on the BAA-covered list in profiles.json")

    def test_the_list_is_only_there_with_the_overlay(self) -> None:
        self.assertEqual(cp.resolve(cp.load(), "gcc-high")["baaCoveredServices"], [])


if __name__ == "__main__":
    unittest.main()
