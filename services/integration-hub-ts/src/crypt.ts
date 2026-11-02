// Field encryption at rest: a node:crypto port of platform/go/fieldcrypt,
// envelope v1, byte for byte. A value is sealed with AES-256-GCM under a data
// key derived per tenant with HKDF-SHA256 (salt = tenant id, info
// "passage.fieldcrypt.v1") from the current master key. The envelope is
//
//   0x01 | key id length | key id | 12-byte nonce | ciphertext and tag
//
// and tenant id + NUL + field name are the additional data, so a value cannot
// be moved to another tenant or column. The key id names the master key, so
// values sealed before a rotation still open while the old key stays configured.
// testdata/fieldcrypt-vectors.json holds values sealed by the Go package.

import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";

export const ENV_KEYS = "FIELDCRYPT_KEYS";
export const ENV_KEYS_FILE = "FIELDCRYPT_KEYS_FILE";
export const ENV_CURRENT_ID = "FIELDCRYPT_CURRENT_KEY_ID";

const KEY_SIZE = 32;
const NONCE_SIZE = 12;
const TAG_SIZE = 16;
const ENVELOPE_V1 = 1;
const MAX_KEY_ID_BYTES = 64;
const KDF_INFO = "passage.fieldcrypt.v1";
const BASE64_32 = /^[A-Za-z0-9+/]{43}=$/;

/** The value is not a v1 envelope, names an unknown key, or fails authentication (wrong tenant, field or key). */
export class OpenError extends Error {
  constructor() {
    super("fieldcrypt: cannot open value");
    this.name = "OpenError";
  }
}

export class FieldKeys {
  readonly #current: string;
  readonly #keys: ReadonlyMap<string, Buffer>;

  constructor(current: string, keys: ReadonlyMap<string, Uint8Array>) {
    if (current === "" || Buffer.byteLength(current) > MAX_KEY_ID_BYTES || !keys.has(current)) {
      throw new Error("fieldcrypt: the current key id must name a configured key");
    }
    const own = new Map<string, Buffer>();
    for (const [id, key] of keys) {
      if (key.length !== KEY_SIZE || id === "" || Buffer.byteLength(id) > MAX_KEY_ID_BYTES) {
        throw new Error(`fieldcrypt: key ${JSON.stringify(id)} must be ${KEY_SIZE} bytes`);
      }
      own.set(id, Buffer.from(key));
    }
    this.#current = current;
    this.#keys = own;
  }

  /**
   * Reads FIELDCRYPT_KEYS, or the mounted file FIELDCRYPT_KEYS_FILE names (which
   * wins), as comma- or newline-separated id=base64(32 bytes) pairs, and
   * FIELDCRYPT_CURRENT_KEY_ID. Null when no keys are configured.
   */
  static fromEnv(env: NodeJS.ProcessEnv = process.env): FieldKeys | null {
    let raw = (env[ENV_KEYS] ?? "").trim();
    const file = env[ENV_KEYS_FILE] ?? "";
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
      if (eq < 0 || !BASE64_32.test(enc)) {
        throw new Error(`${ENV_KEYS} entry ${JSON.stringify(id)} is not id=base64 ${KEY_SIZE}-byte key`);
      }
      if (keys.has(id)) {
        throw new Error(`${ENV_KEYS} repeats key id ${id}`);
      }
      keys.set(id, Buffer.from(enc, "base64"));
    }
    return new FieldKeys(env[ENV_CURRENT_ID] ?? "", keys);
  }

  get currentId(): string {
    return this.#current;
  }

  /** Seals `plain` for the tenant and field under the current master key. */
  seal(tenantId: string, field: string, plain: Uint8Array): Buffer {
    return this.sealWithNonce(tenantId, field, plain, randomBytes(NONCE_SIZE));
  }

  /** seal with a caller-chosen nonce. Only for known-answer tests: a nonce must never repeat under one key. */
  sealWithNonce(tenantId: string, field: string, plain: Uint8Array, nonce: Uint8Array): Buffer {
    if (tenantId === "") {
      throw new Error("fieldcrypt: seal needs a tenant id");
    }
    if (nonce.length !== NONCE_SIZE) {
      throw new Error(`fieldcrypt: nonce must be ${NONCE_SIZE} bytes`);
    }
    const cipher = createCipheriv("aes-256-gcm", this.#dataKey(this.#current, tenantId), nonce, { authTagLength: TAG_SIZE });
    cipher.setAAD(additional(tenantId, field));
    const id = Buffer.from(this.#current, "utf8");
    return Buffer.concat([Buffer.from([ENVELOPE_V1, id.length]), id, nonce, cipher.update(plain), cipher.final(), cipher.getAuthTag()]);
  }

  /** Opens a sealed value; OpenError for anything that does not authenticate for this tenant and field. */
  open(tenantId: string, field: string, sealed: Uint8Array): Buffer {
    const keyId = envelopeKeyId(sealed);
    if (keyId === null || tenantId === "" || !this.#keys.has(keyId)) {
      throw new OpenError();
    }
    const rest = Buffer.from(sealed).subarray(2 + sealed[1]!);
    if (rest.length < NONCE_SIZE + TAG_SIZE) {
      throw new OpenError();
    }
    try {
      const decipher = createDecipheriv("aes-256-gcm", this.#dataKey(keyId, tenantId), rest.subarray(0, NONCE_SIZE), { authTagLength: TAG_SIZE });
      decipher.setAAD(additional(tenantId, field));
      decipher.setAuthTag(rest.subarray(rest.length - TAG_SIZE));
      return Buffer.concat([decipher.update(rest.subarray(NONCE_SIZE, rest.length - TAG_SIZE)), decipher.final()]);
    } catch {
      throw new OpenError();
    }
  }

  #dataKey(keyId: string, tenantId: string): Buffer {
    const master = this.#keys.get(keyId) as Buffer;
    return Buffer.from(hkdfSync("sha256", master, Buffer.from(tenantId, "utf8"), KDF_INFO, KEY_SIZE));
  }
}

/** The master key id a v1 envelope names, without authenticating it; null when it is not a v1 envelope. */
export function envelopeKeyId(sealed: Uint8Array): string | null {
  if (sealed.length < 2 || sealed[0] !== ENVELOPE_V1 || sealed.length < 2 + sealed[1]!) {
    return null;
  }
  return Buffer.from(sealed.subarray(2, 2 + sealed[1]!)).toString("utf8");
}

function additional(tenantId: string, field: string): Buffer {
  return Buffer.from(`${tenantId}\x00${field}`, "utf8");
}
