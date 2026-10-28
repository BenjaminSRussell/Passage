#!/usr/bin/env python3
"""Prepare immutable hello-ledger digest changes for the Argo CD GitOps flow."""

import argparse
import re
import subprocess
import sys
import time
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
MANIFESTS = {
    env: Path(f"deploy/envs/{env}/apps/hello-ledger.yaml") for env in ("stage", "prod")
}
SOAK_MINUTES = 60
PLACEHOLDER_DIGEST ="sha256:" + "0" * 64
IMAGE_REPOSITORY = "ghcr.io/benjaminsrussell/hello-ledger"
WORKFLOW_REPOSITORY = "BenjaminSRussell/Passage"
OIDC_ISSUER = "https://token.actions.githubusercontent.com"
IDENTITY_REGEXP = r"^https://github\.com/BenjaminSRussell/Passage/\.github/workflows/ci\.yml@refs/(heads/main|tags/v.+)$"
DIGEST_LINE = re.compile(r"(?m)^(\s*digest:\s*)(sha256:[0-9a-f]{64})(\s*)$")


class ReleaseError(Exception):
    """A safe promotion or rollback could not be prepared."""


def _git(root, *args):
    try:
        return subprocess.run(
            ["git", "-C", str(root), *args],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as error:
        detail = getattr(error, "stderr", "") or str(error)
        raise ReleaseError(f"git {' '.join(args)} failed: {detail.strip()}") from error


def read_digest(manifest):
    """Return the one SHA-256 image digest pinned by an environment app manifest."""
    matches = list(DIGEST_LINE.finditer(manifest))
    if len(matches) != 1:
        raise ReleaseError(
            f"app manifest must contain exactly one lowercase sha256 digest; found {len(matches)}"
        )
    return matches[0].group(2)


def _manifest_path(root, env):
    path = root / MANIFESTS[env]
    if not path.is_file():
        raise ReleaseError(f"{env} Application manifest not found: {path}")
    return path


def _read_manifest(path):
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as error:
        raise ReleaseError(f"cannot read manifest {path}: {error}") from error
    read_digest(text)
    return text


def _require_clean_manifest(root, env):
    changed = _git(root, "status", "--porcelain", "--", MANIFESTS[env].as_posix())
    if changed:
        raise ReleaseError(
            f"{MANIFESTS[env]} has uncommitted changes; commit or discard them before promotion or rollback"
        )


def _require_working_branch(root):
    branch = _git(root, "branch", "--show-current")
    if not branch or branch == "main":
        raise ReleaseError(
            "run promotion or rollback on a named branch; direct changes on main are refused"
        )


def _write_digest(path, text, digest):
    updated, replacements = DIGEST_LINE.subn(
        lambda match: match.group(1) + digest + match.group(3), text, count=1
    )
    if replacements != 1:
        raise ReleaseError("could not update the image digest safely")
    try:
        path.write_text(updated, encoding="utf-8")
    except OSError as error:
        raise ReleaseError(f"cannot update manifest {path}: {error}") from error


def cosign_identity_args():
    return [
        "--certificate-identity-regexp",
        IDENTITY_REGEXP,
        "--certificate-oidc-issuer",
        OIDC_ISSUER,
        "--certificate-github-workflow-repository",
        WORKFLOW_REPOSITORY,
    ]


def verify_image(digest):
    reference = f"{IMAGE_REPOSITORY}@{digest}"
    identity = cosign_identity_args()
    for command in (
        ["cosign", "verify", *identity, reference],
        ["cosign", "verify-attestation", "--type", "spdxjson", *identity, reference],
    ):
        try:
            subprocess.run(command, check=True, capture_output=True, text=True)
        except OSError as error:
            raise ReleaseError(f"cannot run cosign to verify {reference}: {error}") from error
        except subprocess.CalledProcessError as error:
            raise ReleaseError(
                f"`{' '.join(command[:2])}` rejected {reference}: {error.stderr.strip()}"
            ) from error


def _fetch_origin_main(root):
    _git(root, "fetch", "origin", "main")


def _require_soaked_stage_digest(root, digest):
    _manifest_path(root, "stage")
    _require_clean_manifest(root, "stage")
    _fetch_origin_main(root)
    stage_path = MANIFESTS["stage"].as_posix()
    staged = read_digest(_git(root, "show", f"origin/main:{stage_path}"))
    if staged == PLACEHOLDER_DIGEST:
        raise ReleaseError("stage on origin/main still pins the placeholder digest; promote it to stage first")
    if digest != staged:
        raise ReleaseError(
            f"prod promotes only the digest stage pins on origin/main ({staged}); got {digest}"
        )
    pinned_since = None
    history = _git(
        root, "log", "--first-parent", "--format=%H %ct", "origin/main", "--", stage_path
    ).splitlines()
    for line in history:
        revision, committed = line.split()
        try:
            candidate = read_digest(_git(root, "show", f"{revision}:{stage_path}"))
        except ReleaseError:
            break
        if candidate != staged:
            break
        pinned_since = int(committed)
    if pinned_since is None:
        raise ReleaseError("cannot determine when stage started pinning this digest")
    soaked_minutes = (time.time() - pinned_since) / 60
    if soaked_minutes < SOAK_MINUTES:
        raise ReleaseError(
            f"stage has pinned {digest} for {soaked_minutes:.0f} minutes; prod requires at least {SOAK_MINUTES}"
        )


def promote(root, digest, env="stage", skip_verify=False):
    if env == "prod" and skip_verify:
        raise ReleaseError("--skip-verify is not allowed for prod")
    if re.fullmatch(r"sha256:[0-9a-f]{64}", digest) is None:
        raise ReleaseError("digest must be an immutable lowercase sha256:<64 hex characters> reference")
    if digest == PLACEHOLDER_DIGEST:
        raise ReleaseError("the all-zero placeholder digest cannot be promoted")
    root = Path(root).resolve()
    path = _manifest_path(root, env)
    _require_working_branch(root)
    _require_clean_manifest(root, env)
    text = _read_manifest(path)
    current = read_digest(text)
    if env == "prod":
        _require_soaked_stage_digest(root, digest)
    if digest == current:
        raise ReleaseError(f"{env} already pins {digest}")
    if skip_verify:
        print(
            f"WARNING: SKIP_VERIFY set; {digest} promoted WITHOUT signature or SBOM verification",
            file=sys.stderr,
        )
    else:
        verify_image(digest)
    _write_digest(path, text, digest)
    return current, digest


def _previous_digest(root, env, current, ref="HEAD"):
    revisions = _git(
        root, "log", "--first-parent", "--format=%H", ref, "--", MANIFESTS[env].as_posix()
    ).splitlines()
    for revision in revisions:
        try:
            historical_manifest = _git(
                root, "show", f"{revision}:{MANIFESTS[env].as_posix()}"
            )
        except ReleaseError:
            continue
        try:
            candidate = read_digest(historical_manifest)
        except ReleaseError:
            continue
        if candidate != current and candidate != PLACEHOLDER_DIGEST:
            return candidate
    return None


def rollback(root, env="stage"):
    root = Path(root).resolve()
    path = _manifest_path(root, env)
    _require_working_branch(root)
    _require_clean_manifest(root, env)
    text = _read_manifest(path)
    current = read_digest(text)
    if current == PLACEHOLDER_DIGEST:
        raise ReleaseError(
            f"{env} still pins the placeholder digest; refusing rollback before the first promotion"
        )
    ref = "HEAD"
    if env == "prod":
        _fetch_origin_main(root)
        ref = "origin/main"
    previous = _previous_digest(root, env, current, ref)
    if previous is None:
        raise ReleaseError(
            f"no earlier non-placeholder {env} digest exists in Git history; refusing rollback"
        )
    verify_image(previous)
    _write_digest(path, text, previous)
    return current, previous


def main(argv=None, repo_root=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    promote_parser = commands.add_parser("promote", help="pin a digest in an environment Application")
    promote_parser.add_argument("--to", choices=sorted(MANIFESTS), required=True)
    promote_parser.add_argument("--digest", required=True)
    promote_parser.add_argument("--skip-verify", action="store_true")
    rollback_parser = commands.add_parser("rollback", help="restore the prior environment digest from Git history")
    rollback_parser.add_argument("--env", choices=sorted(MANIFESTS), required=True)
    args = parser.parse_args(argv)
    root = Path(repo_root or REPOSITORY_ROOT).resolve()

    try:
        if args.command == "promote":
            target = args.to
            previous, current = promote(root, args.digest, target, args.skip_verify)
            print(f"{target} digest: {previous} -> {current}")
        else:
            target = args.env
            previous, current = rollback(root, target)
            print(f"{target} rollback digest: {previous} -> {current}")
    except ReleaseError as error:
        print(f"release refused: {error}", file=sys.stderr)
        return 2

    print(
        f"Review and merge this manifest change on main; then verify Argo with: "
        f"argocd app history hello-ledger-{target}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
