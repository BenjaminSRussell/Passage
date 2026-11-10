const BOM = "\uFEFF";
const FORMULA_START = /^([\t\r]|[\s'"]*[=+\-@])/;
const PLAIN_NUMBER = /^[+-]?\d+(\.\d+)?$/;
const NEEDS_QUOTES = /[",\r\n]/;

export function csvCell(value: string | number | null | undefined): string {
  const text = value === null || value === undefined ? "" : String(value);
  const safe = FORMULA_START.test(text) && !PLAIN_NUMBER.test(text) ? `'${text}` : text;
  return NEEDS_QUOTES.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
}

export function toCsv(header: string[], records: (string | number | null | undefined)[][]): string {
  const lines = [header, ...records].map((record) => record.map(csvCell).join(","));
  return `${BOM}${lines.join("\r\n")}\r\n`;
}

export function downloadCsv(filename: string, content: string): void {
  const url = URL.createObjectURL(new Blob([content], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}
