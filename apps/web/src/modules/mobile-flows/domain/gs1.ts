// GS1 application identifier parser (BR-32.1). Pure: no I/O, no React.
// Shared by the flow designer's virtual scanner and the warehouse-mobile app.

export type ScanClass = "LP" | "GTIN" | "LOCATION" | "ANY";

export const GS = "\u001d";

/** Catalog codes raised by the parser (MOB-0102..0104; MOB-0103 also covers malformed AI data). */
export const ErrorCode = {
  UnknownAI: "MOB-0103",
  CheckDigit: "MOB-0102",
  InvalidDate: "MOB-0104",
  Malformed: "MOB-0103",
} as const;

export interface GS1Error {
  code: string;
  ai?: string;
  position: number;
  message: string;
}

export interface GS1Parse {
  symbology?: string;
  /** AI (e.g. "01", "3102") to its value. Dates are normalised to YYYY-MM-DD; weights to decimal strings. */
  fields: Record<string, string>;
  /** AIs in the order they were read. Object key order is not reliable for numeric-looking keys. */
  order: string[];
  errors: GS1Error[];
  ok: boolean;
}

export interface ParseOptions {
  /** Character accepted for FNC1 in addition to GS (0x1D). Defaults to GS only. */
  fnc1Substitute?: string;
  /** Reference year for the expiry century window. Defaults to the current year. */
  now?: Date;
}

type AIKind = "fixedNumeric" | "fixedDate" | "variable";

interface AIDef {
  ai: string;
  kind: AIKind;
  length: number; // fixed length, or maximum length for variable
  charset: "numeric" | "set82";
}

// Supported AI table. 310n (net weight) is handled separately because its AI is four digits.
const AI_TABLE: AIDef[] = [
  { ai: "00", kind: "fixedNumeric", length: 18, charset: "numeric" },
  { ai: "01", kind: "fixedNumeric", length: 14, charset: "numeric" },
  { ai: "10", kind: "variable", length: 20, charset: "set82" },
  { ai: "11", kind: "fixedDate", length: 6, charset: "numeric" },
  { ai: "17", kind: "fixedDate", length: 6, charset: "numeric" },
  { ai: "21", kind: "variable", length: 20, charset: "set82" },
  { ai: "37", kind: "variable", length: 8, charset: "numeric" },
  { ai: "400", kind: "variable", length: 30, charset: "set82" },
  { ai: "410", kind: "fixedNumeric", length: 13, charset: "numeric" },
  { ai: "412", kind: "fixedNumeric", length: 13, charset: "numeric" },
  // AI 420: ship-to postal code, printed on the GS1 carton label (§6.32.15).
  { ai: "420", kind: "variable", length: 20, charset: "set82" },
  // AI 91 to 99: company-internal information, variable up to 90 characters.
  ...["91", "92", "93", "94", "95", "96", "97", "98", "99"].map((ai): AIDef => ({ ai, kind: "variable", length: 90, charset: "set82" })),
];

/** Symbology identifiers that carry GS1 element strings (BR-32.1): GS1-128, GS1 DataMatrix, GS1 QR. */
export const GS1_SYMBOLOGIES = ["C1", "d2", "Q3"] as const;

const SET82_SPECIALS = "!\"%&'()*+,-./:;<=>?_";

function isDigit(c: string): boolean {
  return c >= "0" && c <= "9";
}

/** GS1 AI encodable character set 82. */
export function inSet82(s: string): boolean {
  for (const c of s) {
    const ok = isDigit(c) || (c >= "A" && c <= "Z") || (c >= "a" && c <= "z") || SET82_SPECIALS.includes(c);
    if (!ok) return false;
  }
  return true;
}

/** GS1 mod-10 check digit verification (weights 3 and 1 from the right, excluding the check digit). */
export function mod10Valid(digits: string): boolean {
  if (digits.length < 2 || ![...digits].every(isDigit)) return false;
  let sum = 0;
  const payload = digits.slice(0, -1);
  for (let i = 0; i < payload.length; i++) {
    const d = payload.charCodeAt(payload.length - 1 - i) - 48;
    sum += d * (i % 2 === 0 ? 3 : 1);
  }
  const check = (10 - (sum % 10)) % 10;
  return check === digits.charCodeAt(digits.length - 1) - 48;
}

/** Pads a GTIN-8/12/13 to GTIN-14 for lookup (BR-32.1). */
export function toGtin14(gtin: string): string {
  return gtin.padStart(14, "0");
}

function daysInMonth(year: number, month: number): number {
  return new Date(Date.UTC(year, month, 0)).getUTCDate();
}

