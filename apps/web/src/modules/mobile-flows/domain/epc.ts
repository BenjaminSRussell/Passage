// RFID EPC decoding (FR-32.9). Pure: no I/O, no React. Reads the GS1 EPC binary encodings of GS1
// TDS a device scans: SGTIN-96 and SGTIN-198 (a trade item with serial; the 198-bit one carries an
// alphanumeric serial of 7-bit characters) and SSCC-96 (a logistic unit). An EPC becomes
// the same GS1 element string a barcode scan carries, so the flow runner treats a tag read and a
// GS1-128 scan alike.

export type EpcScheme = "SGTIN-96" | "SGTIN-198" | "SSCC-96";

export interface Epc {
  scheme: EpcScheme;
  /** 3-bit filter value (for example 1 = point of sale item, 2 = full case, 6 = unit load). */
  filter: number;
  /** 3-bit partition: where the company prefix ends. */
  partition: number;
  companyPrefix: string;
  /** SGTIN-96: the 14-digit GTIN with its check digit. */
  gtin?: string;
  /** SGTIN-96: the serial number, decimal (up to 38 bits); SGTIN-198: up to 20 characters. */
  serial?: string;
  /** SSCC-96: the 18-digit SSCC with its check digit. */
  sscc?: string;
}

export class EpcError extends Error {
  readonly code = "MOB-0118";
  constructor(message: string) {
    super(message);
    this.name = "EpcError";
  }
}

