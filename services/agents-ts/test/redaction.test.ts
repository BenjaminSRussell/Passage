import { describe, expect, it } from "vitest";
import { existsSync, readFileSync } from "node:fs";
import { assertNoPii, maskValue, Pseudonymizer, scanPii, SENSITIVE_DUTY } from "../src/domain/redaction.js";

const TTL = 24 * 3_600_000;

describe("scanPii", () => {
  it("finds emails, IBANs and US SSNs", () => {
    const found = scanPii("mail ap@acme.example, iban DE89 3704 0044 0532 0130 00, ssn 123-45-6789");
    expect(found.map((f) => f.category).sort()).toEqual(["EMAIL", "IBAN", "SSN"]);
  });

  it("assertNoPii raises AGT-0210 on raw PII and passes pseudonymized text", () => {
    expect(() => assertNoPii("remit to DE89370400440532013000")).toThrow("AGT-0210");
    expect(() => assertNoPii("remit to [IBAN-1]")).not.toThrow();
  });

  it("scans a 100 KB run of word characters in linear time (redaction budget 9.47.19)", () => {
    const started = performance.now();
    expect(scanPii("x".repeat(100 * 1024))).toEqual([]);
    expect(performance.now() - started).toBeLessThan(250);
  });

  it("scans 100 KB runs of digits and labelled words in linear time", () => {
    const started = performance.now();
    for (const text of ["1".repeat(100 * 1024), "1 ".repeat(50 * 1024), "account ".repeat(12_000), "salary".repeat(16_000)]) expect(scanPii(text)).toEqual([]);
    expect(performance.now() - started).toBeLessThan(1000);
  });

  it("finds nothing in ordinary invoice text", () => {
    expect(scanPii("Invoice INV-2026-0042 for 1,200.50 USD, net 30")).toEqual([]);
  });
});

describe("Pseudonymizer (BR-47.7)", () => {
  it("replaces a value with the same pseudonym within the TTL and restores it", () => {
    let t = 0;
    const p = new Pseudonymizer("tenant-a", TTL, () => t);
    const red = p.redact("pay ap@acme.example then ap@acme.example");
    expect(red).toBe("pay [EMAIL-1] then [EMAIL-1]");
    expect(p.restore(red)).toBe("pay ap@acme.example then ap@acme.example");
  });

  it("gives a card, a bank account and a salary stable pseudonyms and restores them", () => {
    const p = new Pseudonymizer("tenant-a", TTL, () => 0);
    const red = p.redact('card 4111 1111 1111 1111 again 4111111111111111 {"bankAccount":"00123456789","salary":"85000"}');
    expect(red).toBe('card [CARD-1] again [CARD-2] {"bankAccount":"[BANKACCT-1]","salary":"[PAY-1]"}');
    expect(p.restore(red)).toContain('"salary":"85000"');
  });

  it("forgets mappings after the TTL", () => {
    let t = 0;
    const p = new Pseudonymizer("tenant-a", 1000, () => t);
    p.redact("ap@acme.example");
    t = 1000;
    expect(p.restore("[EMAIL-1]")).toBe("[EMAIL-1]");
  });

  it("generated samples never leave a PII pattern in the redacted text", () => {
    // Deterministic generator: 500 samples with emails, IBANs and SSNs embedded.
    let seed = 42;
    const rand = (n: number): number => {
      seed = (seed * 1103515245 + 12345) & 0x7fffffff;
      return seed % n;
    };
    const letters = "abcdefghijklmnopqrstuvwxyz";
    const pick = (s: string): string => s.charAt(rand(s.length));
    let leaks = 0;
    for (let i = 0; i < 500; i++) {
      const email = `${pick(letters)}${pick(letters)}${i}@${pick(letters)}${pick(letters)}.example`;
      const iban = `DE${10 + rand(89)}${String(370400440532013000n + BigInt(i)).padStart(18, "0")}`;
      const ssn = `${100 + rand(800)}-${10 + rand(89)}-${1000 + rand(8999)}`;
      const text = `ref ${i}: ${email} / ${iban} / ${ssn}`;
      const redacted = new Pseudonymizer("tenant-a", TTL, () => 0).redact(text);
      if (scanPii(redacted).length > 0) leaks++;
    }
    expect(leaks).toBe(0);
  });
});

describe("Pseudonymizer.restoreFor", () => {
  const redacted = () => {
    const p = new Pseudonymizer("tenant-a", TTL, () => 0);
    const text = p.redact('mail ap@acme.example ssn 123-45-6789 iban DE89370400440532013000 {"salary":"85000"}');
    return { p, text };
  };
  const none = new Set<string>();

  it("restores a sensitive value only for a viewer holding the duty its domain maps to", () => {
    const { p, text } = redacted();
    const worker = p.restoreFor(text, new Set([SENSITIVE_DUTY.worker]));
    expect(worker).toContain("123-45-6789");
    expect(worker).toContain('"salary":"****"');
    const payroll = p.restoreFor(text, new Set([SENSITIVE_DUTY.payroll]));
    expect(payroll).toContain('"salary":"85000"');
    expect(payroll).not.toContain("123-45-6789");
  });

  it("masks every sensitive value for a viewer with no duty, and leaves email unchanged", () => {
    const { p, text } = redacted();
    const out = p.restoreFor(text, none);
    expect(out).toContain("ap@acme.example");
    expect(out).toContain("****6789");
    expect(out).not.toContain("85000");
  });

  it("always masks a sensitive category that maps to no duty, even with every duty held", () => {
    const { p, text } = redacted();
    const out = p.restoreFor(text, new Set(Object.values(SENSITIVE_DUTY)));
    expect(out).not.toContain("DE89370400440532013000");
    expect(out).toContain("****3000");
  });
});

describe("SENSITIVE_DUTY", () => {
  it("names only duties seeded in policies/authz/data.json, so a rename in one place fails here", () => {
    const file = new URL("../../../policies/authz/data.json", import.meta.url);
    expect(existsSync(file), "the data.json cross-check requires the monorepo layout (policies/authz/data.json three directories above this test)").toBe(true);
    const data = JSON.parse(readFileSync(file, "utf8")) as { duties: Record<string, { flag?: boolean }> };
    for (const duty of Object.values(SENSITIVE_DUTY)) expect(data.duties[duty]?.flag).toBe(true);
  });
});

describe("maskValue", () => {
  it("fully masks pay and any amount, whatever the length", () => {
    expect(maskValue("85000", "PAY")).toBe("****");
    expect(maskValue("1234567890", "PAY")).toBe("****");
    expect(maskValue("1234567890", "AMOUNT")).toBe("****");
  });

  it("fully masks an identifier with fewer than eight significant characters", () => {
    expect(maskValue("1234567", "BANKACCT")).toBe("****");
    expect(maskValue("12345", "TAXID")).toBe("****");
    expect(maskValue("12-34-567", "TAXID")).toBe("****");
    expect(maskValue("", "SSN")).toBe("****");
  });

  it("keeps the last four of an identifier with at least eight significant characters", () => {
    expect(maskValue("4111111111111111", "CARD")).toBe("****1111");
    expect(maskValue("123-45-6789", "SSN")).toBe("****6789");
    expect(maskValue("DE89370400440532013000", "IBAN")).toBe("****3000");
  });
});
