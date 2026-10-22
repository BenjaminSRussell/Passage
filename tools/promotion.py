#!/usr/bin/env python3
"""Promote or roll back the hello-ledger image digest.

Writes the Helm values digest and the Argo CD annotation first. The history
file is an append-only rollback log and is updated only after both of those
writes succeed. Argo CD syncs the values file and the annotation, not the log.
"""

import argparse
import os
import pathlib
import re
import subprocess
import sys

ENVS = ("dev", "stage", "prod")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


class PromotionError(Exception):
    pass


def chart_dir(root: pathlib.Path) -> pathlib.Path:
    return root / "deploy" / "helm" / "hello-ledger"


def values_path(root: pathlib.Path, env: str) -> pathlib.Path:
    return chart_dir(root) / f"values-{env}.yaml"


def history_path(root: pathlib.Path, env: str) -> pathlib.Path:
    return chart_dir(root) / "history" / f"{env}.txt"


def app_path(root: pathlib.Path, env: str) -> pathlib.Path:
    return root / "deploy" / "argocd" / "apps" / f"hello-ledger-{env}.yaml"


def check_digest(digest: str) -> str:
    if not DIGEST_RE.fullmatch(digest):
        raise PromotionError(f"digest must look like sha256:<64 hex chars>, got {digest!r}")
    return digest


def replace_key(text: str, key: str, digest: str) -> str:
    lines = []
    found = 0
    prefix = key + ":"
    for line in text.splitlines():
        stripped = line.lstrip()
        if stripped.startswith(prefix):
            indent = line[: len(line) - len(stripped)]
            lines.append(f"{indent}{key}: {digest}")
            found += 1
        else:
            lines.append(line)
    if found != 1:
        raise PromotionError(f"{key} must appear exactly once, found {found}")
    return "\n".join(lines) + "\n"


def read_key(text: str, key: str) -> str:
    prefix = key + ":"
    found = []
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith(prefix):
            found.append(stripped.split(":", 1)[1].strip())
    if len(found) != 1:
        raise PromotionError(f"{key} must appear exactly once, found {len(found)}")
    return found[0]


def history_events(text: str) -> list[tuple[str, str]]:
    events = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) != 2 or parts[0] not in ("promote", "rollback"):
            raise PromotionError(f"bad history line: {raw!r}")
        events.append((parts[0], check_digest(parts[1])))
    return events


def history_digests(text: str) -> list[str]:
    return [digest for _, digest in history_events(text)]


def stack_from(events: list[tuple[str, str]]) -> list[str]:
    stack: list[str] = []
    for action, digest in events:
        if action == "promote":
            stack.append(digest)
            continue
        if len(stack) < 2 or stack[-2] != digest:
            raise PromotionError("history rollback does not match the prior digest")
        stack.pop()
    return stack


def append_history(path: pathlib.Path, action: str, digest: str) -> None:
    existing = path.read_text() if path.exists() else ""
    if existing and not existing.endswith("\n"):
        existing += "\n"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(existing + f"{action} {digest}\n")


def write_digest(root: pathlib.Path, env: str, digest: str, on_write=None) -> None:
    targets = (
        (values_path(root, env), "imageDigest"),
        (app_path(root, env), "passage.dev/image-digest"),
    )
    updates = []
    for path, key in targets:
        original = path.read_text()
        updates.append((path, original, replace_key(original, key, digest)))
    try:
        for index, (path, _original, text) in enumerate(updates):
            if on_write is not None:
                on_write(index, path, text)
            else:
                path.write_text(text)
    except Exception:
        for path, original, _text in updates:
            path.write_text(original)
        raise


def local_registry(repository: str) -> bool:
    host = repository.split("/", 1)[0]
    return host == "hello-ledger" or host.startswith("127.0.0.1") or host.startswith("localhost")


def verify_signature(root: pathlib.Path, env: str, digest: str) -> None:
    key = os.environ.get("COSIGN_PUBLIC_KEY", "")
    if not key or not os.path.isfile(key):
        raise PromotionError("COSIGN_PUBLIC_KEY is required to promote")
    repository = read_key(values_path(root, env).read_text(), "imageRepository")
    ref = f"{repository}@{digest}"
    flags = ["--key", key]
    if local_registry(repository):
        flags.extend(["--insecure-ignore-tlog", "--allow-insecure-registry"])
    commands = (
        ["cosign", "verify", *flags, ref],
        ["cosign", "verify-attestation", *flags, "--type", "spdxjson", ref],
    )
    for command in commands:
        result = subprocess.run(command, capture_output=True, text=True)
        if result.returncode != 0:
            detail = (result.stderr or result.stdout).strip()
            raise PromotionError(f"signature verification failed for {ref}: {detail}")


def promote(root: pathlib.Path, env: str, digest: str, verify=None, on_write=None) -> str:
    if env not in ENVS:
        raise PromotionError(f"unknown env {env}")
    digest = check_digest(digest)
    current = read_key(values_path(root, env).read_text(), "imageDigest")
    if current == digest:
        raise PromotionError(f"{env} is already at {digest}")
    if verify is None:
        verify = verify_signature
    verify(root, env, digest)
    write_digest(root, env, digest, on_write=on_write)
    append_history(history_path(root, env), "promote", digest)
    return digest


def rollback(root: pathlib.Path, env: str, on_write=None) -> str:
    if env not in ENVS:
        raise PromotionError(f"unknown env {env}")
    stack = stack_from(history_events(history_path(root, env).read_text()))
    if len(stack) < 2:
        raise PromotionError(f"{env} has no prior digest")
    prior = stack[-2]
    write_digest(root, env, prior, on_write=on_write)
    append_history(history_path(root, env), "rollback", prior)
    return prior


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Promote or roll back hello-ledger")
    parser.add_argument("action", choices=("promote", "rollback"))
    parser.add_argument("env", choices=ENVS)
    parser.add_argument("digest", nargs="?")
    parser.add_argument("--root", default=".")
    args = parser.parse_args(argv)
    root = pathlib.Path(args.root)
    try:
        if args.action == "promote":
            if not args.digest:
                raise PromotionError("promote requires a digest")
            digest = promote(root, args.env, args.digest)
            print(f"promoted {args.env} to {digest}")
        else:
            digest = rollback(root, args.env)
            print(f"rolled back {args.env} to {digest}")
    except PromotionError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
