#!/usr/bin/env python3
"""Offline install bundle for a cluster with no internet egress (IL6 readiness, #603).

An air-gapped enclave (Azure Government Secret, SIPRNet only) cannot reach ghcr.io, github.com, quay.io, Docker Hub or any
chart repository. The bundle is everything an install needs, as one signed archive:

  images/<slug>/        every container image as an OCI layout, pinned by digest
  signatures/<slug>/    the image with its signature and SPDX SBOM attestation made with the offline key (`cosign save`),
                        so the in-cluster policy can verify them against the offline public key
  sboms/<slug>.spdx.json  the SBOM (syft) that the attestation carries
  charts/               every Helm chart: the ones in this repository (helm package) and the third-party ones the
                        Argo CD Applications pull (helm pull)
  policies/             Kyverno admission policies (the offline variants) and the OPA authorization bundle
  manifest.json         what is inside, with image digests and chart versions
  SHA256SUMS            a hash of every file
  <bundle>.tar.gz.sig   cosign signature of the archive by the release key

    docker run -d -p 5000:5000 registry:2                               # throwaway staging registry for signing
    make -C deploy offline-bundle OUT=dist/offline KEY=cosign.key     # build, on a connected machine
    python3 deploy/offline/offline_bundle.py verify dist/offline/passage-offline.tar.gz --key cosign.pub
    python3 deploy/offline/offline_bundle.py install dist/offline/passage-offline.tar.gz --registry registry.enclave:5000 \\
        --git-url https://git.enclave/passage.git --key cosign.pub        # inside the enclave

`build` checks each image's upstream signature against provenance.json (an image or chart with no entry fails the
build), then signs and attests every image with the offline key in a staging registry; it needs skopeo, syft, cosign
and helm. `verify` and `install` need only skopeo, cosign and helm, all of which work against a local registry. Argo CD syncs from an
in-enclave Git mirror: `render-argocd` rewrites every repoURL to the mirror and every chart repository to the in-enclave
OCI registry. Kyverno verifies signatures against a static public key held in the cluster
(deploy/policies/admission-offline), not keyless Sigstore, which needs Fulcio and Rekor on the internet.

Stdlib only, Python 3.11+. External programs run through `Runner`, so the tests replace them. A CI run that installs the
bundle on a cluster with egress blocked is a live proof and is tracked on #213.
"""

from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path
from typing import Callable, Iterable, Protocol

HERE = Path(__file__).resolve().parent
DEPLOY = HERE.parent
REPO = DEPLOY.parent

GIT_URL = "https://github.com/BenjaminSRussell/Passage.git"
IMAGE_LINE = re.compile(r"^\s*-?\s*image:\s*[\"']?([^\s\"']+)", re.M)
REPO_URL_LINE = re.compile(r"^(\s*-?\s*)repoURL:\s*(\S+)\s*$", re.M)
DIGEST = re.compile(r"@sha256:[0-9a-f]{64}$")
ENCLAVE_MARKER = "REGISTRY_HOST"
OFFLINE_POLICIES = DEPLOY / "policies" / "admission-offline"
EXTRA_IMAGES = HERE / "extra-images.txt"
PROVENANCE = HERE / "provenance.json"


class Runner(Protocol):
    def __call__(self, cmd: list[str], *, cwd: Path | None = None) -> str: ...


def run(cmd: list[str], *, cwd: Path | None = None) -> str:
    proc = subprocess.run(cmd, capture_output=True, text=True, cwd=cwd)
    if proc.returncode != 0:
        raise RuntimeError(f"{' '.join(cmd)} failed ({proc.returncode}): {proc.stderr.strip()}")
    return proc.stdout


def slug(ref: str) -> str:
    return re.sub(r"[^A-Za-z0-9._-]+", "_", ref).strip("_")


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


# ---- what is in the bundle ------------------------------------------------------------------------------------------


def repo_charts() -> list[Path]:
    out = []
    for chart in sorted(REPO.rglob("Chart.yaml")):
        rel = chart.relative_to(REPO).as_posix()
        if "node_modules" in rel or rel.startswith((".worktrees/", ".claude/")) or "/testdata/" in rel:
            continue
        out.append(chart.parent)
    return out