/** Expands YY into the year within -49/+50 of the reference year (GS1 century rule). */
export function expandYear(yy: number, refYear: number): number {
  const start = refYear - 49;
  const offset = (((yy - (start % 100)) % 100) + 100) % 100;
  return start + offset;
}

/** Converts YYMMDD to YYYY-MM-DD; DD=00 means last day of month (BR-32.10). */
export function parseDate(yymmdd: string, refYear: number): string | null {
  const yy = Number(yymmdd.slice(0, 2));
  const mm = Number(yymmdd.slice(2, 4));
  let dd = Number(yymmdd.slice(4, 6));
  if (mm < 1 || mm > 12) return null;
  const year = expandYear(yy, refYear);
  if (dd === 0) dd = daysInMonth(year, mm);
  if (dd < 1 || dd > daysInMonth(year, mm)) return null;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${year}-${pad(mm)}-${pad(dd)}`;
}

/** Applies the implied decimal point of AI 310n without floating point. */
export function implyDecimals(digits: string, decimals: number): string {
  if (decimals === 0) return digits.replace(/^0+(?=\d)/, "");
  const intPart = digits.slice(0, digits.length - decimals).replace(/^0+(?=\d)/, "") || "0";
  return `${intPart}.${digits.slice(digits.length - decimals)}`;
}

const SYMBOLOGY_ID = /^\][A-Za-z][0-9A-Za-z]/;

function parseSymbology(raw: string): { symbology?: string; body: string; offset: number } {
  if (SYMBOLOGY_ID.test(raw)) {
    return { symbology: raw.slice(1, 3), body: raw.slice(3), offset: 3 };
  }
  return { body: raw, offset: 0 };
}

/** Matches the AI at the position, longest prefix first. */
function matchAI(s: string, pos: number): { ai: string; def?: AIDef; weight: boolean } | null {
  const rest = s.slice(pos);
  // AI 310n: "310" then the implied-decimal count n (0 to 5).
  if (/^310[0-5]/.test(rest)) {
    return { ai: rest.slice(0, 4), weight: true };
  }
  for (const len of [3, 2]) {
    const ai = rest.slice(0, len);
    const def = AI_TABLE.find((d) => d.ai === ai);
    if (def) return { ai, def, weight: false };
  }
  return null;
}

/**
 * Parses a GS1 element string. Symbology identifiers ]C1, ]d2, ]Q3 are accepted.
 * Check-digit, date and charset failures are collected rather than thrown, so the
 * device can show every problem at once.
 */
export function parseGS1(raw: string, opts: ParseOptions = {}): GS1Parse {
  const refYear = (opts.now ?? new Date()).getUTCFullYear();
  const sub = opts.fnc1Substitute ?? GS;
  const sym = parseSymbology(raw);
  const s = sym.body.split(sub).join(GS);
  const result: GS1Parse = { symbology: sym.symbology, fields: {}, order: [], errors: [], ok: true };
  const fail = (e: GS1Error) => {
    result.errors.push(e);
    result.ok = false;
  };
  const setField = (ai: string, value: string) => {
    result.fields[ai] = value;
    result.order.push(ai);
  };
  let pos = 0;

  while (pos < s.length) {
    if (s[pos] === GS) {
      pos++;
      continue;
    }
    const at = pos + sym.offset;
    const m = matchAI(s, pos);
    if (!m) {
      fail({ code: ErrorCode.UnknownAI, position: at, message: `Unknown GS1 application identifier at position ${at}.` });
      break;
    }
    pos += m.ai.length;

    if (m.weight) {
      const data = s.slice(pos, pos + 6);
      pos += 6;
      if (!/^\d{6}$/.test(data)) {
        fail({ code: ErrorCode.Malformed, ai: m.ai, position: at, message: `AI ${m.ai} needs 6 digits.` });
        break;
      }
      setField(m.ai, implyDecimals(data, Number(m.ai[3])));
      continue;
    }

    const def = m.def as AIDef;
    if (def.kind === "variable") {
      const end = s.indexOf(GS, pos);
      const value = end < 0 ? s.slice(pos) : s.slice(pos, end);
      pos += value.length;
      if (value.length === 0 || value.length > def.length) {
        fail({ code: ErrorCode.Malformed, ai: def.ai, position: at, message: `AI ${def.ai} must be 1 to ${def.length} characters.` });
        continue;
      }
      if (def.charset === "numeric" ? !/^\d+$/.test(value) : !inSet82(value)) {
        fail({ code: ErrorCode.Malformed, ai: def.ai, position: at, message: `AI ${def.ai} has characters outside its charset.` });
        continue;
      }
      setField(def.ai, value);
      continue;
    }

    const value = s.slice(pos, pos + def.length);
    pos += def.length;
    if (value.length !== def.length || !/^\d+$/.test(value)) {
      fail({ code: ErrorCode.Malformed, ai: def.ai, position: at, message: `AI ${def.ai} needs ${def.length} digits.` });
      break;
    }
    if (def.kind === "fixedDate") {
      const iso = parseDate(value, refYear);
      if (iso === null) {
        fail({ code: ErrorCode.InvalidDate, ai: def.ai, position: at, message: `Invalid date ${value} for AI ${def.ai}.` });
        continue;
      }
      setField(def.ai, iso);
      continue;
    }
    if ((def.ai === "00" || def.ai === "01") && !mod10Valid(value)) {
      fail({ code: ErrorCode.CheckDigit, ai: def.ai, position: at, message: `Check digit invalid for AI ${def.ai}.` });
      continue;
    }
    setField(def.ai, value);
  }
  return result;
}

/** True when the string begins with a known AI, which is how an unprefixed GS1 string is recognised. */
export function startsWithAI(raw: string): boolean {
  return matchAI(raw, 0) !== null;
}

export interface Classified {
  kind: "gs1" | "plain";
  value: string;
  gs1?: GS1Parse;
}

/** Decides whether a raw scan is GS1 or plain (BR-32.1 last paragraph, gs1_require_symbology_id). */
export function classifyScan(
  raw: string,
  opts: ParseOptions & { requireSymbologyId?: boolean; expectGs1?: boolean } = {},
): Classified {
  if (SYMBOLOGY_ID.test(raw)) {
    const symbology = raw.slice(1, 3);
    if ((GS1_SYMBOLOGIES as readonly string[]).includes(symbology)) return { kind: "gs1", value: raw, gs1: parseGS1(raw, opts) };
    // Any other symbology (]C0 plain Code 128, ]E0 EAN-13, ...) is a plain value without its prefix.
    return { kind: "plain", value: raw.slice(3) };
  }
  if (opts.requireSymbologyId) return { kind: "plain", value: raw };
  if (opts.expectGs1 && startsWithAI(raw)) return { kind: "gs1", value: raw, gs1: parseGS1(raw, opts) };
  return { kind: "plain", value: raw };
}

const LP_FORMAT = /^LP[0-9A-Z-]{3,20}$/;
const LOCATION_FORMAT = /^[A-Z0-9]{1,4}(-[A-Z0-9]{1,4}){1,3}$/;

/** Scan class names as the MOB-0101 message words them. */
export const CLASS_LABEL: Record<ScanClass, string> = { LP: "LP", GTIN: "GTIN", LOCATION: "location", ANY: "any barcode" };

/** What the worker scanned, in the words of the MOB-0101 message. */
export function describeScan(c: Classified): string {
  if (c.gs1) {
    if (c.gs1.fields["00"]) return "LP";
    if (c.gs1.fields["01"]) return "GTIN";
    return `AI ${c.gs1.order[0] ?? "?"}`;
  }
  if (LP_FORMAT.test(c.value)) return "LP";
  if (LOCATION_FORMAT.test(c.value)) return "location";
  return c.value;
}

/** BR-32.2 scan routing: returns null when the scan satisfies the expected class, else the MOB-0101 text. */
export function matchScanClass(expected: ScanClass, c: Classified): string | null {
  if (expected === "ANY") return null;
  const fields = c.gs1?.fields ?? {};
  if (expected === "LP") {
    if (fields["00"] || (c.kind === "plain" && LP_FORMAT.test(c.value))) return null;
  } else if (expected === "GTIN") {
    if (fields["01"] || (c.kind === "plain" && /^\d{8}$|^\d{12,14}$/.test(c.value) && mod10Valid(c.value))) return null;
  } else if (expected === "LOCATION") {
    if (c.kind === "plain" && LOCATION_FORMAT.test(c.value)) return null;
  }
  return `Expected ${CLASS_LABEL[expected]}, scanned ${describeScan(c)}.`;
}

export interface ScanVerdict {
  code: string;
  message: string;
}

/**
 * The device's answer to one scan: null when the step may advance, otherwise the catalog
 * code and text. A GS1 parse failure (check digit, date, unknown AI) is reported before a
 * class mismatch, because the label itself is wrong.
 */
export function checkScan(expected: ScanClass, c: Classified): ScanVerdict | null {
  const parseError = c.gs1?.errors[0];
  if (parseError) return { code: parseError.code, message: parseError.message };
  const mismatch = matchScanClass(expected, c);
  return mismatch ? { code: "MOB-0101", message: mismatch } : null;
}
