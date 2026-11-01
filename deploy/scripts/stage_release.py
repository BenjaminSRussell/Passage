#!/usr/bin/env python3
"""Prepare immutable hello-ledger digest changes for the Argo CD GitOps flow."""

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


REPOSITORY_ROOT = Path(__file__).resolve().parents[2]
MANIFESTS = {
    env: Path(f"deploy/envs/{env}/apps/hello-ledger.yaml") for env in ("stage", "prod")
}
SOAK_MINUTES = 60
SERVICE = "hello-ledger"
API_URL_VARIABLE = "PASSAGE_PLATFORM_API_URL"
API_TOKEN_VARIABLE = "PASSAGE_PLATFORM_API_TOKEN"
RECORD_TIMEOUT_SECONDS = 10
NEXT_CURSOR_HEADER = "X-Next-Cursor"
LOCAL_HOSTS = ("localhost", "127.0.0.1")
PLACEHOLDER_DIGEST = "sha256:" + "0" * 64
IMAGE_REPOSITORY = "ghcr.io/benjaminsrussell/hello-ledger"
WORKFLOW_REPOSITORY = "BenjaminSRussell/Passage"
OIDC_ISSUER = "https://token.actions.githubusercontent.com"
IDENTITY_REGEXP = r"^https://github\.com/BenjaminSRussell/Passage/\.github/workflows/ci\.yml@refs/(heads/main|tags/v.+)$"
GIT_SHA = re.compile(r"[0-9a-f]{40}")
DIGEST_LINE = re.compile(r"""(?m)^(\s*digest:\s*)(["']?)(sha256:[0-9a-f]{64})\2(\s*)$""")


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
    return matches[0].group(3)


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
        lambda match: match.group(1) + match.group(2) + digest + match.group(2) + match.group(4), text, count=1
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


def _cosign_env():
    return {name: value for name, value in os.environ.items() if name != API_TOKEN_VARIABLE}


def verify_image(digest):
    reference = f"{IMAGE_REPOSITORY}@{digest}"
    identity = cosign_identity_args()
    for command in (
        ["cosign", "verify", *identity, reference],
        ["cosign", "verify-attestation", "--type", "spdxjson", *identity, reference],
    ):
        try:
            subprocess.run(command, check=True, capture_output=True, text=True, env=_cosign_env())
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
    ref = "HEAD"
    if env == "prod":
        _fetch_origin_main(root)
        ref = "origin/main"
        live = read_digest(_git(root, "show", f"origin/main:{MANIFESTS[env].as_posix()}"))
        if live != current:
            raise ReleaseError(
                f"local prod manifest pins {current} but origin/main pins {live}; rebase onto origin/main first"
            )
    if current == PLACEHOLDER_DIGEST:
        raise ReleaseError(
            f"{env} still pins the placeholder digest; refusing rollback before the first promotion"
        )
    previous = _previous_digest(root, env, current, ref)
    if previous is None:
        raise ReleaseError(
            f"no earlier non-placeholder {env} digest exists in Git history; refusing rollback"
        )
    verify_image(previous)
    _write_digest(path, text, previous)
    return current, previous


def idempotency_key(env, action, digest, git_sha):
    material = "|".join((env, SERVICE, digest, git_sha, action))
    return hashlib.sha256(material.encode()).hexdigest()


def _api_base():
    base = os.environ.get(API_URL_VARIABLE, "").rstrip("/")
    if not base:
        return ""
    parsed = urllib.parse.urlparse(base)
    local = parsed.scheme == "http" and parsed.hostname in LOCAL_HOSTS
    if parsed.scheme != "https" and not local:
        raise ReleaseError(f"{API_URL_VARIABLE} must be https (http is allowed only for localhost or 127.0.0.1)")
    return base


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


_OPENER = urllib.request.build_opener(_NoRedirect)


def _api_headers(extra=None):
    headers = dict(extra or {})
    token = os.environ.get(API_TOKEN_VARIABLE)
    if token:
        headers["Authorization"] = f"Bearer {token}"
    return headers


