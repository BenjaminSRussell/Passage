import { describe, expect, it } from "vitest";
import { assertNoPii, Pseudonymizer, scanPii } from "../src/domain/redaction.js";

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