def chart_name(chart_dir: Path) -> str:
    m = re.search(r"^name:\s*(\S+)", (chart_dir / "Chart.yaml").read_text(encoding="utf-8"), re.M)
    return m.group(1) if m else chart_dir.name


def third_party_charts() -> list[dict[str, str]]:
    """Charts the Argo CD Applications pull from a chart repository: {repo, chart, version}, deduplicated."""
    seen: dict[tuple[str, str, str], dict[str, str]] = {}
    for path in sorted((DEPLOY / "envs").glob("*/apps/*.yaml")) + sorted((DEPLOY / "argocd").glob("*.yaml")):
        text = path.read_text(encoding="utf-8")
        # a source block starts at a repoURL line and runs to the next repoURL line or document end
        for block in re.split(r"(?m)^(?=\s*-?\s*repoURL:)", text):
            repo = re.search(r"repoURL:\s*(\S+)", block)
            chart = re.search(r"^\s*chart:\s*(\S+)", block, re.M)
            rev = re.search(r"^\s*targetRevision:\s*(\S+)", block, re.M)
            if repo and chart and rev and repo.group(1) != GIT_URL:
                key = (repo.group(1), chart.group(1), rev.group(1))
                seen[key] = {"repo": key[0], "chart": key[1], "version": key[2]}
    # cert-manager and Argo CD are installed by OpenTofu (deploy/tofu/cluster), with their versions pinned in variables.tf.
    tf = (DEPLOY / "tofu" / "cluster" / "variables.tf").read_text(encoding="utf-8")
    for var, repo, chart in (("cert_manager_chart_version", "https://charts.jetstack.io", "cert-manager"), ("argocd_chart_version", "https://argoproj.github.io/argo-helm", "argo-cd")):
        m = re.search(rf'variable "{var}" \{{.*?default\s*=\s*"([^"]+)"', tf, re.S)
        if m:
            seen[(repo, chart, m.group(1))] = {"repo": repo, "chart": chart, "version": m.group(1)}
    return list(seen.values())


def extra_images() -> list[str]:
    """Images no in-repo chart names: operator and controller images of third-party charts, one reference per line."""
    if not EXTRA_IMAGES.exists():
        return []
    return [l.strip() for l in EXTRA_IMAGES.read_text(encoding="utf-8").splitlines() if l.strip() and not l.lstrip().startswith("#")]


def images_in(manifest: str) -> set[str]:
    body = "\n".join(l for l in manifest.splitlines() if not l.lstrip().startswith("#"))
    return {m.group(1) for m in IMAGE_LINE.finditer(body)}


def collect_images(runner: Runner = run) -> list[str]:
    """Every image the in-repo charts render with their defaults, plus extra-images.txt."""
    found: set[str] = set(extra_images())
    for chart in repo_charts():
        try:
            out = runner(["helm", "template", "offline", str(chart)])
        except RuntimeError:
            continue  # a chart with required values renders in the real install; its images come from extra-images.txt
        found |= images_in(out)
    return sorted(found)


def plan(runner: Runner = run) -> dict:
    return {
        "schemaVersion": 1,
        "images": [{"ref": r, "slug": slug(r)} for r in collect_images(runner)],
        "repoCharts": [{"name": chart_name(c), "path": c.relative_to(REPO).as_posix()} for c in repo_charts()],
        "thirdPartyCharts": third_party_charts(),
        "policies": ["deploy/policies/admission-offline", "policies/authz"],
    }


# ---- build ----------------------------------------------------------------------------------------------------------


def resolve_digest(ref: str, runner: Runner) -> str:
    if DIGEST.search(ref):
        return ref
    digest = runner(["skopeo", "inspect", "--format", "{{.Digest}}", f"docker://{ref}"]).strip()
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise RuntimeError(f"skopeo returned no digest for {ref}: {digest!r}")
    return f"{repository(ref)}@{digest}"


def repository(ref: str) -> str:
    """The reference without its tag or digest. skopeo refuses a reference that has both a tag and a digest."""
    name = ref.split("@")[0]
    head, sep, last = name.rpartition("/")
    return f"{head}{sep}{last.split(':')[0]}"


