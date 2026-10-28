import os
import shutil
import stat
import subprocess
import tempfile
import unittest
import unittest.mock
from pathlib import Path

import stage_release


INITIAL_DIGEST = "sha256:" + "a" * 64
PROMOTED_DIGEST = "sha256:" + "b" * 64
STAGE_MANIFEST = Path("deploy/envs/stage/apps/hello-ledger.yaml")
PROD_MANIFEST = Path("deploy/envs/prod/apps/hello-ledger.yaml")


class StageReleaseTests(unittest.TestCase):
    def test_stage_application_sources_make_each_digest_change_history_visible(self):
        repository_root = Path(__file__).resolve().parents[2]
        root_app = (repository_root / "deploy/argocd/root-stage.yaml").read_text(
            encoding="utf-8"
        )
        stage_app = (
            repository_root / STAGE_MANIFEST
        ).read_text(encoding="utf-8")

        self.assertIn("name: root-stage", root_app)
        self.assertIn("targetRevision: main", root_app)
        self.assertIn("path: deploy/envs/stage/apps", root_app)
        self.assertIn("name: hello-ledger-stage", stage_app)
        self.assertIn("targetRevision: main", stage_app)
        self.assertIn("path: services/platform-health/charts/hello-ledger", stage_app)
        self.assertIn("name: stage", stage_app)
        self.assertEqual(stage_release.read_digest(stage_app), stage_release.PLACEHOLDER_DIGEST)

    def setUp(self):
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.manifest = self.root / STAGE_MANIFEST
        self.manifest.parent.mkdir(parents=True)
        self.manifest.write_text(
            "apiVersion: argoproj.io/v1alpha1\n"
            "spec:\n"
            "  source:\n"
            "    targetRevision: main\n"
            "    helm:\n"
            "      valuesObject:\n"
            "        image:\n"
            "          digest: " + INITIAL_DIGEST + "\n",
            encoding="utf-8",
        )
        self.run_git("init", "-q", "-b", "main")
        self.run_git("config", "user.name", "Passage test")
        self.run_git("config", "user.email", "passage-test@example.invalid")
        self.commit("Initial stage digest")
        self.run_git("checkout", "-qb", "promotion-test")
        self.stub_dir = self.root / "stub-bin"
        self.stub_dir.mkdir()
        self.cosign_log = self.root / "cosign.log"
        self.install_cosign_stub(fail_on="")
        path_patch = unittest.mock.patch.dict(
            os.environ, {"PATH": f"{self.stub_dir}{os.pathsep}{os.environ['PATH']}"}
        )
        path_patch.start()
        self.addCleanup(path_patch.stop)

    def install_cosign_stub(self, fail_on):
        stub = self.stub_dir / "cosign"
        stub.write_text(
            "#!/bin/sh\n"
            f'echo "$@" >> "{self.cosign_log}"\n'
            f'[ "$1" = "{fail_on}" ] && {{ echo "no matching signatures" >&2; exit 1; }}\n'
            "exit 0\n",
            encoding="utf-8",
        )
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)

    def cosign_calls(self):
        if not self.cosign_log.exists():
            return []
        return self.cosign_log.read_text(encoding="utf-8").splitlines()

    def tearDown(self):
        self.temporary_directory.cleanup()

    def run_git(self, *args):
        return subprocess.run(
            ["git", "-C", str(self.root), *args],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()

    def commit(self, message):
        self.run_git("add", str(STAGE_MANIFEST))
        self.run_git("commit", "-m", message)

    def digest(self):
        return stage_release.read_digest(self.manifest.read_text(encoding="utf-8"))

    def test_promote_and_rollback_create_argo_visible_git_revisions(self):
        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST],
            repo_root=self.root,
        )
        self.assertEqual(result, 0)
        self.assertEqual(self.digest(), PROMOTED_DIGEST)
        self.commit("Promote hello-ledger to stage")
        promoted_revision = self.run_git("rev-parse", "HEAD")

        result = stage_release.main(["rollback", "--env", "stage"], repo_root=self.root)
        self.assertEqual(result, 0)
        self.assertEqual(self.digest(), INITIAL_DIGEST)
        self.commit("Roll back hello-ledger stage digest")
        rollback_revision = self.run_git("rev-parse", "HEAD")

        revisions = self.run_git("log", "--format=%H", "-3", "--", str(STAGE_MANIFEST)).splitlines()
        self.assertEqual(revisions, [rollback_revision, promoted_revision, self.run_git("rev-parse", "HEAD~2")])
        history_digests = [
            stage_release.read_digest(
                self.run_git("show", f"{revision}:{STAGE_MANIFEST}")
            )
            for revision in reversed(revisions)
        ]
        self.assertEqual(history_digests, [INITIAL_DIGEST, PROMOTED_DIGEST, INITIAL_DIGEST])

    def test_promote_verifies_signature_and_sbom_attestation_before_writing(self):
        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST],
            repo_root=self.root,
        )

        self.assertEqual(result, 0)
        reference = f"{stage_release.IMAGE_REPOSITORY}@{PROMOTED_DIGEST}"
        identity = " ".join(stage_release.cosign_identity_args())
        self.assertEqual(
            self.cosign_calls(),
            [
                f"verify {identity} {reference}",
                f"verify-attestation --type spdxjson {identity} {reference}",
            ],
        )

    def test_promote_refuses_unsigned_image(self):
        self.install_cosign_stub(fail_on="verify")

        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST],
            repo_root=self.root,
        )

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_promote_refuses_image_without_sbom_attestation(self):
        self.install_cosign_stub(fail_on="verify-attestation")

        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST],
            repo_root=self.root,
        )

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_promote_refuses_when_cosign_is_missing(self):
        (self.stub_dir / "cosign").unlink()
        git_dir = os.path.dirname(shutil.which("git"))
        with unittest.mock.patch.dict(os.environ, {"PATH": f"{self.stub_dir}{os.pathsep}{git_dir}"}):
            result = stage_release.main(
                ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST],
                repo_root=self.root,
            )

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_skip_verify_bypasses_cosign_with_warning(self):
        self.install_cosign_stub(fail_on="verify")

        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", PROMOTED_DIGEST, "--skip-verify"],
            repo_root=self.root,
        )

        self.assertEqual(result, 0)
        self.assertEqual(self.digest(), PROMOTED_DIGEST)
        self.assertEqual(self.cosign_calls(), [])

    def test_promote_rejects_non_sha256_digest(self):
        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", "latest"],
            repo_root=self.root,
        )
        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_cli_refuses_changes_directly_on_main(self):
        self.run_git("checkout", "-q", "main")

        result = stage_release.main(
            [
                "promote",
                "--to",
                "stage",
                "--digest",
                PROMOTED_DIGEST,
            ],
            repo_root=self.root,
        )

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_promote_rejects_placeholder_digest(self):
        result = stage_release.main(
            [
                "promote",
                "--to",
                "stage",
                "--digest",
                stage_release.PLACEHOLDER_DIGEST,
            ],
            repo_root=self.root,
        )
        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), INITIAL_DIGEST)

    def test_prod_application_is_manual_sync_with_placeholder_digest(self):
        repository_root = Path(__file__).resolve().parents[2]
        root_app = (repository_root / "deploy/argocd/root-prod.yaml").read_text(encoding="utf-8")
        prod_app = (repository_root / PROD_MANIFEST).read_text(encoding="utf-8")

        self.assertIn("path: deploy/envs/prod/apps", root_app)
        self.assertIn("name: hello-ledger-prod", prod_app)
        self.assertIn("name: prod", prod_app)
        self.assertEqual(stage_release.read_digest(prod_app), stage_release.PLACEHOLDER_DIGEST)
        for app in (root_app, prod_app):
            self.assertNotIn("automated", app)
            self.assertNotIn("prune", app)

    def add_prod_manifest(self, stage_digest):
        prod = self.root / PROD_MANIFEST
        prod.parent.mkdir(parents=True)
        prod.write_text(
            self.manifest.read_text(encoding="utf-8").replace(INITIAL_DIGEST, stage_release.PLACEHOLDER_DIGEST),
            encoding="utf-8",
        )
        self.manifest.write_text(
            self.manifest.read_text(encoding="utf-8").replace(INITIAL_DIGEST, stage_digest),
            encoding="utf-8",
        )
        self.run_git("add", str(STAGE_MANIFEST), str(PROD_MANIFEST))
        self.run_git("commit", "-m", "Add prod")
        return prod

    def test_prod_promotes_the_digest_stage_pins(self):
        prod = self.add_prod_manifest(PROMOTED_DIGEST)

        result = stage_release.main(
            ["promote", "--to", "prod", "--digest", PROMOTED_DIGEST], repo_root=self.root
        )

        self.assertEqual(result, 0)
        self.assertEqual(stage_release.read_digest(prod.read_text(encoding="utf-8")), PROMOTED_DIGEST)
        self.assertEqual(self.digest(), PROMOTED_DIGEST)

    def test_prod_refuses_a_digest_stage_does_not_pin(self):
        prod = self.add_prod_manifest(PROMOTED_DIGEST)

        result = stage_release.main(
            ["promote", "--to", "prod", "--digest", "sha256:" + "c" * 64], repo_root=self.root
        )

        self.assertEqual(result, 2)
        self.assertEqual(
            stage_release.read_digest(prod.read_text(encoding="utf-8")), stage_release.PLACEHOLDER_DIGEST
        )

    def test_prod_rollback_restores_prior_prod_digest(self):
        prod = self.add_prod_manifest(PROMOTED_DIGEST)
        prod.write_text(
            prod.read_text(encoding="utf-8").replace(stage_release.PLACEHOLDER_DIGEST, INITIAL_DIGEST),
            encoding="utf-8",
        )
        self.run_git("add", str(PROD_MANIFEST))
        self.run_git("commit", "-m", "Prod first")
        prod.write_text(
            prod.read_text(encoding="utf-8").replace(INITIAL_DIGEST, PROMOTED_DIGEST), encoding="utf-8"
        )
        self.run_git("add", str(PROD_MANIFEST))
        self.run_git("commit", "-m", "Prod second")

        result = stage_release.main(["rollback", "--env", "prod"], repo_root=self.root)

        self.assertEqual(result, 0)
        self.assertEqual(stage_release.read_digest(prod.read_text(encoding="utf-8")), INITIAL_DIGEST)

    def test_rollback_refuses_without_prior_real_digest(self):
        self.manifest.write_text(
            self.manifest.read_text(encoding="utf-8").replace(
                INITIAL_DIGEST, stage_release.PLACEHOLDER_DIGEST
            ),
            encoding="utf-8",
        )
        self.commit("Set unpromoted placeholder")

        result = stage_release.main(["rollback", "--env", "stage"], repo_root=self.root)

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_cli_requires_clean_stage_manifest(self):
        self.manifest.write_text(
            self.manifest.read_text(encoding="utf-8").replace(
                INITIAL_DIGEST, PROMOTED_DIGEST
            ),
            encoding="utf-8",
        )

        result = stage_release.main(
            ["promote", "--to", "stage", "--digest", "sha256:" + "c" * 64],
            repo_root=self.root,
        )

        self.assertEqual(result, 2)
        self.assertEqual(self.digest(), PROMOTED_DIGEST)


if __name__ == "__main__":
    unittest.main()
