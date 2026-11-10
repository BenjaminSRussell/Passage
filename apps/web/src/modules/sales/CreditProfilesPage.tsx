import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCreditProfiles, type CreditProfile } from "./api/client";
import { dash, money, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<CreditProfile>[] = [
  { key: "customerId", header: "Customer", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => shortId(row.legalEntityId) },
  { key: "limit", header: "Credit limit", align: "end", render: (row) => money(row.limit, row.currency) },
  { key: "limitType", header: "Limit type", render: (row) => sentence(row.limitType) },
  { key: "openAr", header: "Open AR", align: "end", render: (row) => money(row.exposure.openAr ?? "0", row.currency) },
  { key: "mandatoryCheck", header: "Mandatory", render: (row) => (row.mandatoryCheck ? <Badge tone="info">Mandatory</Badge> : dash) },
  { key: "reviewDate", header: "Review date", mono: true, render: (row) => row.reviewDate ?? dash },
];

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** Credit profiles per customer and legal entity (PRD §5.27.14 "Credit exposure by customer"). */
export function CreditProfilesPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState("");
  const [legalEntityId, setLegalEntityId] = useState("");
  const [dueDraft, setDueDraft] = useState("");
  const [reviewDueBy, setReviewDueBy] = useState("");
  const invalid = draft.trim() !== "" && !UUID.test(draft.trim());
  const dueInvalid = dueDraft !== "" && !DAY.test(dueDraft);
  const state = useLoad((signal) => listCreditProfiles(legalEntityId, signal, reviewDueBy), `${legalEntityId}|${reviewDueBy}`);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit profiles"
        description="Each customer's credit limit, tolerances and exposure per legal entity. The credit check at confirmation uses these values."
        actions={
          <Link className={buttonClassName("primary")} to="/sales/credit-profiles/new">
            New credit profile
          </Link>
        }
      />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          if (invalid || dueInvalid) return;
          setLegalEntityId(draft.trim());
          setReviewDueBy(dueDraft);
        }}
      >
        <FormField id="profiles-le" label="Legal entity" hint="Leave empty for every legal entity you can see." error={invalid ? "Enter a legal entity UUID." : undefined} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={invalid} />}
        </FormField>
        <FormField id="profiles-due" label="Review due by" hint="Only profiles whose review date is on or before this day, oldest first. Past it, the check holds orders (CR-4)." error={dueInvalid ? "Enter a date." : undefined} optional>
          {(control) => <Input {...control} type="date" value={dueDraft} onChange={(e) => setDueDraft(e.target.value)} invalid={dueInvalid} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load credit profiles" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Credit profiles"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => `${row.legalEntityId}/${row.customerId}`}
          loading={state.loading}
          empty={
            reviewDueBy
              ? { title: "No credit reviews due", description: `No profile has a review date on or before ${reviewDueBy}.` }
              : { title: "No credit profiles yet", description: "Without a profile, orders confirm without a credit limit unless the check is mandatory." }
          }
          onRowOpen={(row) => navigate(`/sales/credit-profiles/${row.legalEntityId}/${row.customerId}`)}
        />
      )}
    </div>
  );
}
