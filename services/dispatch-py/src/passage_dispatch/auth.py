from __future__ import annotations

import base64
import binascii
import json
import threading
import time
import uuid
from collections.abc import Callable, Mapping
from dataclasses import dataclass

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

HEADER = "X-Passage-Service-Token"
SERVICE_NAME = "dispatch"
DUTY_CREATE = "dispatch.run.create"
DUTY_READ = "dispatch.run.read"
SERVICE_GRANTS: Mapping[str, tuple[str, ...]] = {"fieldservice": (DUTY_CREATE, DUTY_READ)}

TOKEN_CONTEXT = b"passage.service-token.v1."
MAX_TOKEN_BYTES = 4096
MAX_TTL_SECONDS = 60
CLOCK_SKEW_SECONDS = 5
REPLAY_CAPACITY = 100_000


class TokenError(Exception):
    pass


@dataclass(frozen=True)
class Caller:
    tenant_id: str
    legal_entity_id: str
    actor: str
    issuer: str
    duties: frozenset[str]


def _canonical_uuid(value: object) -> bool:
    try:
        return isinstance(value, str) and str(uuid.UUID(value)) == value and value != str(uuid.UUID(int=0))
    except ValueError:
        return False


class ServiceVerifier:
    def __init__(
        self,
        keys: Mapping[str, Mapping[str, Ed25519PublicKey]],
        self_name: str = SERVICE_NAME,
        grants: Mapping[str, tuple[str, ...]] = SERVICE_GRANTS,
        now: Callable[[], float] = time.time,
        capacity: int = REPLAY_CAPACITY,
    ) -> None:
        self._keys, self._self, self._grants, self._now, self._capacity = keys, self_name, grants, now, capacity
        self._seen: dict[str, int] = {}
        self._lock = threading.Lock()

    @classmethod
    def from_env(cls, getenv: Callable[[str], str | None]) -> ServiceVerifier | None:
        raw = (getenv("PASSAGE_SERVICE_PUBLIC_KEYS") or "").strip()
        if not raw:
            return None
        keys: dict[str, dict[str, Ed25519PublicKey]] = {}
        for entry in raw.split(","):
            ident, _, encoded = entry.strip().partition("=")
            name, _, kid = ident.partition(":")
            try:
                key = Ed25519PublicKey.from_public_bytes(base64.b64decode(encoded, validate=True))
            except (ValueError, binascii.Error) as exc:
                raise ValueError(f"PASSAGE_SERVICE_PUBLIC_KEYS entry {ident!r} is not name[:kid]=base64 Ed25519 public key") from exc
            if not name or kid in keys.get(name, {}):
                raise ValueError(f"PASSAGE_SERVICE_PUBLIC_KEYS entry {ident!r} is empty or repeated")
            keys.setdefault(name, {})[kid] = key
        return cls(keys, getenv("PASSAGE_SERVICE_NAME") or SERVICE_NAME)

    def verify(self, token: str) -> Caller:
        if len(token) > MAX_TOKEN_BYTES:
            raise TokenError("token too large")
        body, dot, signature = token.partition(".")
        try:
            payload = json.loads(_b64url(body))
            raw_signature = _b64url(signature)
        except (ValueError, binascii.Error) as exc:
            raise TokenError("malformed token") from exc
        if not dot or not isinstance(payload, dict):
            raise TokenError("malformed token")
        issuer, kid = payload.get("iss"), payload.get("kid", "")
        key = self._keys.get(issuer, {}).get(kid) if isinstance(issuer, str) and isinstance(kid, str) else None
        if key is None:
            raise TokenError("unknown issuer")
        try:
            key.verify(raw_signature, TOKEN_CONTEXT + body.encode())
        except InvalidSignature as exc:
            raise TokenError("bad signature") from exc
        now = self._now()
        iat, exp, jti, duties = payload.get("iat"), payload.get("exp"), payload.get("jti"), payload.get("duties")
        if (
            payload.get("aud") != self._self
            or not isinstance(jti, str) or not jti
            or not isinstance(payload.get("act"), str) or not payload["act"]
            or not isinstance(iat, int) or not isinstance(exp, int)
            or exp <= now or exp - iat > MAX_TTL_SECONDS or iat > now + CLOCK_SKEW_SECONDS
        ):
            raise TokenError("token not valid for this service now")
        if not _canonical_uuid(payload.get("tid")) or not _canonical_uuid(payload.get("le")):
            raise TokenError("token names no tenant or legal entity")
        granted = self._grants.get(issuer, ())
        if not isinstance(duties, list) or not duties or any(d not in granted for d in duties):
            raise TokenError("token carries duties outside the service grant")
        self._remember(issuer + ":" + jti, exp, int(now))
        return Caller(payload["tid"], payload["le"], payload["act"], issuer, frozenset(duties))

    def _remember(self, key: str, exp: int, now: int) -> None:
        with self._lock:
            if len(self._seen) >= self._capacity:
                self._seen = {k: e for k, e in self._seen.items() if e > now}
            if key in self._seen or len(self._seen) >= self._capacity:
                raise TokenError("token replayed or replay cache full")
            self._seen[key] = exp


def _b64url(value: str) -> bytes:
    return base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
