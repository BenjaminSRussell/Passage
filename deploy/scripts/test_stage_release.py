import contextlib
import http.server
import io
import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import threading
import time
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
        for variable in (stage_release.API_URL_VARIABLE, stage_release.API_TOKEN_VARIABLE):
            environment_patch = unittest.mock.patch.dict(os.environ)
            environment_patch.start()
            self.addCleanup(environment_patch.stop)
            os.environ.pop(variable, None)

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

    def test_environment_applications_have_unique_names_and_own_destinations(self):
        repository_root = Path(__file__).resolve().parents[2]
        names = []
        for env in ("dev", "stage", "prod"):
            for manifest in sorted((repository_root / f"deploy/envs/{env}/apps").glob("*.yaml")):
                for document in manifest.read_text(encoding="utf-8").split("\n---\n"):
                    found = re.search(r"kind: Application\nmetadata:\n  name: (\S+)", document)
                    if found is None:
                        continue
                    names.append(found.group(1))
                    if env != "dev":
                        self.assertRegex(document, rf"destination:\n    name: {env}\n", manifest.name)
                        self.assertNotIn("kubernetes.default.svc", document, manifest.name)
        self.assertEqual(len(names), len(set(names)), names)

    def test_prod_applications_match_stage_sync_policy_and_placeholder_digest(self):
        repository_root = Path(__file__).resolve().parents[2]
        automated = "    automated:\n      prune: true\n      selfHeal: true\n"
        for relative in (
            "deploy/argocd/root-stage.yaml",
            "deploy/argocd/root-prod.yaml",
            STAGE_MANIFEST.as_posix(),
            PROD_MANIFEST.as_posix(),
            "deploy/envs/stage/apps/kyverno.yaml",
            "deploy/envs/prod/apps/kyverno.yaml",
        ):
            text = (repository_root / relative).read_text(encoding="utf-8")
            self.assertEqual(text.count("  syncPolicy:\n"), text.count(automated), relative)
            self.assertGreater(text.count(automated), 0, relative)
        root_app = (repository_root / "deploy/argocd/root-prod.yaml").read_text(encoding="utf-8")
        prod_app = (repository_root / PROD_MANIFEST).read_text(encoding="utf-8")
        self.assertIn("path: deploy/envs/prod/apps", root_app)
        self.assertIn("name: hello-ledger-prod", prod_app)
        self.assertEqual(stage_release.read_digest(prod_app), stage_release.PLACEHOLDER_DIGEST)

    def publish_main(self, stage=None, prod=None, at=None, note=""):
        self.run_git("checkout", "-q", "main")
        template = self.manifest.read_text(encoding="utf-8")
        if stage is not None:
            self.manifest.write_text(
                self.with_digest(template, stage) + (f"# {note}\n" if note else ""), encoding="utf-8"
            )
        if prod is not None:
            prod_path = self.root / PROD_MANIFEST
            prod_path.parent.mkdir(parents=True, exist_ok=True)
            prod_path.write_text(self.with_digest(template, prod), encoding="utf-8")
        self.run_git("add", str(STAGE_MANIFEST), *([str(PROD_MANIFEST)] if prod is not None else []))
        committer_date = {"GIT_COMMITTER_DATE": f"{int(at)} +0000"} if at is not None else {}
        with unittest.mock.patch.dict(os.environ, committer_date):
            self.run_git("commit", "-q", "-m", "Publish to main")
        if "origin" not in self.run_git("remote").split():
            origin = tempfile.mkdtemp()
            self.addCleanup(shutil.rmtree, origin, True)
            subprocess.run(["git", "init", "-q", "--bare", origin], check=True)
            self.run_git("remote", "add", "origin", origin)
        self.run_git("push", "-q", "origin", "main")
        self.run_git("checkout", "-q", "promotion-test")
        self.run_git("reset", "-q", "--hard", "main")

    @staticmethod
    def with_digest(text, digest):
        return stage_release.DIGEST_LINE.sub(lambda m: m.group(1) + digest + m.group(3), text)

    def prod_digest(self):
        return stage_release.read_digest((self.root / PROD_MANIFEST).read_text(encoding="utf-8"))

    def run_cli(self, argv, soaked=True, now=None):
        errors = io.StringIO()
        later = now if now is not None else time.time() + 7200 if soaked else time.time()
        with unittest.mock.patch("stage_release.time.time", return_value=later), \
                contextlib.redirect_stderr(errors):
            result = stage_release.main(argv, repo_root=self.root)
        return result, errors.getvalue()

    def promote_prod(self, digest=PROMOTED_DIGEST, soaked=True, extra=()):
        return self.run_cli(["promote", "--to", "prod", "--digest", digest, *extra], soaked)

    def test_prod_promotes_the_digest_stage_pins_on_origin_main_after_soak(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)

        result, _ = self.promote_prod()

        self.assertEqual(result, 0)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)
        self.assertEqual(len(self.cosign_calls()), 2)

    def test_prod_refuses_a_digest_stage_does_not_pin(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)

        result, errors = self.promote_prod("sha256:" + "c" * 64)

        self.assertEqual(result, 2)
        self.assertIn("prod promotes only the digest stage pins", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_refuses_when_stage_manifest_is_missing(self):
        self.publish_main(prod=stage_release.PLACEHOLDER_DIGEST)
        self.run_git("rm", "-q", str(STAGE_MANIFEST))
        self.run_git("commit", "-q", "-m", "Drop stage")

        result, errors = self.promote_prod()

        self.assertEqual(result, 2)
        self.assertIn("stage Application manifest not found", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_refuses_when_stage_still_pins_placeholder(self):
        self.publish_main(stage=stage_release.PLACEHOLDER_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)

        result, errors = self.promote_prod()

        self.assertEqual(result, 2)
        self.assertIn("placeholder", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_refuses_a_stage_digest_that_is_not_on_origin_main(self):
        self.publish_main(prod=stage_release.PLACEHOLDER_DIGEST)
        self.manifest.write_text(self.with_digest(self.manifest.read_text(encoding="utf-8"), PROMOTED_DIGEST), encoding="utf-8")
        self.commit("Stage only on branch")

        result, errors = self.promote_prod()

        self.assertEqual(result, 2)
        self.assertIn("prod promotes only the digest stage pins on origin/main", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_promotion_reads_stage_from_origin_main_not_the_working_tree(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)
        self.manifest.write_text(self.with_digest(self.manifest.read_text(encoding="utf-8"), INITIAL_DIGEST), encoding="utf-8")

        result, _ = self.promote_prod()

        self.assertEqual(result, 0)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

    def test_prod_soak_counts_from_the_repin_not_the_earlier_pin(self):
        now = time.time()
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST, at=now - 7200)
        self.publish_main(stage=INITIAL_DIGEST, at=now - 3000)
        self.publish_main(stage=PROMOTED_DIGEST, at=now - 600)

        result, errors = self.run_cli(["promote", "--to", "prod", "--digest", PROMOTED_DIGEST], now=now)

        self.assertEqual(result, 2)
        self.assertIn("prod requires at least 60", errors)

    def test_prod_soak_counts_from_the_earliest_commit_of_an_unbroken_pin(self):
        now = time.time()
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST, at=now - 7200, note="a")
        self.publish_main(stage=PROMOTED_DIGEST, at=now - 3000, note="b")
        self.publish_main(stage=PROMOTED_DIGEST, at=now - 100, note="c")

        result, _ = self.run_cli(["promote", "--to", "prod", "--digest", PROMOTED_DIGEST], now=now)

        self.assertEqual(result, 0)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

    def test_prod_promotion_refuses_when_fetch_fails(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)
        self.run_git("remote", "set-url", "origin", str(self.root / "missing-remote"))

        result, errors = self.promote_prod()

        self.assertEqual(result, 2)
        self.assertIn("fetch", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_rollback_refuses_when_fetch_fails(self):
        self.publish_main(prod=INITIAL_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)
        self.run_git("remote", "set-url", "origin", str(self.root / "missing-remote"))

        result, errors = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 2)
        self.assertIn("fetch", errors)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

    def test_prod_rollback_refuses_target_without_sbom_attestation(self):
        self.publish_main(prod=INITIAL_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)
        self.install_cosign_stub(fail_on="verify-attestation")

        result, errors = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 2)
        self.assertIn("verify-attestation", errors)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

    def test_prod_rollback_refuses_a_stale_branch(self):
        self.publish_main(prod=INITIAL_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)
        self.run_git("reset", "-q", "--hard", "HEAD~1")

        result, errors = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 2)
        self.assertIn("origin/main pins", errors)
        self.assertEqual(self.prod_digest(), INITIAL_DIGEST)

    def test_prod_refuses_before_stage_soak_floor(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)

        result, errors = self.promote_prod(soaked=False)

        self.assertEqual(result, 2)
        self.assertIn("prod requires at least 60", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)

    def test_prod_refuses_a_digest_it_already_pins(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=PROMOTED_DIGEST)

        result, errors = self.promote_prod()

        self.assertEqual(result, 2)
        self.assertIn("prod already pins", errors)

    def test_prod_refuses_skip_verify(self):
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)

        result, errors = self.promote_prod(extra=["--skip-verify"])

        self.assertEqual(result, 2)
        self.assertIn("--skip-verify is not allowed for prod", errors)
        self.assertEqual(self.prod_digest(), stage_release.PLACEHOLDER_DIGEST)
        self.assertEqual(self.cosign_calls(), [])

    def test_prod_rollback_restores_prior_origin_main_digest_after_verifying_it(self):
        self.publish_main(prod=INITIAL_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)

        result, _ = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 0)
        self.assertEqual(self.prod_digest(), INITIAL_DIGEST)
        self.assertIn(f"{stage_release.IMAGE_REPOSITORY}@{INITIAL_DIGEST}", self.cosign_calls()[0])

    def test_prod_rollback_refuses_unsigned_target(self):
        self.publish_main(prod=INITIAL_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)
        self.install_cosign_stub(fail_on="verify")

        result, _ = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 2)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

    def test_prod_rollback_refuses_without_an_earlier_digest(self):
        self.publish_main(prod=stage_release.PLACEHOLDER_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)

        result, errors = self.run_cli(["rollback", "--env", "prod"])

        self.assertEqual(result, 2)
        self.assertIn("no earlier non-placeholder prod digest", errors)
        self.assertEqual(self.prod_digest(), PROMOTED_DIGEST)

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

    def start_release_api(self, status=201, body=None):
        received = []
        reply = json.dumps(body if body is not None else {"id": "rel-1"}).encode()

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(handler):
                length = int(handler.headers["Content-Length"])
                received.append(
                    {
                        "path": handler.path,
                        "headers": dict(handler.headers),
                        "body": json.loads(handler.rfile.read(length)),
                    }
                )
                handler.send_response(status)
                handler.send_header("Content-Type", "application/json")
                handler.end_headers()
                handler.wfile.write(reply)

            def log_message(handler, *args):
                pass

        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        os.environ[stage_release.API_URL_VARIABLE] = f"http://127.0.0.1:{server.server_port}"
        return received

    def record(self, env="stage", since=None):
        argv = ["record", "--env", env] + (["--since", since] if since else [])
        return self.run_cli(argv)

    def test_promote_and_rollback_edits_record_nothing(self):
        received = self.start_release_api()

        self.run_cli(["promote", "--to", "stage", "--digest", PROMOTED_DIGEST])
        self.commit("Promote hello-ledger to stage")
        self.run_cli(["rollback", "--env", "stage"])

        self.assertEqual(received, [])

    def test_record_posts_the_merged_promotion(self):
        received = self.start_release_api()
        os.environ[stage_release.API_TOKEN_VARIABLE] = "secret-token"
        self.publish_main(stage=PROMOTED_DIGEST)

        result, _ = self.record()

        self.assertEqual(result, 0)
        self.assertEqual(len(received), 1)
        self.assertEqual(received[0]["path"], "/v1/platform/releases")
        self.assertEqual(received[0]["headers"]["Authorization"], "Bearer secret-token")
        git_sha = self.run_git("rev-parse", "HEAD")
        self.assertEqual(
            received[0]["headers"]["Idempotency-Key"],
            stage_release.idempotency_key("stage", "promote", PROMOTED_DIGEST, git_sha),
        )
        self.assertEqual(
            received[0]["body"],
            {
                "service": "hello-ledger",
                "environment": "stage",
                "action": "promote",
                "imageDigest": PROMOTED_DIGEST,
                "previousDigest": INITIAL_DIGEST,
                "gitSha": git_sha,
                "signed": True,
            },
        )
        self.assertEqual(len(self.cosign_calls()), 2)

    def test_record_classifies_a_return_to_an_earlier_digest_as_rollback(self):
        received = self.start_release_api()
        self.publish_main(stage=PROMOTED_DIGEST)
        self.publish_main(stage=INITIAL_DIGEST)

        self.record()

        body = received[0]["body"]
        self.assertEqual((body["action"], body["imageDigest"], body["previousDigest"]), ("rollback", INITIAL_DIGEST, PROMOTED_DIGEST))

    def test_record_covers_every_commit_since_the_given_revision(self):
        received = self.start_release_api()
        before = self.run_git("rev-parse", "HEAD")
        self.publish_main(stage=PROMOTED_DIGEST)
        self.publish_main(stage=PROMOTED_DIGEST, note="doc only")

        self.record(since=before)

        self.assertEqual(received[0]["body"]["previousDigest"], INITIAL_DIGEST)
        self.assertEqual(received[0]["body"]["action"], "promote")

    def test_record_prod_promotion(self):
        received = self.start_release_api()
        self.publish_main(stage=PROMOTED_DIGEST, prod=stage_release.PLACEHOLDER_DIGEST)
        self.publish_main(prod=PROMOTED_DIGEST)

        result, _ = self.record("prod")

        self.assertEqual(result, 0)
        self.assertEqual(received[0]["body"]["environment"], "prod")
        self.assertEqual(received[0]["body"]["previousDigest"], stage_release.PLACEHOLDER_DIGEST)

    def test_record_skips_when_the_digest_did_not_change(self):
        received = self.start_release_api()
        self.publish_main(stage=INITIAL_DIGEST, note="doc only")

        result, _ = self.record()

        self.assertEqual(result, 0)
        self.assertEqual(received, [])

    def test_record_marks_an_unverifiable_image_unsigned(self):
        received = self.start_release_api()
        self.publish_main(stage=PROMOTED_DIGEST)
        self.install_cosign_stub(fail_on="verify")

        result, errors = self.record()

        self.assertEqual(result, 0)
        self.assertIs(received[0]["body"]["signed"], False)
        self.assertIn("unsigned", errors)

    def test_record_fails_with_exit_3_when_the_api_rejects_or_is_unreachable(self):
        self.publish_main(stage=PROMOTED_DIGEST)
        self.start_release_api(status=400, body={"code": "COM-0001"})
        result, errors = self.record()
        self.assertEqual(result, 3)
        self.assertIn("answered 400", errors)

        os.environ[stage_release.API_URL_VARIABLE] = "http://127.0.0.1:1"
        result, errors = self.record()
        self.assertEqual(result, 3)
        self.assertIn("release not recorded", errors)

    def test_record_without_an_api_url_only_warns(self):
        self.publish_main(stage=PROMOTED_DIGEST)

        result, errors = self.record()

        self.assertEqual(result, 0)
        self.assertIn(f"{stage_release.API_URL_VARIABLE} is not set", errors)

    def test_record_requires_https_except_for_localhost(self):
        self.publish_main(stage=PROMOTED_DIGEST)
        os.environ[stage_release.API_URL_VARIABLE] = "http://platform.example.com"
        result, errors = self.record()
        self.assertEqual(result, 2)
        self.assertIn("must be https", errors)

        for allowed in ("https://platform.example.com/", "http://localhost:8080", "http://127.0.0.1:8080"):
            os.environ[stage_release.API_URL_VARIABLE] = allowed
            self.assertEqual(stage_release._api_base(), allowed.rstrip("/"))
        os.environ[stage_release.API_URL_VARIABLE] = "file:///etc/passwd"
        with self.assertRaises(stage_release.ReleaseError):
            stage_release._api_base()

    def test_idempotency_key_is_deterministic_and_changes_with_every_input(self):
        sha = "1" * 40
        key = stage_release.idempotency_key("stage", "promote", PROMOTED_DIGEST, sha)
        self.assertEqual(key, stage_release.idempotency_key("stage", "promote", PROMOTED_DIGEST, sha))
        for other in (
            ("prod", "promote", PROMOTED_DIGEST, sha),
            ("stage", "rollback", PROMOTED_DIGEST, sha),
            ("stage", "promote", INITIAL_DIGEST, sha),
            ("stage", "promote", PROMOTED_DIGEST, "2" * 40),
        ):
            self.assertNotEqual(key, stage_release.idempotency_key(*other))


if __name__ == "__main__":
    unittest.main()