def load_provenance(path: Path = PROVENANCE) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def image_provenance(ref: str, provenance: dict) -> dict | None:
    """The first provenance entry whose match (a glob over the repository, no tag or digest) fits ref, else None."""
    repo = repository(ref)
    for entry in provenance.get("images", []):
        if fnmatch.fnmatchcase(repo, entry["match"]):
            return entry
    return None


def chart_provenance(chart: str, provenance: dict) -> dict | None:
    return next((c for c in provenance.get("charts", []) if c["chart"] == chart), None)


def missing_provenance(manifest: dict, provenance: dict) -> list[str]:
    """Images and third-party charts the provenance file says nothing about, or says something unusable about."""
    problems = []
    for item in manifest["images"]:
        entry = image_provenance(item["ref"], provenance)
        if entry is None:
            problems.append(f"image {item['ref']}: no provenance entry (an expected signer identity, a key, or unsignedUpstream with a reason)")
        elif not (entry.get("unsignedUpstream") or entry.get("key") or (entry.get("certificateIdentityRegexp") and entry.get("certificateOidcIssuer"))):
            problems.append(f"image {item['ref']}: provenance entry {entry['match']!r} names no identity, key or unsignedUpstream reason")
    for c in manifest["thirdPartyCharts"]:
        entry = chart_provenance(c["chart"], provenance)
        if entry is None or not (entry.get("keyring") or entry.get("sha256") or entry.get("unverified")):
            problems.append(f"chart {c['chart']}: no provenance entry (a keyring, a sha256 pin, or unverified with a reason)")
    return problems


def verify_upstream(ref: str, entry: dict, runner: Runner) -> str:
    """Check the publisher's signature on a digest-pinned image; returns verified or unsigned."""
    if entry.get("unsignedUpstream"):
        return "unsigned"
    if entry.get("key"):
        runner(["cosign", "verify", "--key", str(REPO / entry["key"]), ref])
    else:
        runner(["cosign", "verify", "--certificate-identity-regexp", entry["certificateIdentityRegexp"],
                "--certificate-oidc-issuer", entry["certificateOidcIssuer"], ref])
    return "verified"


def pull_chart(c: dict, entry: dict, dest: Path, runner: Runner) -> Path:
    """helm pull one third-party chart into dest, checked as its provenance entry says; returns the archive."""
    tmp = dest / ".pull" / c["chart"]
    tmp.mkdir(parents=True, exist_ok=True)
    cmd = ["helm", "pull", c["chart"], "--repo", c["repo"], "--version", c["version"], "--destination", str(tmp)]
    if entry.get("keyring"):
        cmd += ["--verify", "--keyring", str(REPO / entry["keyring"])]
    runner(cmd)
    archives = sorted(tmp.glob("*.tgz"))
    if len(archives) != 1:
        raise RuntimeError(f"helm pull {c['chart']} left {len(archives)} archives")
    if entry.get("sha256") and sha256(archives[0]) != entry["sha256"]:
        raise RuntimeError(f"chart {c['chart']} {c['version']}: sha256 {sha256(archives[0])} does not match the pinned {entry['sha256']}")
    final = dest / archives[0].name
    archives[0].replace(final)
    return final


