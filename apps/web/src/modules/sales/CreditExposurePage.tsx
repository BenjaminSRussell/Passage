import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { UTILIZATION_STATUSES, listCreditExposure, type CreditExposureFilter, type CreditExposureRow, type UtilizationStatus } from "./api/client";
import { dash, money, sentence, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { PlainName } from "./OrderNumber";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const PERCENT_2 = /^\d+(\.\d{1,2})?$/;

const tones: Record<UtilizationStatus, BadgeTone> = { NO_LIMIT: "neutral", WITHIN: "success", OVER_LIMIT: "warning", OVER_TOLERANCE: "danger" };

const labels: Record<UtilizationStatus, string> = {
  NO_LIMIT: "No limit",
  WITHIN: "Within limit",
  OVER_LIMIT: "Over limit",
  OVER_TOLERANCE: "Over tolerance",
};

const columns: DataTableColumn<CreditExposureRow>[] = [
  { key: "customerId", header: "Customer", render: (row) => <PlainName entity="customer" id={row.customerId} /> },
  { key: "legalEntityId", header: "Legal entity", render: (r) => <EntityName entity="legalEntity" id={r.legalEntityId} /> },
  { key: "limit", header: "Credit limit", align: "end", render: (r) => money(r.limit, r.currency) },
  { key: "limitType", header: "Limit type", render: (r) => sentence(r.limitType) },
  { key: "openAr", header: "Open AR", align: "end", render: (r) => money(r.exposure.openAr ?? "0", r.currency) },
  { key: "unapplied", header: "Unapplied", align: "end", render: (r) => money(r.exposure.unapplied ?? "0", r.currency) },
  { key: "dni", header: "Delivered, not invoiced", align: "end", render: (r) => money(r.exposure.deliveredNotInvoiced ?? "0", r.currency) },
  { key: "openOrders", header: "Open orders", align: "end", render: (r) => money(r.exposure.openOrders ?? "0", r.currency) },
  {
    key: "balance",
    header: "Counted against limit",
    align: "end",
    render: (r) => (r.understated ? `${money(r.balance, r.currency)} or more` : money(r.balance, r.currency)),
  },
  { key: "utilizationPct", header: "Used", align: "end", render: (r) => (r.utilizationPct === undefined ? dash : `${r.utilizationPct}%`) },
  { key: "status", header: "Status", render: (r) => <Badge tone={tones[r.status]}>{labels[r.status]}</Badge> },
];

/**
 * Credit exposure by customer (PRD §5.27.14): each customer's exposure against the credit
 * limit, highest use first, so a credit manager sees who is near or over the limit.
 */
export function CreditExposurePage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState({ legalEntityId: "", customerId: "", status: "" as UtilizationStatus | "", minUtilizationPct: "" });
  const [filter, setFilter] = useState<CreditExposureFilter>({});
  const invalid: Record<string, string> = {};
  if (draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim())) invalid.legalEntityId = "Enter a legal entity UUID.";
  if (draft.customerId.trim() && !UUID.test(draft.customerId.trim())) invalid.customerId = "Enter a customer UUID.";
  if (draft.minUtilizationPct !== "" && !PERCENT_2.test(draft.minUtilizationPct)) invalid.minUtilizationPct = "Enter a percent such as 80 or 92.5.";
  const state = useLoad((signal) => listCreditExposure(filter, signal), JSON.stringify(filter));
  const report = state.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit exposure"
        description="Each customer's exposure against the credit limit, highest use first. The figure counted against the limit is the one the credit check starts from: open AR less unapplied cash, plus delivered goods not yet invoiced and open orders where the limit type counts them."
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (Object.keys(invalid).length > 0) return;
          setFilter({
            legalEntityId: draft.legalEntityId.trim(),
            customerId: draft.customerId.trim(),
            status: draft.status,
            minUtilizationPct: draft.minUtilizationPct,
          });
        }}
      >
        <FormField id="exposure-le" label="Legal entity" hint="Leave empty for every legal entity you can see." error={invalid.legalEntityId} optional>
          {(control) => <EntityLookup entity="legalEntity" {...control} prefill={false} placeholder="All legal entities" value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(invalid.legalEntityId)} />}
        </FormField>
        <FormField id="exposure-customer" label="Customer" error={invalid.customerId} optional>
          {(control) => <EntityLookup entity="customer" {...control} placeholder="All customers" value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(invalid.customerId)} />}
        </FormField>
        <FormField id="exposure-status" label="Status" optional>
          {(control) => (
            <Select {...control} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as UtilizationStatus | "" })}>
              <option value="">Any status</option>
              {UTILIZATION_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {labels[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="exposure-min" label="Used at least (%)" hint="Customers without a limit are left out." error={invalid.minUtilizationPct} optional>
          {(control) => <Input {...control} inputMode="decimal" value={draft.minUtilizationPct} onChange={(e) => setDraft({ ...draft, minUtilizationPct: e.target.value })} invalid={Boolean(invalid.minUtilizationPct)} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {report?.truncated ? (
        <Alert tone="warning" title="Partial ranking">
          This scope has more than 50,000 credit profiles, so only the first 50,000 were measured and some customers may be missing. Narrow it to one legal entity or customer.
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load credit exposure" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          <DataTable
            label="Credit exposure by customer"
            columns={columns}
            rows={report?.items ?? []}
            getRowKey={(r) => `${r.legalEntityId}/${r.customerId}`}
            loading={state.loading}
            empty={{ title: "No customers match", description: "Credit profiles define the limits this report measures; change the filters or add a profile." }}
            onRowOpen={(r) => navigate(`/sales/credit-profiles/${r.legalEntityId}/${r.customerId}`)}
          />
          {report && report.total > report.items.length ? (
            <p className={styles.muted}>
              Showing {report.items.length} of {report.total} customers.
            </p>
          ) : null}
        </>
      )}
    </div>
  );
}