def _call_api(request, headers_out=None):
    try:
        with _OPENER.open(request, timeout=RECORD_TIMEOUT_SECONDS) as response:
            if headers_out is not None:
                headers_out.update(response.headers)
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise ReleaseError(f"release API answered {error.code}: {error.read().decode(errors='replace')[:300]}") from error
    except (OSError, ValueError) as error:
        raise ReleaseError(f"release API call failed: {error}") from error


def _list_releases(base, env):
    releases = []
    cursor = None
    seen = set()
    while True:
        params = {"service": SERVICE, "env": env}
        if cursor:
            params["cursor"] = cursor
        headers = {}
        page = _call_api(
            urllib.request.Request(
                f"{base}/v1/platform/releases?{urllib.parse.urlencode(params)}", headers=_api_headers(), method="GET"
            ),
            headers,
        )
        if not isinstance(page, list):
            raise ReleaseError("release API list answered a non-array body")
        releases.extend(page)
        cursor = headers.get(NEXT_CURSOR_HEADER)
        if not cursor:
            return releases
        if cursor in seen:
            raise ReleaseError("release API list repeated a pagination cursor")
        seen.add(cursor)


def _is_ancestor(root, ancestor, descendant):
    result = subprocess.run(
        ["git", "-C", str(root), "merge-base", "--is-ancestor", ancestor, descendant],
        capture_output=True,
        text=True,
    )
    if result.returncode not in (0, 1):
        raise ReleaseError(f"git merge-base --is-ancestor failed: {result.stderr.strip()}")
    return result.returncode == 0


def registry_cursor(root, releases):
    """Return (cursor, registry_ahead): the newest registry gitSha that is a valid commit on HEAD's history, skipping rows that are not."""
    if not releases:
        return None, False
    reason = ""
    for release in releases:
        git_sha = release.get("gitSha") if isinstance(release, dict) else None
        if not isinstance(git_sha, str) or GIT_SHA.fullmatch(git_sha) is None:
            reason = "release API release has no valid 40-hex gitSha"
            continue
        try:
            if _is_ancestor(root, git_sha, "HEAD"):
                return git_sha, False
            if _is_ancestor(root, "HEAD", git_sha):
                return None, True
        except ReleaseError as error:
            reason = f"registry gitSha {git_sha} cannot be compared with HEAD: {error}"
            continue
        reason = f"registry gitSha {git_sha} is unrelated to HEAD"
    raise ReleaseError(reason)


def record_release(base, env, action, digest, previous, git_sha, signed):
    """POST the merged promote or rollback to the platform-health release registry."""
    headers = _api_headers(
        {"Content-Type": "application/json", "Idempotency-Key": idempotency_key(env, action, digest, git_sha)}
    )
    body = json.dumps(
        {
            "service": SERVICE,
            "environment": env,
            "action": action,
            "imageDigest": digest,
            "previousDigest": previous,
            "gitSha": git_sha,
            "signed": signed,
        }
    ).encode()
    request = urllib.request.Request(
        f"{base}/v1/platform/releases", data=body, headers=headers, method="POST"
    )
    try:
        return _call_api(request)["id"]
    except (KeyError, TypeError) as error:
        raise ReleaseError(f"release API call failed: {error}") from error


def _digest_at(root, env, revision):
    try:
        manifest = _git(root, "show", f"{revision}:{MANIFESTS[env].as_posix()}")
    except ReleaseError:
        return PLACEHOLDER_DIGEST
    try:
        return read_digest(manifest)
    except ReleaseError:
        return None


def registry_known_digests(releases):
    """Digests the registry accepts as rollback targets: promoted images and every previous digest."""
    known = set()
    for release in releases:
        if not isinstance(release, dict):
            continue
        if release.get("action") == "promote":
            known.add(release.get("imageDigest"))
        known.add(release.get("previousDigest"))
    return known


