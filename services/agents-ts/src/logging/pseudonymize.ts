// Log pseudonymization (issue 605, EU Data Boundary): the TypeScript twin of platform/go/logpseudo.
//
// A field whose key names a person (user, email, name, person, subject, ip, ...) and any email address inside a string are
// replaced by a stable pseudonym before a log line is written: HMAC-SHA256 under the tenant MAC key, derived from the
// fieldcrypt master key exactly as platform/go/fieldcrypt MAC() does (HKDF-SHA256, salt = tenant id, info
// "passage.fieldcrypt.mac.v1"). The same value maps to the same pseudonym inside one tenant and to another across tenants.
// With no key, or no tenant id on the line, the value is replaced by a fixed marker and never passed through.
// The keys come from the same environment as the Go services (FIELDCRYPT_KEYS, FIELDCRYPT_KEYS_FILE,
// FIELDCRYPT_CURRENT_KEY_ID); an entry wrapped by a key vault (wrapped:...) cannot be unwrapped here and counts as no key.
//
// Limits: a person's name in free text or under a key that does not name a person is not recognised; log it under name,
// displayName or user. A nested value that is not a plain object or array is checked as text for email addresses only.

import { createHmac, hkdfSync } from "node:crypto";
import { readFileSync } from "node:fs";
import { profileFromEnv } from "../cloudprofile/index.js";

export const ENV_PSEUDONYMIZE = "LOG_PSEUDONYMIZE";
export const REDACTED = "[redacted]";
export const EMAIL_REDACTED = "[redacted-email]";
export const PREFIX = "pn_";