const HEADER_SGTIN96 = 0x30;
const HEADER_SSCC96 = 0x31;
const HEADER_SGTIN198 = 0x36;
/** Characters an SGTIN-198 serial can hold: the GS1 AI encodable character set 82. */
const SERIAL_CHARS = /^[!"%&'()*+,\-./0-9:;<=>?A-Z_a-z]$/;

/** Partition tables of GS1 TDS: company prefix and the item or serial reference, in bits and digits. */
interface Partition {
  cpBits: number;
  cpDigits: number;
  refBits: number;
  refDigits: number;
}
const SGTIN_PARTITIONS: readonly Partition[] = [
  { cpBits: 40, cpDigits: 12, refBits: 4, refDigits: 1 },
  { cpBits: 37, cpDigits: 11, refBits: 7, refDigits: 2 },
  { cpBits: 34, cpDigits: 10, refBits: 10, refDigits: 3 },
  { cpBits: 30, cpDigits: 9, refBits: 14, refDigits: 4 },
  { cpBits: 27, cpDigits: 8, refBits: 17, refDigits: 5 },
  { cpBits: 24, cpDigits: 7, refBits: 20, refDigits: 6 },
  { cpBits: 20, cpDigits: 6, refBits: 24, refDigits: 7 },
];
const SSCC_PARTITIONS: readonly Partition[] = [
  { cpBits: 40, cpDigits: 12, refBits: 18, refDigits: 5 },
  { cpBits: 37, cpDigits: 11, refBits: 21, refDigits: 6 },
  { cpBits: 34, cpDigits: 10, refBits: 24, refDigits: 7 },
  { cpBits: 30, cpDigits: 9, refBits: 28, refDigits: 8 },
  { cpBits: 27, cpDigits: 8, refBits: 31, refDigits: 9 },
  { cpBits: 24, cpDigits: 7, refBits: 34, refDigits: 10 },
  { cpBits: 20, cpDigits: 6, refBits: 38, refDigits: 11 },
];

/** GS1 mod-10 check digit of a digit string without its check digit. */
export function checkDigitOf(data: string): string {
  let sum = 0;
  for (let i = 0; i < data.length; i++) {
    const d = data.charCodeAt(data.length - 1 - i) - 48;
    sum += i % 2 === 0 ? d * 3 : d;
  }
  return String((10 - (sum % 10)) % 10);
}

const pad = (n: bigint, digits: number) => n.toString().padStart(digits, "0");

function toBits(hex: string): bigint {
  return BigInt(`0x${hex}`);
}

/** Reads `width` bits starting `from` bits after the most significant bit of a value `total` bits wide. */
function field(value: bigint, from: number, width: number, total = 96): bigint {
  return (value >> BigInt(total - from - width)) & ((1n << BigInt(width)) - 1n);
}

/**
 * Decodes an SGTIN-198 (50 or 52 hexadecimal characters: 198 bits, padded to a 16-bit word with zero bits): the item as in
 * SGTIN-96, then a serial of up to 20 7-bit characters ending at the first zero character.
 */
function decodeSgtin198(clean: string): Epc {
  const total = clean.length * 4;
  const bits = toBits(clean);
  if (total > 198 && field(bits, 198, total - 198, total) !== 0n) throw new EpcError("The bits after an SGTIN-198 are not zero.");
  const filter = Number(field(bits, 8, 3, total));
  const partition = Number(field(bits, 11, 3, total));
  const p = SGTIN_PARTITIONS[partition];
  if (!p) throw new EpcError(`EPC partition ${partition} is not defined.`);
  const prefix = field(bits, 14, p.cpBits, total);
  const ref = field(bits, 14 + p.cpBits, p.refBits, total);
  if (prefix >= 10n ** BigInt(p.cpDigits) || ref >= 10n ** BigInt(p.refDigits)) throw new EpcError("EPC company prefix or reference is out of range.");
  let serial = "";
  let pos = 58;
  for (let i = 0; i < 20; i++, pos += 7) {
    const c = Number(field(bits, pos, 7, total));
    if (c === 0) break;
    const ch = String.fromCharCode(c);
    if (!SERIAL_CHARS.test(ch)) throw new EpcError(`Character 0x${c.toString(16)} cannot be in an SGTIN-198 serial.`);
    serial += ch;
  }
  if (pos < 198 && field(bits, pos, 198 - pos, total) !== 0n) throw new EpcError("The SGTIN-198 serial does not end in zero bits.");
  if (serial === "") throw new EpcError("The SGTIN-198 serial is empty.");
  const companyPrefix = pad(prefix, p.cpDigits);
  const refDigits = pad(ref, p.refDigits);
  const base = refDigits.slice(0, 1) + companyPrefix + refDigits.slice(1);
  return { scheme: "SGTIN-198", filter, partition, companyPrefix, gtin: base + checkDigitOf(base), serial };
}

/** Decodes the hex of an SGTIN-96, SGTIN-198 or SSCC-96 tag. Anything else is MOB-0118. */
export function decodeEpc(hex: string): Epc {
  const clean = hex.trim().toUpperCase();
  if (/^36[0-9A-F]{48}([0-9A-F]{2})?$/.test(clean)) return decodeSgtin198(clean);
  if (!/^[0-9A-F]{24}$/.test(clean)) throw new EpcError("An EPC is 24 hexadecimal characters (96 bits), or 52 for an SGTIN-198.");
  const bits = toBits(clean);
  const header = Number(field(bits, 0, 8));
  if (header !== HEADER_SGTIN96 && header !== HEADER_SSCC96) {
    throw new EpcError(`EPC header 0x${header.toString(16)} is not SGTIN-96, SGTIN-198 or SSCC-96.`);
  }
  const filter = Number(field(bits, 8, 3));
  const partition = Number(field(bits, 11, 3));
  const table = header === HEADER_SGTIN96 ? SGTIN_PARTITIONS : SSCC_PARTITIONS;
  const p = table[partition];
  if (!p) throw new EpcError(`EPC partition ${partition} is not defined.`);
  const prefix = field(bits, 14, p.cpBits);
  const ref = field(bits, 14 + p.cpBits, p.refBits);
  if (prefix >= 10n ** BigInt(p.cpDigits) || ref >= 10n ** BigInt(p.refDigits)) throw new EpcError("EPC company prefix or reference is out of range.");
  const companyPrefix = pad(prefix, p.cpDigits);
  const refDigits = pad(ref, p.refDigits);
  if (header === HEADER_SSCC96) {
    const base = refDigits.slice(0, 1) + companyPrefix + refDigits.slice(1);
    return { scheme: "SSCC-96", filter, partition, companyPrefix, sscc: base + checkDigitOf(base) };
  }
  const serial = field(bits, 58, 38);
  const base = refDigits.slice(0, 1) + companyPrefix + refDigits.slice(1);
  return { scheme: "SGTIN-96", filter, partition, companyPrefix, gtin: base + checkDigitOf(base), serial: serial.toString() };
}

/**
 * The GS1 element string of a tag, with the GS1-128 symbology identifier the scan rules read:
 * `]C1` + AI 01 GTIN + AI 21 serial for an SGTIN, `]C1` + AI 00 SSCC for an SSCC.
 */
export function epcToScan(hex: string): string {
  const epc = decodeEpc(hex);
  return epc.scheme === "SSCC-96" ? `]C100${epc.sscc}` : `]C101${epc.gtin}21${epc.serial}`;
}

/** Encodes an SGTIN-198 with an alphanumeric serial, padded to 52 hexadecimal characters (for the tests and for tag printing). */
export function encodeSgtin198(gtin14: string, serial: string, companyPrefixDigits: number, filter = 1): string {
  if (!/^\d{14}$/.test(gtin14) || checkDigitOf(gtin14.slice(0, 13)) !== gtin14[13]) throw new EpcError("The GTIN-14 is not valid.");
  if (serial.length < 1 || serial.length > 20 || ![...serial].every((ch) => SERIAL_CHARS.test(ch))) throw new EpcError("An SGTIN-198 serial is 1 to 20 GS1 characters.");
  const partition = SGTIN_PARTITIONS.findIndex((p) => p.cpDigits === companyPrefixDigits);
  if (partition < 0) throw new EpcError("A company prefix has 6 to 12 digits.");
  const p = SGTIN_PARTITIONS[partition];
  const prefix = BigInt(gtin14.slice(1, 1 + p.cpDigits));
  const ref = BigInt(gtin14[0] + gtin14.slice(1 + p.cpDigits, 13));
  let v = BigInt(HEADER_SGTIN198);
  for (const [value, width] of [
    [BigInt(filter), 3],
    [BigInt(partition), 3],
    [prefix, p.cpBits],
    [ref, p.refBits],
  ] as const) {
    v = (v << BigInt(width)) | value;
  }
  for (let i = 0; i < 20; i++) v = (v << 7n) | BigInt(i < serial.length ? serial.charCodeAt(i) : 0);
  v <<= 10n; // pad 198 bits to 208
  return v.toString(16).toUpperCase().padStart(52, "0");
}

/** Encodes an SGTIN-96 (for the tests and for tag printing). The GTIN must have a valid check digit. */
export function encodeSgtin96(gtin14: string, serial: bigint, companyPrefixDigits: number, filter = 1): string {
  if (!/^\d{14}$/.test(gtin14) || checkDigitOf(gtin14.slice(0, 13)) !== gtin14[13]) throw new EpcError("The GTIN-14 is not valid.");
  const partition = SGTIN_PARTITIONS.findIndex((p) => p.cpDigits === companyPrefixDigits);
  if (partition < 0) throw new EpcError("A company prefix has 6 to 12 digits.");
  const p = SGTIN_PARTITIONS[partition];
  if (serial < 0n || serial >= 1n << 38n) throw new EpcError("An SGTIN-96 serial is at most 38 bits.");
  const prefix = BigInt(gtin14.slice(1, 1 + p.cpDigits));
  const ref = BigInt(gtin14[0] + gtin14.slice(1 + p.cpDigits, 13));
  if (ref >= 1n << BigInt(p.refBits)) throw new EpcError("The item reference does not fit this partition.");
  let v = BigInt(HEADER_SGTIN96);
  for (const [value, width] of [
    [BigInt(filter), 3],
    [BigInt(partition), 3],
    [prefix, p.cpBits],
    [ref, p.refBits],
    [serial, 38],
  ] as const) {
    v = (v << BigInt(width)) | value;
  }
  return v.toString(16).toUpperCase().padStart(24, "0");
}
