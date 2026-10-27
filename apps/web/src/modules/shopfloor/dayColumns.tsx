import { Badge, StatusPill, type DataTableColumn } from "../../shell/ui";
import type { WorkerDay } from "./api/client";
import { describeLines, FLAG_LABELS, formatHours, formatPayTotal } from "./format";
import styles from "./pages.module.css";

/** Columns shared by the worker detail and time approval tables. */
export const dayColumns: DataTableColumn<WorkerDay>[] = [
  { key: "workDate", header: "Date", mono: true },
  { key: "hours", header: "Hours", align: "end", render: (row) => formatHours(row.hours) },
  { key: "payLines", header: "Pay lines", render: (row) => describeLines(row.payLines) },
  { key: "pay", header: "Pay", align: "end", render: (row) => formatPayTotal(row.payLines) },
  {
    key: "flags",
    header: "Flags",
    render: (row) =>
      row.flags.length === 0 ? (
        "None"
      ) : (
        <span className={styles.flags}>
          {row.flags.map((flag) => (
            <Badge key={flag} tone="warning">
              {FLAG_LABELS[flag] ?? flag}
            </Badge>
          ))}
        </span>
      ),
  },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];
