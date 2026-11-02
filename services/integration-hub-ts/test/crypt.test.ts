import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, test } from "vitest";

import { ENV_CURRENT_ID, ENV_KEYS, ENV_KEYS_FILE, envelopeKeyId, FieldKeys, OpenError } from "../src/crypt.ts";

interface Vector {
  name: string;
  keyId: string;
  tenantId: string;
  field: string;
  plain: string;
  sealed: string;
}

// Sealed by platform/go/fieldcrypt (see the generator note in the file).
const doc = JSON.parse(readFileSync(new URL("../testdata/fieldcrypt-vectors.json", import.meta.url), "utf8")) as {
  keys: string;
  currentKeyId: string;
  vectors: Vector[];
};
const keys = FieldKeys.fromEnv({ [ENV_KEYS]: doc.keys, [ENV_CURRENT_ID]: doc.currentKeyId }) as FieldKeys;

describe("fieldcrypt envelope v1, against values sealed by the Go package", () => {
  test.each(doc.vectors.map((v) => [v.name, v] as const))("%s opens and re-seals to the same bytes", (_name, v) => {
    const sealed = Buffer.from(v.sealed, "base64");
    expect(envelopeKeyId(sealed)).toBe(v.keyId);
    expect(keys.open(v.tenantId, v.field, sealed).toString("utf8")).toBe(v.plain);
    // With the Go value's nonce, the port produces the Go bytes exactly (AES-GCM is deterministic given the nonce).
    const nonce = sealed.subarray(2 + v.keyId.length, 2 + v.keyId.length + 12);
    const sealer = new FieldKeys(v.keyId, new Map([[v.keyId, keyBytes(v.keyId)]]));
    expect(sealer.sealWithNonce(v.tenantId, v.field, Buffer.from(v.plain, "utf8"), nonce).toString("base64")).toBe(v.sealed);
  });

  test("a value does not open for another tenant, another field, or after tampering", () => {
    const v = doc.vectors[0]!;
    const sealed = Buffer.from(v.sealed, "base64");
    const other = doc.vectors.find((x) => x.tenantId !== v.tenantId)!.tenantId;
    expect(() => keys.open(other, v.field, sealed)).toThrow(OpenError);
    expect(() => keys.open(v.tenantId, "hub_subscription.url", sealed)).toThrow(OpenError);
    expect(() => keys.open("", v.field, sealed)).toThrow(OpenError);
    for (const at of [0, 3, sealed.length - 1, 2 + v.keyId.length + 1]) {
      const bad = Buffer.from(sealed);
      bad[at] = bad[at]! ^ 1;
      expect(() => keys.open(v.tenantId, v.field, bad), `byte ${at}`).toThrow(OpenError);
    }
    expect(() => keys.open(v.tenantId, v.field, sealed.subarray(0, 20))).toThrow(OpenError);
    expect(() => keys.open(v.tenantId, v.field, Buffer.from("plain text secret"))).toThrow(OpenError);
  });

  test("seal uses the current key and a fresh nonce; a removed key no longer opens its values", () => {
    const a = keys.seal("t1", "f", Buffer.from("x"));
    const b = keys.seal("t1", "f", Buffer.from("x"));
    expect(envelopeKeyId(a)).toBe(doc.currentKeyId);
    expect(a.equals(b)).toBe(false);
    expect(keys.open("t1", "f", b).toString()).toBe("x");
    const rotated = doc.vectors.find((v) => v.keyId !== doc.currentKeyId)!;
    const onlyNew = new FieldKeys(doc.currentKeyId, new Map([[doc.currentKeyId, keyBytes(doc.currentKeyId)]]));
    expect(() => onlyNew.open(rotated.tenantId, rotated.field, Buffer.from(rotated.sealed, "base64"))).toThrow(OpenError);
  });

  test("fromEnv: unset is null, the mounted file wins, malformed configuration fails", () => {
    expect(FieldKeys.fromEnv({})).toBeNull();
    const dir = mkdtempSync(join(tmpdir(), "hub-keys-"));
    const file = join(dir, "keys");
    writeFileSync(file, doc.keys.replace(",", "\n") + "\n");
    const fromFile = FieldKeys.fromEnv({ [ENV_KEYS]: "zz=" + Buffer.alloc(32).toString("base64"), [ENV_KEYS_FILE]: file, [ENV_CURRENT_ID]: "k2024" });
    expect(fromFile?.currentId).toBe("k2024");
    const k32 = Buffer.alloc(32, 7).toString("base64");
    for (const [name, env] of Object.entries({
      "bad base64": { [ENV_KEYS]: "k=!!", [ENV_CURRENT_ID]: "k" },
      "short key": { [ENV_KEYS]: "k=" + Buffer.alloc(16).toString("base64"), [ENV_CURRENT_ID]: "k" },
      duplicate: { [ENV_KEYS]: `k=${k32},k=${k32}`, [ENV_CURRENT_ID]: "k" },
      "no current": { [ENV_KEYS]: `k=${k32}` },
      "unknown current": { [ENV_KEYS]: `k=${k32}`, [ENV_CURRENT_ID]: "other" },
      "missing file": { [ENV_KEYS_FILE]: join(dir, "nope"), [ENV_CURRENT_ID]: "k" },
    })) {
      expect(() => FieldKeys.fromEnv(env), name).toThrow();
    }
  });
});

function keyBytes(id: string): Buffer {
  const entry = doc.keys.split(",").find((e) => e.startsWith(`${id}=`)) as string;
  return Buffer.from(entry.slice(id.length + 1), "base64");
}
