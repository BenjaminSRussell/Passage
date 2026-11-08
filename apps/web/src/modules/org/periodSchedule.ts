import { csvCell } from "./hierarchyTree";
import type { FiscalCalendar, FiscalPeriod } from "./api/structure";

// The fiscal period schedule of one calendar year as a CSV document (client side, like the org chart export): one row
// per period with the calendar named on every row, so the file can be filtered or merged in a spreadsheet.
const header = ["Calendar", "Pattern", "Fiscal year", "Period", "Name", "Type", "Start", "End", "53rd week"];

export function periodScheduleCsv(calendar: Pick<FiscalCalendar, "code" | "pattern">, periods: readonly FiscalPeriod[]): string {
  const rows = periods.map((period) => [
    calendar.code,
    calendar.pattern,
    String(period.fiscalYear),
    String(period.periodNo),
    period.name,
    period.type,
    period.startDate,
    period.endDate,
    period.week53 ? "Yes" : "No",
  ]);
  return [header, ...rows].map((row) => row.map(csvCell).join(",")).join("\r\n");
}

export const periodScheduleFilename = (calendar: Pick<FiscalCalendar, "code">, year: number) =>
  `fiscal-periods-${calendar.code.replace(/[^A-Za-z0-9_-]/g, "_")}-${year}.csv`;

// downloadCsv saves a CSV with a UTF-8 byte order mark, so spreadsheet tools read accented names correctly.
export function downloadCsv(filename: string, csv: string): void {
  const url = URL.createObjectURL(new Blob(["\ufeff", csv], { type: "text/csv;charset=utf-8" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
