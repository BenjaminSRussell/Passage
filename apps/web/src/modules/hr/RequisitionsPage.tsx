import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { buttonClassName, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createRequisition, listRequisitions, type Requisition, type RequisitionStatus } from "./api/recruitment";
import { DATE, UUID, dayOf } from "./format";
import styles from "./pages.module.css";
import { CheckField, FormActions, ListPage, SelectField, Status, TextField, labelled, useSubmit } from "./parts";

const STATUSES: RequisitionStatus[] = ["DRAFT", "APPROVED", "OPEN", "FILLED", "CANCELED"];

const columns: DataTableColumn<Requisition>[] = [
  { key: "requisitionNumber", header: "Requisition", mono: true, render: (row) => <Link to={`/hr/requisitions/${row.id}`}>{row.requisitionNumber}</Link> },
  { key: "status", header: "Status", render: (row) => <Status value={row.status} /> },
  { key: "filled", header: "Filled", align: "end", mono: true, render: (row) => `${row.filled} of ${row.headcount}` },
  { key: "plannedStartDate", header: "Planned start", mono: true, render: (row) => dayOf(row.plannedStartDate) },
  { key: "positionId", header: "Position", mono: true, render: (row) => <Link to={`/hr/positions/${row.positionId}`}>{row.positionId.slice(0, 8)}</Link> },
];

/** Requisitions by number. A requisition moves draft, approved, open, then filled as hires are made. */
export function RequisitionsPage() {
  const [params, setParams] = useSearchParams();
  const raw = params.get("status") ?? "";
  const status = STATUSES.includes(raw as RequisitionStatus) ? raw : "";
  return (
    <ListPage<Requisition>
      title="Requisitions"
      description="Open positions to fill. Opening checks that the position has room on the planned start date."
      label="Requisitions"
      noun="requisitions"
      load={(cursor, signal) => listRequisitions({ status, cursor }, signal)}
      loadKey={`requisitions|${status}`}
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No requisitions", description: status ? "Try another status." : "Create a requisition to start recruiting for a position." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/requisitions/new">
          New requisition
        </Link>
      }
      toolbar={
        <SelectField
          id="requisition-status"
          label="Status"
          value={status}
          onChange={(value) => setParams(value ? { status: value } : {}, { replace: true })}
          options={labelled(STATUSES)}
          placeholder="All"
        />
      }
    />
  );
}

export interface RequisitionValues {
  requisitionNumber: string;
  positionId: string;
  hiringManagerWorkerId: string;
  headcount: string;
  plannedStartDate: string;
  allowOverfill: boolean;
}

export function validateRequisition(v: RequisitionValues) {
  const errors: Record<string, string> = {};
  const number = v.requisitionNumber.trim().toUpperCase();
  if (!/^[A-Z0-9][A-Z0-9-]{0,19}$/.test(number)) errors.requisitionNumber = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  if (!UUID.test(v.positionId.trim())) errors.positionId = "Enter the position's id.";
  if (!UUID.test(v.hiringManagerWorkerId.trim())) errors.hiringManagerWorkerId = "Enter the hiring manager's worker id.";
  const headcount = Number(v.headcount);
  if (!/^\d{1,3}$/.test(v.headcount.trim()) || headcount < 1 || headcount > 500) errors.headcount = "Enter a whole number from 1 to 500.";
  if (!DATE.test(v.plannedStartDate)) errors.plannedStartDate = "Choose the planned start date.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      requisitionNumber: number,
      positionId: v.positionId.trim(),
      hiringManagerWorkerId: v.hiringManagerWorkerId.trim(),
      headcount,
      plannedStartDate: v.plannedStartDate,
      allowOverfill: v.allowOverfill,
    },
  };
}

export function NewRequisitionPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RequisitionValues>({
    requisitionNumber: "",
    positionId: "",
    hiringManagerWorkerId: "",
    headcount: "1",
    plannedStartDate: "",
    allowOverfill: false,
  });
  const form = useSubmit({ "HCM-REQ-DUPLICATE": "requisitionNumber" });
  const set = (patch: Partial<RequisitionValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateRequisition(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createRequisition(input, key), (created) => navigate(`/hr/requisitions/${created.id}`));
  }

  const text = (name: Exclude<keyof RequisitionValues, "allowOverfill">, label: string, extra?: { hint?: string; type?: string; inputMode?: "numeric" }) => (
    <TextField id={`requisition-${name}`} label={label} value={values[name]} error={form.errors[name]} onChange={(v) => set({ [name]: v })} {...extra} />
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New requisition" description="A new requisition starts as a draft; approve it, then open it to take candidates." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("requisitionNumber", "Requisition number", { hint: "Unique in your tenant, for example REQ-2026-014." })}
          {text("positionId", "Position id")}
          {text("hiringManagerWorkerId", "Hiring manager worker id")}
          {text("headcount", "Headcount", { inputMode: "numeric" })}
          {text("plannedStartDate", "Planned start date", { type: "date" })}
          <CheckField id="requisition-allowOverfill" label="Allow more people than the position has room for" checked={values.allowOverfill} onChange={(allowOverfill) => set({ allowOverfill })} />
          <FormActions errors={form.errors} busy={form.busy} submit="Create requisition" cancelTo="/hr/requisitions" failure={form.failure} failureTitle="Could not create the requisition" />
        </form>
      </Card>
    </div>
  );
}
