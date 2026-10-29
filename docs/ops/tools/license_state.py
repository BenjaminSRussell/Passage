#!/usr/bin/env python3
"""Tenant license state evaluation (PSG-50, FR-50.12, BR-50.8, AC-50.8).

Pure function from (license terms, today, active users) to the state VALID | GRACE | EXPIRED and the access
rules that follow from it. The signature check (Ed25519 over the license file, LIC-1804) is NOT here: it needs a
crypto library and the ops service; see docs/ops/NOT-IMPLEMENTED.md.

Rules:
- VALID: today <= expires_at and active full users <= full_users.
- GRACE: (a) up to `grace_days` after expires_at (BR-50.8, default 30), or (b) full users above the licensed
  count (PRD 9.50.4 invariant). Case (b) has no time limit in the PRD; it lasts until the over-use is cleared
  (open decision, recorded in docs/ops/NOT-IMPLEMENTED.md).
- EXPIRED: more than `grace_days` after expires_at.
- Login: allowed in VALID and GRACE. EXPIRED blocks new user logins.
- Admin sign-in and data export are never blocked (BR-50.8), in any state.
- Warnings: LIC-1801 when 0 <= days to expiry <= 30 (also while in GRACE for over-use); LIC-1802 in GRACE;
  LIC-1803 in EXPIRED. `message()` renders the catalog text (PRD 9.50.18).
- Only full users are enforced against the licence (PRD invariant). Operational and self-service counts are
  carried for the usage report but do not move the state.

Stdlib only, Python 3.11+. Tests: python3 -m unittest discover -s docs/ops/tools
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, timedelta
from enum import Enum

DEFAULT_GRACE_DAYS = 30  # license.grace_days default
MAX_GRACE_DAYS = 60  # license.grace_days allowed range is 0-60
EXPIRY_WARNING_DAYS = 30


class State(str, Enum):
    VALID = "VALID"
    GRACE = "GRACE"
    EXPIRED = "EXPIRED"


@dataclass(frozen=True)
class License:
    issued_at: date
    expires_at: date
    full_users: int
    operational_users: int
    selfservice_users: int

    def __post_init__(self) -> None:
        if self.expires_at < self.issued_at:
            raise ValueError("expires_at must not be before issued_at")
        for name in ("full_users", "operational_users", "selfservice_users"):
            value = getattr(self, name)
            if not isinstance(value, int) or isinstance(value, bool) or value < 0:
                raise ValueError(f"{name} must be a non-negative integer")


@dataclass(frozen=True)
class Usage:
    active_full: int
    active_operational: int = 0
    active_selfservice: int = 0

    def __post_init__(self) -> None:
        for name in ("active_full", "active_operational", "active_selfservice"):
            value = getattr(self, name)
            if not isinstance(value, int) or isinstance(value, bool) or value < 0:
                raise ValueError(f"{name} must be a non-negative integer")


@dataclass(frozen=True)
class Status:
    state: State
    days_to_expiry: int  # negative once expired
    login_allowed: bool
    admin_login_allowed: bool
    export_allowed: bool
    banner: bool  # shell banner shown in GRACE (BR-50.8)
    codes: tuple[str, ...]  # LIC-180x codes that apply, most severe last

    def to_dict(self) -> dict[str, object]:
        return {
            "state": self.state.value,
            "daysToExpiry": self.days_to_expiry,
            "loginAllowed": self.login_allowed,
            "adminLoginAllowed": self.admin_login_allowed,
            "exportAllowed": self.export_allowed,
            "banner": self.banner,
            "codes": list(self.codes),
        }


def evaluate(lic: License, usage: Usage, today: date, grace_days: int = DEFAULT_GRACE_DAYS) -> Status:
    if not 0 <= grace_days <= MAX_GRACE_DAYS:
        raise ValueError(f"grace_days must be between 0 and {MAX_GRACE_DAYS}")
    days = (lic.expires_at - today).days
    over_use = usage.active_full > lic.full_users
    grace_end = lic.expires_at + timedelta(days=grace_days)

    if today > grace_end:
        state = State.EXPIRED
    elif today > lic.expires_at or over_use:
        state = State.GRACE
    else:
        state = State.VALID

    codes: list[str] = []
    if 0 <= days <= EXPIRY_WARNING_DAYS:  # only reachable in VALID or over-use GRACE
        codes.append("LIC-1801")
    if state is State.GRACE:
        codes.append("LIC-1802")
    if state is State.EXPIRED:
        codes.append("LIC-1803")

    return Status(
        state=state,
        days_to_expiry=days,
        login_allowed=state is not State.EXPIRED,
        admin_login_allowed=True,
        export_allowed=True,
        banner=state is State.GRACE,
        codes=tuple(codes),
    )


MESSAGES = {
    "LIC-1801": "License expires in {days} days.",
    "LIC-1802": "License in grace period: {used} full users active, {licensed} licensed.",
    "LIC-1803": "License expired; sign-in blocked. Administrators can still sign in and export data.",
    "LIC-1804": "License signature invalid.",
}


def message(code: str, lic: License, usage: Usage, status: Status) -> str:
    """Catalog text for one LIC code (PRD 9.50.18), with its placeholders filled."""
    if code not in MESSAGES:
        raise ValueError(f"unknown license code {code}")
    return MESSAGES[code].format(days=status.days_to_expiry, used=usage.active_full, licensed=lic.full_users)


def warning_due(lic: License, today: date, thresholds: tuple[int, ...] = (30, 7, 1)) -> int | None:
    """Return the smallest threshold (30/7/1) the license has crossed, for alert routing; None if more than 30 days out."""
    days = (lic.expires_at - today).days
    due = [t for t in thresholds if days <= t]
    return min(due) if due and days >= 0 else None
