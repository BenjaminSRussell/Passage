import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listCreditProfiles, type CreditProfile } from "./api/client";
import { dash, money, sentence, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<CreditProfile>[] = [
  { key: "customerId", header: "Customer", mono: true },
  { key: "legalEntityId", header: "Legal entity", mono: true, render: (row) => shortId(row.legalEntityId) },
  { key: "limit", header: "Credit limit", align: "end", render: (row) => money(row.limit, row.currency) },
  { key: "limitType", header: "Limit type", render: (row) => sentence(row.limitType) },
  { key: "openAr", header: "Open AR", align: "end", render: (row) => money(row.exposure.openAr ?? "0", row.currency) },
  { key: "mandatoryCheck", header: "Mandatory", render: (row) => (row.mandatoryCheck ? <Badge tone="info">Mandatory</Badge> : dash) },
  { key: "reviewDate", header: "Review date", mono: true, render: (row) => row.reviewDate ?? dash },
];

/** Credit profiles per customer and legal entity (PRD §5.27.14 "Credit exposure by customer"). */
export function CreditProfilesPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState("");
  const [legalEntityId, setLegalEntityId] = useState("");
  const invalid = draft.trim() !== "" && !UUID.test(draft.trim());
  const state = useLoad((signal) => listCreditProfiles(legalEntityId, signal), legalEntityId);

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
          if (!invalid) setLegalEntityId(draft.trim());
        }}
      >
        <FormField id="profiles-le" label="Legal entity" hint="Leave empty for every legal entity you can see." error={invalid ? "Enter a legal entity UUID." : undefined} optional>
          {(control) => <Input {...control} className={styles.mono} value={draft} onChange={(e) => setDraft(e.target.value)} invalid={invalid} />}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load credit profiles" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Credit profiles"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => `${row.legalEntityId}/${row.customerId}`}
          loading={state.loading}
          empty={{ title: "No credit profiles yet", description: "Without a profile, orders confirm without a credit limit unless the check is mandatory." }}
          onRowOpen={(row) => navigate(`/sales/credit-profiles/${row.legalEntityId}/${row.customerId}`)}
        />
      )}
    </div>
  );
}