const MAC_KDF_INFO = "passage.fieldcrypt.mac.v1";
const MAC_DOMAIN = "passage.log.pseudonym.v1\0";
const KEY_SIZE = 32;
const BASE64_32 = /^[A-Za-z0-9+/]{43}=$/;
const EMAIL = /[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+/g;

/** The tenant MAC keys: the MAC half of platform/go/fieldcrypt, read from the same environment. */
export class MacKeys {
  readonly #current: Buffer;

  constructor(current: Uint8Array) {
    if (current.length !== KEY_SIZE) {
      throw new Error(`fieldcrypt: key must be ${KEY_SIZE} bytes`);
    }
    this.#current = Buffer.from(current);
  }

  /** Null when no usable key is configured. A malformed entry throws: logs are never left raw because a key was misread. */
  static fromEnv(env: Record<string, string | undefined> = process.env): MacKeys | null {
    let raw = (env.FIELDCRYPT_KEYS ?? "").trim();
    const file = env.FIELDCRYPT_KEYS_FILE ?? "";
    if (file !== "") {
      raw = readFileSync(file, "utf8").trim();
    }
    if (raw === "") {
      return null;
    }
    const keys = new Map<string, Buffer>();
    for (const entry of raw.split(/[,\n]/).map((e) => e.trim()).filter((e) => e !== "")) {
      const eq = entry.indexOf("=");
      const id = eq < 0 ? entry : entry.slice(0, eq);
      const enc = eq < 0 ? "" : entry.slice(eq + 1);
      if (enc.startsWith("wrapped:")) {
        continue;
      }
      if (eq < 0 || !BASE64_32.test(enc)) {
        throw new Error(`FIELDCRYPT_KEYS entry ${JSON.stringify(id)} is not id=base64 ${KEY_SIZE}-byte key`);
      }
      keys.set(id, Buffer.from(enc, "base64"));
    }
    const key = keys.get(env.FIELDCRYPT_CURRENT_KEY_ID ?? "");
    return key === undefined ? null : new MacKeys(key);
  }

  /** HMAC-SHA256(message) under the tenant key derived from the master key. */
  mac(tenantId: string, message: string): Buffer {
    if (tenantId === "") {
      throw new Error("fieldcrypt: mac needs a tenant id");
    }
    const derived = Buffer.from(hkdfSync("sha256", this.#current, Buffer.from(tenantId, "utf8"), MAC_KDF_INFO, KEY_SIZE));
    return createHmac("sha256", derived).update(message, "utf8").digest();
  }
}

/** The pseudonym of `value` for a tenant, as logged: pn_ and 16 hex digits. Null without a key or tenant. */
export function pseudonym(keys: MacKeys | null, tenantId: string, value: string): string | null {
  if (keys === null || tenantId === "") {
    return null;
  }
  return PREFIX + keys.mac(tenantId, MAC_DOMAIN + value.trim().toLowerCase()).subarray(0, 8).toString("hex");
}

const PERSONAL_WORDS = new Set([
  "user", "username", "userid", "email", "mail", "person", "subject", "sub", "ip", "phone", "actor", "principal", "approver",
  "requester", "caller", "employee", "contact", "firstname", "lastname", "fullname", "displayname", "givenname", "surname", "upn",
]);
const NAME_QUALIFIERS = new Set(["first", "last", "full", "display", "given", "family", "person", "user", "customer", "employee", "contact", "author"]);

function splitWords(key: string): string[] {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2")
    .split(/[^A-Za-z0-9]+/)
    .filter((w) => w !== "")
    .map((w) => w.toLowerCase());
}

/** Whether a key names a person: user, email, name, person, subject, ip and the like, in snake, kebab, dotted or camel case. */
export function isPersonalKey(key: string): boolean {
  const words = splitWords(key);
  if (words.some((w) => PERSONAL_WORDS.has(w))) {
    return true;
  }
  if (!words.includes("name")) {
    return false;
  }
  return words.length === 1 || words.some((w) => NAME_QUALIFIERS.has(w));
}

function isTenantKey(key: string): boolean {
  const k = key.toLowerCase().replace(/[_-]/g, "");
  return k === "tenant" || k === "tenantid";
}

function findTenant(value: unknown): string {
  if (value === null || typeof value !== "object") {
    return "";
  }
  for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
    if (isTenantKey(k) && typeof v === "string") {
      return v;
    }
  }
  for (const v of Object.values(value as Record<string, unknown>)) {
    const found = Array.isArray(v) ? "" : findTenant(v);
    if (found !== "") {
      return found;
    }
  }
  return "";
}

class Scrubber {
  constructor(
    readonly keys: MacKeys | null,
    readonly tenantId: string,
  ) {}

  text(text: string): string {
    return text.includes("@") ? text.replace(EMAIL, (m) => pseudonym(this.keys, this.tenantId, m) ?? EMAIL_REDACTED) : text;
  }

  value(key: string, v: unknown, inPersonal: boolean): unknown {
    const personal = inPersonal || isPersonalKey(key);
    if (Array.isArray(v)) {
      return v.map((item) => this.value(key, item, personal));
    }
    if (v !== null && typeof v === "object" && Object.getPrototypeOf(v) === Object.prototype) {
      return Object.fromEntries(Object.entries(v).map(([k, item]) => [k, this.value(k, item, personal)]));
    }
    if (personal) {
      if (v === undefined || v === null || v === "") {
        return v;
      }
      return pseudonym(this.keys, this.tenantId, String(v)) ?? REDACTED;
    }
    if (typeof v === "string") {
      return this.text(v);
    }
    if (v instanceof Error) {
      return this.text(v.message);
    }
    if (v !== null && typeof v === "object") {
      const shown = JSON.stringify(v) ?? "";
      return shown.includes("@") && this.text(shown) !== shown ? this.text(shown) : v;
    }
    return v;
  }
}

/**
 * Returns the record with personal values replaced. The tenant comes from `tenantId` (or `tenant`, `tenant_id`) on the
 * record, at any depth; those fields themselves pass through. `message` is scrubbed for email addresses.
 */
export function pseudonymizeRecord(record: Record<string, unknown>, keys: MacKeys | null, tenantId = ""): Record<string, unknown> {
  const s = new Scrubber(keys, tenantId !== "" ? tenantId : findTenant(record));
  return Object.fromEntries(Object.entries(record).map(([k, v]) => [k, s.value(k, v, false)]));
}

/** Whether logs are pseudonymized: always under the eu-data-boundary overlay of the cloud profile, and with LOG_PSEUDONYMIZE=true. */
export function pseudonymizationEnabled(env: Record<string, string | undefined>): boolean {
  if (profileFromEnv(env).overlays.includes("eu-data-boundary")) {
    return true;
  }
  return (env[ENV_PSEUDONYMIZE] ?? "").trim().toLowerCase() === "true";
}

export type LogFn = (level: string, message: string, extra?: Record<string, unknown>) => void;

/**
 * The service's structured logger: one JSON line per call on stdout, `{ level, message, service, ...extra }`, with
 * personal data pseudonymized when it is enabled (see pseudonymizationEnabled). A bad profile or key configuration throws
 * at construction, so the service stops instead of logging raw values.
 */
export function createLogger(
  env: Record<string, string | undefined>,
  service: string,
  write: (line: string) => void = (line) => void process.stdout.write(line),
): LogFn {
  const on = pseudonymizationEnabled(env);
  const keys = on ? MacKeys.fromEnv(env) : null;
  return (level, message, extra = {}) => {
    const line: Record<string, unknown> = { level, message, service, ...extra };
    write(`${JSON.stringify(on ? pseudonymizeRecord(line, keys) : line)}\n`);
  };
}
