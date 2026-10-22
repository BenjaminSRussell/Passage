#!/usr/bin/env python3
"""Promote or roll back the hello-ledger image digest.

Updates the Helm values file, the Argo CD application annotation, and an
append-only history log. Argo CD records the same digest changes in its own
history after it syncs the commit.
"""

import argparse
import pathlib
import re
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
    found = False
    prefix = key + ":"
    for line in text.splitlines():
        stripped = line.lstrip()
        if stripped.startswith(prefix):
            indent = line[: len(line) - len(stripped)]
            lines.append(f"{indent}{key}: {digest}")
            found = True
        else:
            lines.append(line)
    if not found:
        raise PromotionError(f"missing {key}")
    return "\n".join(lines) + "\n"


def read_key(text: str, key: str) -> str:
    prefix = key + ":"
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith(prefix):
            return stripped.split(":", 1)[1].strip()
    raise PromotionError(f"missing {key}")


def history_digests(text: str) -> list[str]:
    digests = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) != 2 or parts[0] not in ("promote", "rollback"):
            raise PromotionError(f"bad history line: {raw!r}")
        digests.append(check_digest(parts[1]))
    return digests


def append_history(path: pathlib.Path, action: str, digest: str) -> None:
    existing = path.read_text() if path.exists() else ""
    if existing and not existing.endswith("\n"):
        existing += "\n"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(existing + f"{action} {digest}\n")


def write_digest(root: pathlib.Path, env: str, digest: str) -> None:
    for path, key in (
        (values_path(root, env), "imageDigest"),
        (app_path(root, env), "passage.dev/image-digest"),
    ):
        path.write_text(replace_key(path.read_text(), key, digest))


def promote(root: pathlib.Path, env: str, digest: str) -> str:
    if env not in ENVS:
        raise PromotionError(f"unknown env {env}")
    digest = check_digest(digest)
    current = read_key(values_path(root, env).read_text(), "imageDigest")
    if current == digest:
        raise PromotionError(f"{env} is already at {digest}")
    append_history(history_path(root, env), "promote", digest)
    write_digest(root, env, digest)
    return digest


def rollback(root: pathlib.Path, env: str) -> str:
    if env not in ENVS:
        raise PromotionError(f"unknown env {env}")
    digests = history_digests(history_path(root, env).read_text())
    if not digests:
        raise PromotionError(f"{env} has no history")
    current = digests[-1]
    prior = next((digest for digest in reversed(digests[:-1]) if digest != current), None)
    if prior is None:
        raise PromotionError(f"{env} has no prior digest")
    append_history(history_path(root, env), "rollback", prior)
    write_digest(root, env, prior)
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