def merged_releases(root, env, since, known=()):
    """Return ((action, digest, previous, git_sha) oldest first, unreadable commits) for commits after `since` that changed the manifest digest.

    A commit is a rollback only when `known` (registry digests plus those recorded earlier in this walk) holds its digest."""
    _read_manifest(_manifest_path(root, env))
    commits = _git(
        root, "rev-list", "--first-parent", "--reverse", "--end-of-options", f"{since}..HEAD", "--", MANIFESTS[env].as_posix()
    ).splitlines()
    known = set(known)
    releases = []
    unreadable = []
    last = _digest_at(root, env, since) or PLACEHOLDER_DIGEST
    for commit in commits:
        digest = _digest_at(root, env, commit)
        if digest is None:
            print(f"WARNING: {env} manifest at {commit} is unreadable; recording the rest and exiting 3", file=sys.stderr)
            unreadable.append(commit)
            continue
        if digest != last:
            if digest == PLACEHOLDER_DIGEST:
                print(f"WARNING: {env} pins the placeholder digest at {commit}; skipping that commit", file=sys.stderr)
            else:
                action = "rollback" if digest in known else "promote"
                releases.append((action, digest, last, commit))
                known.update({digest, last} if action == "promote" else {last})
        last = digest
    return releases, unreadable


def record(root, env, since):
    root = Path(root).resolve()
    base = _api_base()
    if base:
        try:
            registry = _list_releases(base, env)
            cursor, registry_ahead = registry_cursor(root, registry)
            if registry_ahead:
                print(f"{env} registry is ahead of this checkout; nothing to record")
                return 0
            if cursor:
                since = cursor
        except ReleaseError as error:
            print(f"release not recorded: {error}", file=sys.stderr)
            return 3
    else:
        registry = []
    since = since if since and set(since) != {"0"} else "HEAD~1"
    try:
        _git(root, "rev-parse", "--verify", "--quiet", "--end-of-options", f"{since}^{{commit}}")
    except ReleaseError:
        print(f"release not recorded: --since {since} is not a readable commit", file=sys.stderr)
        return 3
    if _digest_at(root, env, since) is None:
        print(f"release not recorded: {env} manifest at {since} is unreadable", file=sys.stderr)
        return 3
    releases, unreadable = merged_releases(root, env, since, registry_known_digests(registry))
    status = 3 if unreadable else 0
    if not releases:
        print(f"{env} digest unchanged since {since}; nothing to record")
        return status
    if not base:
        print(
            f"WARNING: {API_URL_VARIABLE} is not set; {len(releases)} {env} release(s) were NOT recorded in the release registry",
            file=sys.stderr,
        )
        return status
    for action, digest, previous, git_sha in releases:
        try:
            verify_image(digest)
            signed = True
        except ReleaseError as error:
            print(f"WARNING: recording {digest} as unsigned: {error}", file=sys.stderr)
            signed = False
        try:
            release_id = record_release(base, env, action, digest, previous, git_sha, signed)
        except ReleaseError as error:
            print(f"release not recorded: {error}", file=sys.stderr)
            return 3
        print(f"recorded {action} release {release_id} for {env} at {git_sha}")
    if unreadable:
        print(f"release not recorded: {len(unreadable)} {env} manifest commit(s) unreadable: {' '.join(unreadable)}", file=sys.stderr)
    return status


def main(argv=None, repo_root=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    promote_parser = commands.add_parser("promote", help="pin a digest in an environment Application")
    promote_parser.add_argument("--to", choices=sorted(MANIFESTS), required=True)
    promote_parser.add_argument("--digest", required=True)
    promote_parser.add_argument("--skip-verify", action="store_true")
    rollback_parser = commands.add_parser("rollback", help="restore the prior environment digest from Git history")
    rollback_parser.add_argument("--env", choices=sorted(MANIFESTS), required=True)
    record_parser = commands.add_parser(
        "record", help="record the merged manifest digest in the release registry (post-merge CI step)"
    )
    record_parser.add_argument("--env", choices=sorted(MANIFESTS), required=True)
    record_parser.add_argument("--since", help="fallback revision when the registry has no release for the environment; defaults to HEAD~1")
    args = parser.parse_args(argv)
    root = Path(repo_root or REPOSITORY_ROOT).resolve()

    try:
        if args.command == "record":
            return record(root, args.env, args.since)
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
        f"Review and merge this manifest change on main (CI records the release after the merge); "
        f"then verify Argo with: argocd app history hello-ledger-{target}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