def build(out: Path, key: str, runner: Runner = run, now: Callable[[], str] | None = None, *,
          staging: str = "localhost:5000", staging_insecure: bool = False, provenance: dict | None = None) -> Path:
    """Build the archive in `out` and return its path. Needs network access to the registries: run it where that exists.

    For each image: check the publisher's signature (provenance.json), copy it by digest into the staging registry,
    write an SPDX SBOM (syft), sign it and attest the SBOM there with the offline key, and save image, signature and
    attestation (`cosign save`). The offline Kyverno policy requires exactly that signature and attestation. Any step
    that fails stops the build: nothing is half-signed. The staging registry is a throwaway one next to the build
    (`docker run -p 5000:5000 registry:2`), because cosign attaches signatures through a registry.
    """
    provenance = load_provenance() if provenance is None else provenance
    stage = out / "stage"
    if stage.exists():
        shutil.rmtree(stage)
    (stage / "images").mkdir(parents=True)
    (stage / "charts").mkdir()
    (stage / "sboms").mkdir()
    manifest = plan(runner)
    problems = missing_provenance(manifest, provenance)
    if problems:
        raise RuntimeError("provenance.json does not cover the bundle:\n  " + "\n  ".join(problems))
    dest_tls = ["--dest-tls-verify=false"] if staging_insecure else []
    src_tls = ["--src-tls-verify=false"] if staging_insecure else []
    insecure = ["--allow-insecure-registry"] if staging_insecure else []
    pinned = []
    for item in manifest["images"]:
        ref = resolve_digest(item["ref"], runner)
        upstream = verify_upstream(ref, image_provenance(item["ref"], provenance), runner)
        staged = f"{staging}/{mirrored_path(ref)}"
        runner(["skopeo", "copy", "--all", "--preserve-digests", *dest_tls, f"docker://{ref}", f"docker://{staged}"])
        d = stage / "images" / item["slug"]
        runner(["skopeo", "copy", "--all", "--preserve-digests", *src_tls, f"docker://{staged}", f"oci:{d}:image"])
        sbom = stage / "sboms" / f"{item['slug']}.spdx.json"
        runner(["syft", "scan", f"oci-dir:{d}", "-o", f"spdx-json={sbom}"])
        # The offline key signs the digest and attests the SBOM; there is no transparency log in the enclave.
        runner(["cosign", "sign", "--yes", "--key", key, "--tlog-upload=false", *insecure, staged])
        runner(["cosign", "attest", "--yes", "--key", key, "--tlog-upload=false", *insecure, "--type", "spdxjson", "--predicate", str(sbom), staged])
        runner(["cosign", "save", *insecure, "--dir", str(stage / "signatures" / item["slug"]), staged])
        pinned.append({"ref": item["ref"], "pinned": ref, "slug": item["slug"], "upstream": upstream, "sbom": f"sboms/{sbom.name}"})
    manifest["images"] = pinned
    for c in manifest["repoCharts"]:
        runner(["helm", "package", str(REPO / c["path"]), "--destination", str(stage / "charts")])
    for c in manifest["thirdPartyCharts"]:
        entry = chart_provenance(c["chart"], provenance)
        archive = pull_chart(c, entry, stage / "charts", runner)
        c["archive"] = f"charts/{archive.name}"
        c["provenance"] = "keyring" if entry.get("keyring") else "sha256" if entry.get("sha256") else "unverified"
    shutil.rmtree(stage / "charts" / ".pull", ignore_errors=True)
    for pol in manifest["policies"]:
        src = REPO / pol
        if src.exists():
            shutil.copytree(src, stage / "policies" / Path(pol).name, dirs_exist_ok=True)
    (stage / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    write_sums(stage)
    archive = out / "passage-offline.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(stage, arcname="passage-offline")
    runner(["cosign", "sign-blob", "--yes", "--key", key, "--output-signature", str(archive) + ".sig", str(archive)])
    return archive


def pin_charts(runner: Runner = run, path: Path = PROVENANCE, work: Path | None = None) -> list[str]:
    """On the connected build machine: pull every third-party chart that has neither a keyring nor a sha256 pin and
    record the archive's sha256 in provenance.json, so later builds refuse a changed archive. Returns the charts pinned."""
    provenance = load_provenance(path)
    work = work or Path(tempfile.mkdtemp())
    pinned = []
    for c in third_party_charts():
        entry = chart_provenance(c["chart"], provenance)
        if entry is None:
            entry = {"chart": c["chart"]}
            provenance.setdefault("charts", []).append(entry)
        if entry.get("keyring") or entry.get("sha256"):
            continue
        archive = pull_chart(c, {}, work, runner)
        entry["sha256"] = sha256(archive)
        entry["version"] = c["version"]
        entry.pop("unverified", None)
        pinned.append(c["chart"])
    path.write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
    return pinned


def write_sums(stage: Path) -> None:
    lines = [f"{sha256(p)}  {p.relative_to(stage).as_posix()}" for p in sorted(stage.rglob("*")) if p.is_file() and p.name != "SHA256SUMS"]
    (stage / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")


# ---- verify and install (inside the enclave) -----------------------------------------------------------------------------


def extract(archive: Path, dest: Path) -> Path:
    if dest.exists():
        shutil.rmtree(dest)
    dest.mkdir(parents=True)
    base = dest.resolve()
    with tarfile.open(archive, "r:gz") as tar:
        for m in tar.getmembers():
            target = (dest / m.name).resolve()
            if not target.is_relative_to(base) or m.issym() or m.islnk() or not (m.isfile() or m.isdir()):
                raise RuntimeError(f"unsafe path in archive: {m.name}")
        # the "data" filter (Python 3.12+, backported to 3.11.4) refuses the same things again and drops modes and owners
        if hasattr(tarfile, "data_filter"):
            tar.extractall(dest, filter="data")
        else:  # pragma: no cover - Python before 3.11.4; every member was checked above
            tar.extractall(dest)
    return dest / "passage-offline"


def check_sums(root: Path) -> list[str]:
    """Problems with the bundle contents: a hash that does not match, a file not listed, a listed file missing."""
    problems: list[str] = []
    listed: dict[str, str] = {}
    for line in (root / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        digest, _, name = line.partition("  ")
        listed[name] = digest
    for name, digest in listed.items():
        p = root / name
        if not p.is_file():
            problems.append(f"missing: {name}")
        elif sha256(p) != digest:
            problems.append(f"hash mismatch: {name}")
    for p in sorted(root.rglob("*")):
        rel = p.relative_to(root).as_posix()
        if p.is_file() and rel != "SHA256SUMS" and rel not in listed:
            problems.append(f"not in SHA256SUMS: {rel}")
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
    for img in manifest["images"]:
        if not DIGEST.search(img["pinned"]):
            problems.append(f"image {img['ref']} is not pinned by digest")
        if not (root / "images" / img["slug"]).is_dir():
            problems.append(f"image {img['ref']} has no OCI layout")
        if not (root / "signatures" / img["slug"]).is_dir():
            problems.append(f"image {img['ref']} has no signature or SBOM attestation")
        if not (root / img.get("sbom", f"sboms/{img['slug']}.spdx.json")).is_file():
            problems.append(f"image {img['ref']} has no SBOM")
    return problems


def verify(archive: Path, key: str, runner: Runner = run, work: Path | None = None) -> Path:
    """Check the archive signature against the offline public key, then every file hash. Returns the extracted root."""
    runner(["cosign", "verify-blob", "--key", key, "--signature", str(archive) + ".sig", "--insecure-ignore-tlog=true", str(archive)])
    root = extract(archive, work or archive.parent / "extracted")
    problems = check_sums(root)
    if problems:
        raise RuntimeError("bundle verification failed:\n  " + "\n  ".join(problems))
    return root


def install(archive: Path, registry: str, git_url: str, key: str, runner: Runner = run, work: Path | None = None) -> Path:
    """Verify the bundle, push its images (with signatures) and charts into the enclave registry, and render Argo CD for the mirror."""
    root = verify(archive, key, runner, work)
    manifest = json.loads((root / "manifest.json").read_text(encoding="utf-8"))
    for img in manifest["images"]:
        target = f"{registry}/{mirrored_path(img['pinned'])}"
        runner(["skopeo", "copy", "--all", "--dest-tls-verify=true", f"oci:{root / 'images' / img['slug']}:image", f"docker://{target}"])
        runner(["cosign", "load", "--dir", str(root / "signatures" / img["slug"]), target])
    for chart in sorted((root / "charts").glob("*.tgz")):
        runner(["helm", "push", str(chart), f"oci://{registry}/charts"])
    out = root / "argocd"
    render_argocd(git_url, registry, out)
    # The offline Kyverno policy needs the same public key the archive was verified with. A KMS or HSM URI cannot be
    # read here: then the operator renders the policy with `render-policies` and the exported PEM.
    if Path(key).is_file():
        render_policies(Path(key).read_text(encoding="utf-8"), registry, out / "policies")
    return out


def mirrored_path(ref: str) -> str:
    """Path of an image in the enclave registry: the original registry host dropped, repository and digest kept."""
    name = repository(ref) if "@" in ref else ref
    first, _, rest = name.partition("/")
    path = rest if rest and ("." in first or ":" in first) else name
    digest = ref.split("@", 1)[1] if "@" in ref else ""
    return f"{path}@{digest}" if digest else path


# ---- Argo CD against the in-enclave mirror -------------------------------------------------------------------------------


def rewrite_repo_urls(text: str, git_url: str, registry: str) -> str:
    """Point every Argo CD source at the enclave: Git sources at the mirror, chart repositories at the OCI registry."""

    def sub(m: re.Match[str]) -> str:
        url = m.group(2)
        if url == GIT_URL:
            return f"{m.group(1)}repoURL: {git_url}"
        if url.startswith(("http://", "https://")):
            return f"{m.group(1)}repoURL: {registry}/charts"
        return m.group(0)

    return REPO_URL_LINE.sub(sub, text)


def render_argocd(git_url: str, registry: str, out: Path) -> list[Path]:
    """Write the Argo CD roots and every environment's Applications with their sources rewritten to the enclave."""
    written = []
    for src in sorted((DEPLOY / "argocd").glob("*.yaml")) + sorted((DEPLOY / "envs").glob("*/apps/*.yaml")):
        dest = out / src.relative_to(DEPLOY)
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(rewrite_repo_urls(src.read_text(encoding="utf-8"), git_url, registry), encoding="utf-8")
        written.append(dest)
    return written


def render_policies(public_key_pem: str, registry: str, out: Path) -> list[Path]:
    """The offline Kyverno policies with the registry host and the cosign public key filled in."""
    written = []
    for src in sorted(OFFLINE_POLICIES.glob("*.yaml")):
        text = src.read_text(encoding="utf-8").replace(ENCLAVE_MARKER, registry)
        key_lines = public_key_pem.strip().splitlines()
        text = re.sub(
            r"^(\s*)REPLACE_WITH_COSIGN_PUBLIC_KEY$",
            lambda m: "\n".join(m.group(1) + line for line in key_lines),
            text,
            flags=re.M,
        )
        dest = out / src.name
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(text, encoding="utf-8")
        written.append(dest)
    return written


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("plan", help="print what the bundle would hold (needs helm)")
    b = sub.add_parser("build", help="build and sign the archive (needs skopeo, cosign, helm and registry access)")
    b.add_argument("--out", type=Path, default=Path("dist/offline"))
    b.add_argument("--key", required=True, help="cosign private key (cosign.key, or a KMS/HSM URI)")
    b.add_argument("--staging-registry", default="localhost:5000", help="throwaway registry the images are signed in")
    b.add_argument("--staging-insecure", action="store_true", help="the staging registry speaks plain http")
    sub.add_parser("pin-charts", help="pin the sha256 of every unverified third-party chart in provenance.json (needs helm and network)")
    v = sub.add_parser("verify", help="verify signature and hashes, offline")
    v.add_argument("archive", type=Path)
    v.add_argument("--key", required=True, help="cosign public key")
    i = sub.add_parser("install", help="verify, then load the registry and render Argo CD for the enclave")
    i.add_argument("archive", type=Path)
    i.add_argument("--registry", required=True)
    i.add_argument("--git-url", required=True)
    i.add_argument("--key", required=True)
    rp = sub.add_parser("render-policies", help="write the offline Kyverno policies for the enclave registry and public key")
    rp.add_argument("--key", type=Path, required=True, help="cosign public key (PEM file)")
    rp.add_argument("--registry", required=True)
    rp.add_argument("--out", type=Path, required=True)
    r = sub.add_parser("render-argocd", help="rewrite the Argo CD sources for the in-enclave Git mirror and registry")
    r.add_argument("--git-url", required=True)
    r.add_argument("--registry", required=True)
    r.add_argument("--out", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        if args.cmd == "plan":
            print(json.dumps(plan(), indent=2))
        elif args.cmd == "build":
            args.out.mkdir(parents=True, exist_ok=True)
            print(build(args.out, args.key, staging=args.staging_registry, staging_insecure=args.staging_insecure))
        elif args.cmd == "pin-charts":
            for name in pin_charts():
                print(f"pinned {name}")
        elif args.cmd == "verify":
            root = verify(args.archive, args.key)
            print(f"bundle verified: {root}")
        elif args.cmd == "install":
            print(install(args.archive, args.registry, args.git_url, args.key))
        elif args.cmd == "render-policies":
            for p in render_policies(args.key.read_text(encoding="utf-8"), args.registry, args.out):
                print(p)
        elif args.cmd == "render-argocd":
            for p in render_argocd(args.git_url, args.registry, args.out):
                print(p)
    except (RuntimeError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
