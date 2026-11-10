import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, DataTable, FormField, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getParty, type Party } from "./api/client";
import { isUuid, partyTypeLabel } from "./format";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<Party>[] = [
  { key: "partyNumber", header: "Number", mono: true },
  { key: "name", header: "Name" },
  { key: "partyType", header: "Type", render: (row) => partyTypeLabel[row.partyType] ?? row.partyType },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

function Result({ id }: { id: string }) {
  const navigate = useNavigate();
  const state = useLoad((signal) => getParty(id, signal), id);

  if (state.error && !state.data) {
    return <ProblemAlert problem={state.error} title="Could not find that party" retry={state.reload} />;
  }

  const rows = state.data ? [state.data] : [];
  return (
    <DataTable
      label="Matching party"
      columns={columns}
      rows={rows}
      getRowKey={(row) => row.id}
      loading={state.loading}
      empty={{ title: "No party found", description: "Choose another party and try again." }}
      onRowOpen={(row) => navigate(`/party/${row.id}`)}
    />
  );
}

export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const id = params.get("id") ?? "";
  const [draft, setDraft] = useState(id);
  const [error, setError] = useState("");

  function search(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!isUuid(value)) {
      setError("Choose a party.");
      return;
    }
    setError("");
    setParams({ id: value });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Parties"
        description="Find an organization or person by name or number, then open it to see vendor roles."
      />
      <form className={styles.toolbar} onSubmit={search} role="search" aria-label="Find a party">
        <FormField id="party-search" label="Party" hint="Search by name or number, or paste its id." error={error || undefined}>
          {(control) => (
            <EntityLookup entity="party"
              {...control}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              invalid={Boolean(error)}
            />
          )}
        </FormField>
        <Button type="submit">Find party</Button>
      </form>
      {id && !isUuid(id) ? <Alert tone="warning">The id in the address is not a UUID.</Alert> : null}
      {isUuid(id) ? <Result id={id} /> : null}
      {!id ? (
        <p className={styles.note}>
          Choose a party to load its record.
        </p>
      ) : null}
    </div>
  );
}
