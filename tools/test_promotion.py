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
        self.assertEqual(promotion.promote(self.root, "stage", NEXT), NEXT)
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
        promotion.promote(self.root, "dev", NEXT)
        promotion.promote(self.root, "dev", THIRD)
        self.assertEqual(promotion.rollback(self.root, "dev"), NEXT)

    def test_invalid_digest_and_missing_prior(self):
        with self.assertRaises(promotion.PromotionError):
            promotion.promote(self.root, "prod", "not-a-digest")
        with self.assertRaises(promotion.PromotionError):
            promotion.rollback(self.root, "prod")

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
