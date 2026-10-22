#!/usr/bin/env python3
"""Tests for hello-ledger image promotion and rollback."""

import pathlib
import shutil
import tempfile
import unittest

import promotion

ROOT = pathlib.Path(__file__).resolve().parent.parent
SEED = "sha256:0a6d5f56e627284b7fcc07112954c0a9fc37d4b04da82a1a4f73ece26af04db5"
NEXT = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
THIRD = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"


def accept(_root, _env, _digest):
    return None


def seed_tree(root: pathlib.Path) -> None:
    for env in promotion.ENVS:
        src_values = ROOT / "deploy" / "helm" / "hello-ledger" / f"values-{env}.yaml"
        src_app = ROOT / "deploy" / "argocd" / "apps" / f"hello-ledger-{env}.yaml"
        src_hist = ROOT / "deploy" / "helm" / "hello-ledger" / "history" / f"{env}.txt"
        for src in (src_values, src_app, src_hist):
            dest = root / src.relative_to(ROOT)
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy(src, dest)


class PromotionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tmp.name)
        seed_tree(self.root)

    def tearDown(self):
        self.tmp.cleanup()

    def test_promote_then_rollback(self):
        self.assertEqual(promotion.promote(self.root, "stage", NEXT, verify=accept), NEXT)
        values = (self.root / "deploy/helm/hello-ledger/values-stage.yaml").read_text()
        app = (self.root / "deploy/argocd/apps/hello-ledger-stage.yaml").read_text()
        history = (self.root / "deploy/helm/hello-ledger/history/stage.txt").read_text()
        self.assertIn(f"imageDigest: {NEXT}", values)
        self.assertIn(f"passage.dev/image-digest: {NEXT}", app)
        self.assertTrue(history.strip().endswith(f"promote {NEXT}"))

        self.assertEqual(promotion.rollback(self.root, "stage"), SEED)
        values = (self.root / "deploy/helm/hello-ledger/values-stage.yaml").read_text()
        history = (self.root / "deploy/helm/hello-ledger/history/stage.txt").read_text().splitlines()
        self.assertIn(f"imageDigest: {SEED}", values)
        self.assertEqual(history[-1], f"rollback {SEED}")
        self.assertIn(f"promote {NEXT}", history)

    def test_second_promotion_rolls_back_to_immediate_prior(self):
        promotion.promote(self.root, "dev", NEXT, verify=accept)
        promotion.promote(self.root, "dev", THIRD, verify=accept)
        self.assertEqual(promotion.rollback(self.root, "dev"), NEXT)
        self.assertEqual(promotion.rollback(self.root, "dev"), SEED)
        with self.assertRaises(promotion.PromotionError):
            promotion.rollback(self.root, "dev")

    def test_invalid_digest_and_missing_prior(self):
        with self.assertRaises(promotion.PromotionError):
            promotion.promote(self.root, "prod", "not-a-digest")
        with self.assertRaises(promotion.PromotionError):
            promotion.rollback(self.root, "prod")

    def test_failed_second_write_does_not_advance_history(self):
        history = self.root / "deploy/helm/hello-ledger/history/stage.txt"
        before_history = history.read_text()
        before_values = (self.root / "deploy/helm/hello-ledger/values-stage.yaml").read_text()
        before_app = (self.root / "deploy/argocd/apps/hello-ledger-stage.yaml").read_text()

        def on_write(index, path, text):
            if index == 1:
                raise promotion.PromotionError("second write failed")
            path.write_text(text)

        with self.assertRaises(promotion.PromotionError):
            promotion.promote(self.root, "stage", NEXT, verify=accept, on_write=on_write)
        self.assertEqual(history.read_text(), before_history)
        self.assertEqual((self.root / "deploy/helm/hello-ledger/values-stage.yaml").read_text(), before_values)
        self.assertEqual((self.root / "deploy/argocd/apps/hello-ledger-stage.yaml").read_text(), before_app)

    def test_rejected_signature_writes_nothing(self):
        history = self.root / "deploy/helm/hello-ledger/history/prod.txt"
        before = history.read_text()

        def reject(_root, _env, _digest):
            raise promotion.PromotionError("unsigned")

        with self.assertRaises(promotion.PromotionError):
            promotion.promote(self.root, "prod", NEXT, verify=reject)
        self.assertEqual(history.read_text(), before)

    def test_repo_history_matches_values(self):
        deploy = (ROOT / "scripts" / "deploy-local.sh").read_text()
        module = (ROOT / "infra" / "tofu" / "modules" / "cluster" / "main.tf").read_text()
        self.assertIn("k3d cluster create passage", deploy)
        self.assertIn("k3d cluster create ${var.cluster_name}", module)
        for env in promotion.ENVS:
            digest = promotion.read_key(
                (ROOT / "deploy" / "helm" / "hello-ledger" / f"values-{env}.yaml").read_text(),
                "imageDigest",
            )
            annotation = promotion.read_key(
                (ROOT / "deploy" / "argocd" / "apps" / f"hello-ledger-{env}.yaml").read_text(),
                "passage.dev/image-digest",
            )
            history = promotion.history_digests(
                (ROOT / "deploy" / "helm" / "hello-ledger" / "history" / f"{env}.txt").read_text()
            )
            self.assertEqual(digest, annotation)
            self.assertEqual(history[-1], digest)


if __name__ == "__main__":
    unittest.main()
