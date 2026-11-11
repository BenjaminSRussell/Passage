"""Render tests for the passage-multiregion chart. Run: python3 -m unittest discover -s deploy/multi-region

They call `helm template`, split the output into documents and assert what each region role renders.
Stdlib only; skipped when helm is not on PATH.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
CHART = HERE / "chart"
HELM = shutil.which("helm")


def render(values_file: str | None = None, *sets: str) -> list[str]:
    cmd = [HELM, "template", "mr", str(CHART)]
    if values_file:
        cmd += ["-f", str(HERE / "values" / values_file)]
    for s in sets:
        cmd += ["--set", s]
    out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
    # Drop comment lines so assertions see only rendered fields.
    out = "\n".join(line for line in out.splitlines() if not line.lstrip().startswith("#"))
    return [d for d in re.split(r"^---\s*$", out, flags=re.M) if re.search(r"^kind:", d, re.M)]


def kinds(docs: list[str]) -> list[str]:
    return sorted(re.search(r"^kind: (\S+)", d, re.M).group(1) for d in docs)


def doc(docs: list[str], kind: str) -> str:
    found = [d for d in docs if re.search(rf"^kind: {kind}$", d, re.M)]
    if len(found) != 1:
        raise AssertionError(f"expected one {kind}, found {len(found)}")
    return found[0]


@unittest.skipUnless(HELM, "helm not installed")
class TestRoles(unittest.TestCase):
    def test_primary_writes_with_quorum_and_no_replica(self) -> None:
        docs = render("eu-west.yaml")
        cluster = doc(docs, "Cluster")
        self.assertIn("method: any", cluster)
        self.assertIn("number: 1", cluster)
        self.assertIn("dataDurability: required", cluster)
        self.assertNotIn("replica:", cluster)
        self.assertNotIn("fencedInstances", cluster)
        self.assertIn("NOBYPASSRLS", cluster)
        self.assertNotIn("shared_preload_libraries", cluster)  # CNPG manages it; setting it is rejected
        self.assertIn("poolMode: transaction", doc(docs, "Pooler"))
        self.assertNotIn("NetworkPolicy", kinds(docs))

    def test_passive_is_an_async_replica_of_the_primary(self) -> None:
        cluster = doc(render("eu-central.yaml"), "Cluster")
        self.assertRegex(cluster, r"replica:\n\s+enabled: true")
        self.assertIn('host: "passage-pg-rw.eu-west.passage.internal"', cluster)
        self.assertIn("sslmode: verify-full", cluster)
        self.assertNotIn("synchronous:", cluster)
        self.assertNotIn("initdb", cluster)

    def test_fenced_stops_postgres_and_closes_its_pods(self) -> None:
        docs = render("eu-west.yaml", "region.role=FENCED")
        cluster = doc(docs, "Cluster")
        self.assertIn("cnpg.io/fencedInstances: '[\"*\"]'", cluster)
        self.assertNotIn("replica:", cluster)  # keeps the former primary's spec until re-seeded
        self.assertNotIn("Pooler", kinds(docs))
        policy = doc(docs, "NetworkPolicy")
        self.assertIn("cnpg.io/cluster: passage-pg", policy)
        self.assertIn("kubernetes.io/metadata.name: cnpg-system", policy)

    def test_game_day_fence_applies_to_a_primary(self) -> None:
        docs = render("eu-west.yaml", "fence.enabled=true")
        self.assertIn("fencedInstances", doc(docs, "Cluster"))
        self.assertIn("NetworkPolicy", kinds(docs))


@unittest.skipUnless(HELM, "helm not installed")
class TestBackup(unittest.TestCase):
    def test_primary_archives_wal_and_takes_scheduled_backups(self) -> None:
        docs = render("eu-west.yaml")
        cluster = doc(docs, "Cluster")
        self.assertIn("name: barman-cloud.cloudnative-pg.io", cluster)
        self.assertIn("isWALArchiver: true", cluster)
        self.assertIn("barmanObjectName: passage-pg-backup", cluster)
        store = doc(docs, "ObjectStore")
        self.assertIn("name: passage-pg-backup", store)
        self.assertIn('destinationPath: "s3://passage-pg-backups-eu-west/"', store)
        self.assertIn('retentionPolicy: "30d"', store)
        self.assertIn("key: ACCESS_SECRET_KEY", store)
        scheduled = doc(docs, "ScheduledBackup")
        self.assertIn('schedule: "0 0 2 * * *"', scheduled)
        self.assertIn("method: plugin", scheduled)
        self.assertRegex(scheduled, r"cluster:\n\s+name: passage-pg\s*$")

    def test_each_region_archives_to_its_own_bucket(self) -> None:
        self.assertIn("passage-pg-backups-eu-central", doc(render("eu-west.yaml", "region.id=eu-central"), "ObjectStore"))
        self.assertIn("s3://override/", doc(render("eu-west.yaml", "backup.destinationPath=s3://override/"), "ObjectStore"))

    def test_passive_replica_does_not_archive(self) -> None:
        docs = render("eu-central.yaml")
        self.assertNotIn("ObjectStore", kinds(docs))
        self.assertNotIn("ScheduledBackup", kinds(docs))
        self.assertNotIn("barman-cloud", doc(docs, "Cluster"))

    def test_disabled_renders_no_backup(self) -> None:
        docs = render("eu-west.yaml", "backup.enabled=false")
        self.assertNotIn("ObjectStore", kinds(docs))
        self.assertNotIn("barman-cloud", doc(docs, "Cluster"))
        self.assertNotIn("PassageBackupStale", doc(docs, "PrometheusRule"))

    def test_backup_alerts_page_on_archive_failure(self) -> None:
        rules = doc(render("eu-west.yaml"), "PrometheusRule")
        self.assertIn("> 93600", rules)
        self.assertIn("max(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp{", rules)
        self.assertNotIn("cnpg_collector_last_available_backup_timestamp", rules)
        self.assertIn("or absent(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp{", rules)
        self.assertIn("alert: PassageWalArchiveFailing", rules)
        self.assertIn("cnpg_pg_stat_archiver_last_failed_time", rules)

    def test_schema_rejects_bad_retention_and_schedule(self) -> None:
        for setting in ("backup.retentionPolicy=forever", "backup.schedule=daily"):
            proc = subprocess.run([HELM, "template", "mr", str(CHART), "--set", setting], capture_output=True, text=True)
            self.assertNotEqual(proc.returncode, 0, setting)
            self.assertIn("/backup/", proc.stderr)


@unittest.skipUnless(HELM, "helm not installed")
class TestMirrorMaker(unittest.TestCase):
    def test_mm2_runs_only_next_to_its_target(self) -> None:
        self.assertNotIn("KafkaMirrorMaker2", kinds(render("eu-west.yaml")))
        docs = render("eu-central.yaml")
        mm2 = doc(docs, "KafkaMirrorMaker2")
        self.assertIn('connectCluster: "eu-central"', mm2)
        self.assertIn('sourceCluster: "eu-west"', mm2)
        self.assertIn("namespace: kafka", mm2)
        self.assertIn("secretName: passage-cluster-ca-cert", mm2)
        self.assertIn("sync.group.offsets.enabled", mm2)
        self.assertIn("PodMonitor", kinds(docs))

    def test_swapping_direction_moves_mm2(self) -> None:
        swap = ("mm2.sourceAlias=eu-central", "mm2.targetAlias=eu-west")
        self.assertIn("KafkaMirrorMaker2", kinds(render("eu-west.yaml", *swap)))
        self.assertNotIn("KafkaMirrorMaker2", kinds(render("eu-central.yaml", *swap)))

    def test_every_region_has_the_mm2_user(self) -> None:
        for f in ("eu-west.yaml", "eu-central.yaml"):
            user = doc(render(f), "KafkaUser")
            self.assertIn("strimzi.io/cluster: passage", user)
            self.assertIn("type: scram-sha-512", user)


@unittest.skipUnless(HELM, "helm not installed")
class TestAlerts(unittest.TestCase):
    def setUp(self) -> None:
        self.rules = doc(render("eu-central.yaml"), "PrometheusRule")

    def test_every_alert_links_a_runbook_that_exists(self) -> None:
        alerts = re.findall(r"- alert: (\S+)", self.rules)
        urls = re.findall(r"runbook_url: https://docs.passage.dev/ops/runbooks/(\S+)", self.rules)
        self.assertEqual(len(alerts), len(urls))
        runbooks = HERE.parent.parent / "docs" / "ops" / "runbooks"
        for page in set(urls):
            self.assertTrue((runbooks / f"{page}.md").is_file(), page)

    def test_burn_rate_rules_are_recorded_here(self) -> None:
        for w in ("5m", "30m", "1h", "6h"):
            self.assertIn(f"record: passage:slo_burn_rate:{w}", self.rules)
        self.assertIn("/ (1 - 0.999)", self.rules)
        self.assertIn("passage:slo_burn_rate:1h > 14.4 and passage:slo_burn_rate:5m > 14.4", self.rules)

    def test_license_alerts_do_not_overlap(self) -> None:
        self.assertIn("<= 30 and min by (tenant_id) (passage_license_days_to_expiry) > 7", self.rules)
        self.assertIn("<= 7 and min by (tenant_id) (passage_license_days_to_expiry) > 1", self.rules)
        self.assertRegex(self.rules, r"expr: min by \(tenant_id\) \(passage_license_days_to_expiry\) <= 1\n")

    def test_cnpg_alerts_select_instance_pods(self) -> None:
        self.assertIn('pod=~"passage-pg-[0-9]+"', self.rules)


@unittest.skipUnless(HELM, "helm not installed")
class TestSchema(unittest.TestCase):
    def assert_rejected(self, setting: str, message: str) -> None:
        proc = subprocess.run(
            [HELM, "template", "mr", str(CHART), "--set", setting], capture_output=True, text=True
        )
        self.assertNotEqual(proc.returncode, 0, setting)
        self.assertIn(message, proc.stderr)

    def test_role_enum(self) -> None:
        self.assert_rejected("region.role=ACTIVE", "/region/role")

    def test_failover_lag_range(self) -> None:
        self.assert_rejected("failover.maxLagSeconds=10", "/failover/maxLagSeconds")
        self.assert_rejected("failover.maxLagSeconds=4000", "/failover/maxLagSeconds")

    def test_dns_ttl_range(self) -> None:
        self.assert_rejected("failover.dnsTtlSeconds=600", "/failover/dnsTtlSeconds")

    def test_mm2_lag_alert_floor(self) -> None:
        self.assert_rejected("mm2.lagAlertMessages=10", "/mm2/lagAlertMessages")


PROFILES = HERE.parent / "cloud-profiles" / "values"


def render_profile(values_file: str, profile: str, *sets: str, registry: str = "passage.azurecr.io") -> list[str]:
    """Render with a cloud profile's global values (deploy/cloud-profiles/values/<profile>.yaml) under a region file."""
    cmd = [HELM, "template", "mr", str(CHART), "-f", str(PROFILES / f"{profile}.yaml"), "-f", str(HERE / "values" / values_file)]
    if registry:
        cmd += ["--set", f"global.imageRegistry={registry}"]
    for s in sets:
        cmd += ["--set", s]
    out = subprocess.run(cmd, check=True, capture_output=True, text=True).stdout
    out = "\n".join(line for line in out.splitlines() if not line.lstrip().startswith("#"))
    return [d for d in re.split(r"^---\s*$", out, flags=re.M) if re.search(r"^kind:", d, re.M)]


