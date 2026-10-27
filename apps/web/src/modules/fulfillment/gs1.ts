// GS1-128 scan parser for the mobile pick flow (FR-28.11, AC-28.9).
// Supports the bracketed human-readable form "(01)...(10)LOT(37)5" and the raw
// form with FNC1 (GS, 0x1D) terminating variable-length fields. Application
// identifiers: 00 SSCC, 01 GTIN, 10 batch, 17 expiry, 21 serial, 30 and 37 count.

export const FNC1 = "\u001d";

export interface Gs1Scan {
  gtin?: string;
  sscc?: string;
  batch?: string;
  serial?: string;
  /** Expiry as YYMMDD, the GS1 encoding; not converted to a date. */
  expiry?: string;
  /** Quantity from AI 37 (count of trade items) or AI 30 (variable count). */
  qty?: number;
}

/** Service catalog code for an unparseable scan (FUL-0017). */
export const GS1_PARSE_CODE = "FUL-0017";

export class Gs1ParseError extends Error {
  readonly code = GS1_PARSE_CODE;
  readonly raw: string;

  constructor(raw: string, reason: string) {
    super(`GS1-128 scan could not be parsed: ${reason}`);
    this.name = "Gs1ParseError";
    this.raw = raw;
  }
}

type Spec = { fixed: number } | { max: number };

const APPLICATION_IDENTIFIERS: Record<string, { field: keyof Gs1Scan; spec: Spec }> = {
  "00": { field: "sscc", spec: { fixed: 18 } },
  "01": { field: "gtin", spec: { fixed: 14 } },
  "10": { field: "batch", spec: { max: 20 } },
  "17": { field: "expiry", spec: { fixed: 6 } },
  "21": { field: "serial", spec: { max: 20 } },
  "30": { field: "qty", spec: { max: 8 } },
  "37": { field: "qty", spec: { max: 8 } },
};

/** GS1 mod-10 check digit for body (weights 3 and 1 from the rightmost digit). */
export function gs1CheckDigit(body: string): number {
  let sum = 0;
  for (let i = 0; i < body.length; i++) {
    const d = body.charCodeAt(body.length - 1 - i) - 48;
    sum += i % 2 === 0 ? 3 * d : d;
  }
  return (10 - (sum % 10)) % 10;
}

/** True when s is all digits and its last digit is the GS1 check digit of the rest. */
export function hasValidCheckDigit(s: string): boolean {
  if (!/^[0-9]{2,}$/.test(s)) {
    return false;
  }
  return gs1CheckDigit(s.slice(0, -1)) === Number(s.slice(-1));
}

/** Parses a GS1-128 scan into its fields. Throws Gs1ParseError (FUL-0017). */
export function parseGs1(raw: string): Gs1Scan {
  const pairs = raw.includes("(") ? fromBracketed(raw) : fromRaw(raw);
  const out: Gs1Scan = {};
  for (const [ai, value] of pairs) {
    const def = APPLICATION_IDENTIFIERS[ai];
    if (!def) {
      throw new Gs1ParseError(raw, `unsupported application identifier ${ai}`);
    }
    if ("fixed" in def.spec && value.length !== def.spec.fixed) {
      throw new Gs1ParseError(raw, `AI ${ai} must be ${def.spec.fixed} characters`);
    }
    if ("max" in def.spec && (value.length === 0 || value.length > def.spec.max)) {
      throw new Gs1ParseError(raw, `AI ${ai} must be 1 to ${def.spec.max} characters`);
    }
    if (ai === "01" && !hasValidCheckDigit(value)) {
      throw new Gs1ParseError(raw, "GTIN check digit is wrong");
    }
    if (ai === "00" && !hasValidCheckDigit(value)) {
      throw new Gs1ParseError(raw, "SSCC check digit is wrong");
    }
    if (def.field === "qty") {
      if (!/^[0-9]+$/.test(value)) {
        throw new Gs1ParseError(raw, `AI ${ai} must be numeric`);
      }
      out.qty = Number(value);
    } else if (def.field === "expiry") {
      if (!/^[0-9]{6}$/.test(value)) {
        throw new Gs1ParseError(raw, "expiry must be YYMMDD");
      }
      out.expiry = value;
    } else {
      (out as Record<string, unknown>)[def.field] = value;
    }
  }
  if (Object.keys(out).length === 0) {
    throw new Gs1ParseError(raw, "no application identifiers found");
  }
  return out;
}

function fromBracketed(raw: string): [string, string][] {
  const pairs: [string, string][] = [];
  const re = /\((\d{2})\)([^(]*)/g;
  let m: RegExpExecArray | null;
  let consumed = 0;
  while ((m = re.exec(raw)) !== null) {
    if (m.index !== consumed) {
      throw new Gs1ParseError(raw, "unexpected text between application identifiers");
    }
    consumed = re.lastIndex;
    pairs.push([m[1], m[2]]);
  }
  if (consumed !== raw.length || pairs.length === 0) {
    throw new Gs1ParseError(raw, "bracketed form must be (AI)value pairs");
  }
  return pairs;
}

function fromRaw(raw: string): [string, string][] {
  const pairs: [string, string][] = [];
  let i = 0;
  while (i < raw.length) {
    const ai = raw.slice(i, i + 2);
    const def = APPLICATION_IDENTIFIERS[ai];
    if (!def) {
      throw new Gs1ParseError(raw, `unsupported application identifier ${ai || "(none)"}`);
    }
    i += 2;
    if ("fixed" in def.spec) {
      pairs.push([ai, raw.slice(i, i + def.spec.fixed)]);
      i += def.spec.fixed;
    } else {
      const end = raw.indexOf(FNC1, i);
      const stop = end === -1 ? raw.length : end;
      pairs.push([ai, raw.slice(i, stop)]);
      i = stop + (end === -1 ? 0 : 1);
    }
  }
  return pairs;
}
