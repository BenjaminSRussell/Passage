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
      empty={{ title: "No party with that id", description: "Check the id and try again." }}
      onRowOpen={(row) => navigate(`/party/${row.id}`)}
    />
  );
}

/** Look up a party by id (GET /v1/party/parties/{id}); name search is a later slice. */
export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const id = params.get("id") ?? "";
  const [draft, setDraft] = useState(id);
  const [error, setError] = useState("");

  function search(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (!isUuid(value)) {
      setError("Enter the party id (a UUID).");
      return;
    }
    setError("");
    setParams({ id: value });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Parties"
        description="Look up an organization or person by id, then open it to see vendor roles. Name search is not on this API yet."
      />
      <form className={styles.toolbar} onSubmit={search} role="search" aria-label="Find a party">
        <FormField id="party-search" label="Party" hint="The party's UUID." error={error || undefined}>
          {(control) => (
            <EntityLookup entity="party"
              {...control}
              value={draft}
              placeholder="00000000-0000-4000-8000-000000000000"
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
          Enter a party id to load one record. The service has no list or name-search route yet, so this page looks up by id only.
        </p>
      ) : null}
    </div>
  );
}