@unittest.skipUnless(HELM, "helm not installed")
class TestAzureBlobBackups(unittest.TestCase):
    """#600 / #601: the US pair archives to Azure Blob in its own US region through barman-cloud."""

    ID = "backup.azure.workloadIdentityClientId=11111111-2222-3333-4444-555555555555"

    def test_primary_archives_to_blob_on_the_profile_suffix_with_workload_identity(self) -> None:
        docs = render_profile("us/eastus2.yaml", "commercial", self.ID)
        store = doc(docs, "ObjectStore")
        self.assertIn('destinationPath: "https://passagepgbkpeastus2.blob.core.windows.net/pg-backups/"', store)
        self.assertIn("inheritFromAzureAD: true", store)
        self.assertNotIn("s3Credentials", store)
        self.assertNotIn("ACCESS_KEY_ID", store)
        cluster = doc(docs, "Cluster")
        self.assertIn('azure.workload.identity/use: "true"', cluster)
        self.assertIn('azure.workload.identity/client-id: "11111111-2222-3333-4444-555555555555"', cluster)
        self.assertIn("isWALArchiver: true", cluster)

    def test_gcc_profile_keeps_backups_in_us_storage(self) -> None:
        store = doc(render_profile("us/eastus2.yaml", "gcc", self.ID), "ObjectStore")
        self.assertIn(".blob.core.windows.net/", store)
        self.assertNotIn("usgovcloudapi", store)
        self.assertNotIn("s3://", store)

    def test_gov_cloud_destination_uses_the_gov_blob_suffix(self) -> None:
        store = doc(render_profile("us/eastus2.yaml", "gcc-high", self.ID), "ObjectStore")
        self.assertIn("https://passagepgbkpeastus2.blob.core.usgovcloudapi.net/pg-backups/", store)

    def test_storage_key_secret_when_workload_identity_is_off(self) -> None:
        store = doc(render_profile("us/eastus2.yaml", "commercial", "backup.azure.useWorkloadIdentity=false"), "ObjectStore")
        self.assertIn("key: AZURE_STORAGE_ACCOUNT", store)
        self.assertIn("key: AZURE_STORAGE_KEY", store)
        self.assertNotIn("inheritFromAzureAD", store)
        self.assertNotIn("azure.workload.identity", doc(render_profile("us/eastus2.yaml", "commercial", "backup.azure.useWorkloadIdentity=false"), "Cluster"))

    def test_blob_needs_a_profile_and_a_storage_account(self) -> None:
        for args, message in (
            ([str(CHART), "-f", str(HERE / "values" / "us" / "eastus2.yaml"), "--set", self.ID], "global.endpoints.blobSuffix is required"),
            ([str(CHART), "-f", str(PROFILES / "commercial.yaml"), "-f", str(HERE / "values" / "us" / "eastus2.yaml"), "--set", "backup.azure.storageAccount=", "--set", "global.imageRegistry=r.azurecr.io", "--set", self.ID], "backup.azure.storageAccount is required"),
            ([str(CHART), "-f", str(PROFILES / "commercial.yaml"), "-f", str(HERE / "values" / "us" / "eastus2.yaml"), "--set", "global.imageRegistry=r.azurecr.io"], "workloadIdentityClientId is required"),
        ):
            proc = subprocess.run([HELM, "template", "mr", *args], capture_output=True, text=True)
            self.assertNotEqual(proc.returncode, 0, message)
            self.assertIn(message, proc.stderr)

    def test_passive_centralus_is_a_replica_and_does_not_archive(self) -> None:
        docs = render_profile("us/centralus.yaml", "gcc")
        self.assertNotIn("ObjectStore", kinds(docs))
        self.assertRegex(doc(docs, "Cluster"), r"replica:\n\s+enabled: true")

    def test_s3_is_still_the_default_store(self) -> None:
        self.assertIn('destinationPath: "s3://passage-pg-backups-eu-west/"', doc(render("eu-west.yaml"), "ObjectStore"))

    def test_schema_rejects_an_unknown_store(self) -> None:
        proc = subprocess.run([HELM, "template", "mr", str(CHART), "--set", "backup.store=gcs"], capture_output=True, text=True)
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("/backup/store", proc.stderr)


@unittest.skipUnless(HELM, "helm not installed")
class TestRegistryMirror(unittest.TestCase):
    def test_image_comes_from_the_mirror(self) -> None:
        cluster = doc(render_profile("us/eastus2.yaml", "commercial", TestAzureBlobBackups.ID, registry="passage.azurecr.io"), "Cluster")
        self.assertIn("imageName: passage.azurecr.io/cloudnative-pg/postgresql:17", cluster)

    def test_a_mirror_only_profile_fails_without_a_mirror(self) -> None:
        proc = subprocess.run(
            [HELM, "template", "mr", str(CHART), "-f", str(PROFILES / "gcc.yaml"), "-f", str(HERE / "values" / "us" / "eastus2.yaml"), "--set", TestAzureBlobBackups.ID],
            capture_output=True, text=True,
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("global.imageRegistry is required", proc.stderr)

    def test_default_render_is_unchanged(self) -> None:
        self.assertIn("imageName: ghcr.io/cloudnative-pg/postgresql:17", doc(render("eu-west.yaml"), "Cluster"))


if __name__ == "__main__":
    unittest.main()
