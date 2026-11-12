"""Log pseudonymization (issue 605, EU Data Boundary): the Python twin of platform/go/logpseudo.

A log field whose name says it holds a person (user, email, name, person, subject, ip, ...) and any email address inside a
string are replaced by a stable pseudonym before the record is formatted: HMAC-SHA256 under the tenant MAC key, derived
from the fieldcrypt master key exactly as platform/go/fieldcrypt MAC() does (HKDF-SHA256, salt = tenant id, info
"passage.fieldcrypt.mac.v1"). The same value gives the same pseudonym inside one tenant and another across tenants. With no
key, or no tenant on the record, the value becomes a fixed marker and is never passed through.

The tenant is the record's ``tenant_id`` (or ``tenantId``, ``tenant``) extra, else the ``tenant`` context variable. Keys come
from the Go services' environment: FIELDCRYPT_KEYS, FIELDCRYPT_KEYS_FILE, FIELDCRYPT_CURRENT_KEY_ID (a key wrapped by a key
vault, ``wrapped:...``, cannot be unwrapped here and counts as no key). It is on under the eu-data-boundary overlay of
PASSAGE_CLOUD_PROFILE (for example ``commercial+eu-data-boundary``) and with LOG_PSEUDONYMIZE=true.

Limits: a person's name in free text or under a field that does not name a person is not recognised; log it as ``name``,
``display_name`` or ``user``. A value that is not a string, list or dict is checked as text for email addresses only.
"""

from __future__ import annotations

import base64
import contextvars
import hashlib
import hmac
import logging
import re
from collections.abc import Mapping
from typing import Any

REDACTED = "[redacted]"
EMAIL_REDACTED = "[redacted-email]"
PREFIX = "pn_"
ENV_PSEUDONYMIZE = "LOG_PSEUDONYMIZE"

_MAC_KDF_INFO = b"passage.fieldcrypt.mac.v1"
_MAC_DOMAIN = "passage.log.pseudonym.v1\x00"
_KEY_SIZE = 32
_EMAIL = re.compile(r"[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+")
_BASE64_32 = re.compile(r"^[A-Za-z0-9+/]{43}=$")

#: The tenant of the code that is logging, for records that do not carry a tenant_id extra.
tenant: contextvars.ContextVar[str] = contextvars.ContextVar("log_tenant", default="")

_PERSONAL_WORDS = frozenset(
    "user username userid email mail person subject sub ip phone actor principal approver requester caller employee contact "
    "firstname lastname fullname displayname givenname surname upn".split()
)
_NAME_QUALIFIERS = frozenset("first last full display given family person user customer employee contact author".split())


class MacKeys:
    """The MAC half of platform/go/fieldcrypt: HMAC under a per-tenant key derived from the current master key."""

    def __init__(self, current: bytes) -> None:
        if len(current) != _KEY_SIZE:
            raise ValueError(f"fieldcrypt: key must be {_KEY_SIZE} bytes")
        self._current = bytes(current)

    @staticmethod
    def from_env(env: Mapping[str, str]) -> MacKeys | None:
        """None when no usable key is configured. A malformed entry raises: logs are never left raw because a key was misread."""
        raw = env.get("FIELDCRYPT_KEYS", "").strip()
        path = env.get("FIELDCRYPT_KEYS_FILE", "")
        if path:
            with open(path, encoding="utf-8") as fh:
                raw = fh.read().strip()
        keys: dict[str, bytes] = {}
        for entry in (e.strip() for e in re.split(r"[,\n]", raw)):
            if not entry:
                continue
            key_id, sep, enc = entry.partition("=")
            if enc.startswith("wrapped:"):
                continue
            if not sep or not _BASE64_32.match(enc):
                raise ValueError(f"FIELDCRYPT_KEYS entry {key_id!r} is not id=base64 {_KEY_SIZE}-byte key")
            keys[key_id] = base64.b64decode(enc)
        current = keys.get(env.get("FIELDCRYPT_CURRENT_KEY_ID", ""))
        return MacKeys(current) if current is not None else None

    def mac(self, tenant_id: str, message: str) -> bytes:
        if not tenant_id:
            raise ValueError("fieldcrypt: mac needs a tenant id")
        # HKDF-SHA256 (RFC 5869), salt = tenant id, one 32-byte block.
        prk = hmac.new(tenant_id.encode(), self._current, hashlib.sha256).digest()
        derived = hmac.new(prk, _MAC_KDF_INFO + b"\x01", hashlib.sha256).digest()
        return hmac.new(derived, message.encode(), hashlib.sha256).digest()


def pseudonym(keys: MacKeys | None, tenant_id: str, value: str) -> str | None:
    """The pseudonym of ``value`` for a tenant, as logged: ``pn_`` and 16 hex digits. None without a key or tenant."""
    if keys is None or not tenant_id:
        return None
    return PREFIX + keys.mac(tenant_id, _MAC_DOMAIN + value.strip().lower())[:8].hex()


def _words(key: str) -> list[str]:
    spaced = re.sub(r"([A-Z]+)([A-Z][a-z])", r"\1 \2", re.sub(r"([a-z0-9])([A-Z])", r"\1 \2", key))
    return [w.lower() for w in re.split(r"[^A-Za-z0-9]+", spaced) if w]


def is_personal_key(key: str) -> bool:
    """Whether a field name says it holds a person: user, email, name, person, subject, ip and the like, in any case style."""
    words = _words(key)
    if any(w in _PERSONAL_WORDS for w in words):
        return True
    if "name" not in words:
        return False
    return len(words) == 1 or any(w in _NAME_QUALIFIERS for w in words)


def _is_tenant_key(key: str) -> bool:
    return key.lower().replace("_", "").replace("-", "") in ("tenant", "tenantid")


def _find_tenant(value: Any) -> str:
    if isinstance(value, Mapping):
        for k, v in value.items():
            if _is_tenant_key(str(k)) and isinstance(v, str):
                return v
        for v in value.values():
            found = _find_tenant(v)
            if found:
                return found
    return ""


class _Scrubber:
    def __init__(self, keys: MacKeys | None, tenant_id: str) -> None:
        self.keys, self.tenant_id = keys, tenant_id

    def text(self, text: str) -> str:
        if "@" not in text:
            return text
        return _EMAIL.sub(lambda m: pseudonym(self.keys, self.tenant_id, m.group(0)) or EMAIL_REDACTED, text)

    def value(self, key: str, v: Any, in_personal: bool = False) -> Any:
        personal = in_personal or is_personal_key(key)
        if isinstance(v, Mapping):
            return {k: self.value(str(k), item, personal) for k, item in v.items()}
        if isinstance(v, (list, tuple)):
            return [self.value(key, item, personal) for item in v]
        if personal:
            if v is None or v == "":
                return v
            return pseudonym(self.keys, self.tenant_id, str(v)) or REDACTED
        if isinstance(v, str):
            return self.text(v)
        if isinstance(v, (int, float, bool)) or v is None:
            return v
        shown = str(v)
        return self.text(shown) if "@" in shown and self.text(shown) != shown else v


def pseudonymize_record(record: Mapping[str, Any], keys: MacKeys | None, tenant_id: str = "") -> dict[str, Any]:
    """The record with personal values replaced. The tenant is ``tenant_id``, else a tenant field of the record at any depth."""
    s = _Scrubber(keys, tenant_id or _find_tenant(record))
    return {k: s.value(str(k), v) for k, v in record.items()}


def enabled(env: Mapping[str, str]) -> bool:
    """On under the eu-data-boundary overlay and with LOG_PSEUDONYMIZE=true."""
    if "eu-data-boundary" in env.get("PASSAGE_CLOUD_PROFILE", "").split("+")[1:]:
        return True
    return env.get(ENV_PSEUDONYMIZE, "").strip().lower() == "true"


_STANDARD = frozenset(logging.makeLogRecord({}).__dict__) | {"message", "asctime"}


class PseudonymizingFilter(logging.Filter):
    """Attach to a handler (``handler.addFilter``): rewrites the record's message, arguments, extras and exception text."""

    def __init__(self, keys: MacKeys | None) -> None:
        super().__init__()
        self.keys = keys

    def filter(self, record: logging.LogRecord) -> bool:
        extras = {k: v for k, v in record.__dict__.items() if k not in _STANDARD}
        tenant_id = tenant.get() or _find_tenant(extras)
        s = _Scrubber(self.keys, tenant_id)
        record.msg, record.args = s.text(record.getMessage()), None
        for k, v in extras.items():
            record.__dict__[k] = s.value(k, v)
        if record.exc_info and record.exc_info[0] is not None:
            record.exc_text = s.text(logging.Formatter().formatException(record.exc_info))
            record.exc_info = None
        elif record.exc_text:
            record.exc_text = s.text(record.exc_text)
        return True


def install(handler: logging.Handler, env: Mapping[str, str]) -> bool:
    """Adds the filter to ``handler`` when pseudonymization is enabled, and reports whether it did. A malformed key raises."""
    if not enabled(env):
        return False
    handler.addFilter(PseudonymizingFilter(MacKeys.from_env(env)))
    return True


