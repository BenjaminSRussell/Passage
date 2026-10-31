import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { KEYED_JOURNAL_TYPES, listJournalNames, updateJournalName, type JournalName, type JournalNamePatch, type Problem } from "./api/client";
import { dash, typeLabels, UUID } from "./format";
import { ProblemAlert, RowAction } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Journal names (PRD 3.16.13): the setup a journal is created under, with its approval rule. */
export function JournalNamesPage() {
  const [legalEntityId, setLegalEntityId] = useState("");
  const [journalType, setJournalType] = useState("");
  const [active, setActive] = useState<"true" | "false" | "">("true");
  const [attempt, setAttempt] = useState(0);
  const entityValid = legalEntityId.trim() === "" || UUID.test(legalEntityId.trim());
  const load = useLoad(
    (signal) => listJournalNames({ legalEntityId: entityValid ? legalEntityId : "", journalType, active }, signal),
    `${legalEntityId}|${journalType}|${active}|${attempt}`,
  );
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  const change = (row: JournalName, patch: JournalNamePatch, done: string) => ({
    run: () => updateJournalName(row.id, row.version, patch),
    onDone: () => {
      setFailure(null);
      setNotice(done);
      setAttempt((n) => n + 1);
    },
    onError: setFailure,
  });

  const columns: DataTableColumn<JournalName>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "description", header: "Description", render: (row) => row.description || dash },
    { key: "journalType", header: "Journal type", render: (row) => typeLabels[row.journalType] },
    {
      key: "requiresApproval",
      header: "Approval",
      render: (row) => <Badge tone={row.requiresApproval ? "warning" : "neutral"}>{row.requiresApproval ? "Required before posting" : "Not required"}</Badge>,
    },
    { key: "offsetAccountId", header: "Offset account", mono: true, render: (row) => row.offsetAccountId || dash },
    { key: "active", header: "State", render: (row) => <Badge tone={row.active ? "success" : "neutral"}>{row.active ? "Active" : "Inactive"}</Badge> },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <span className={styles.rowActions}>
          <RowAction
            label={row.requiresApproval ? "Drop approval" : "Require approval"}
            name={row.code}
            {...change(row, { requiresApproval: !row.requiresApproval }, `${row.code} ${row.requiresApproval ? "no longer requires" : "now requires"} approval.`)}
          />
          <RowAction
            label={row.active ? "Deactivate" : "Activate"}
            name={row.code}
            {...change(row, { active: !row.active }, `${row.code} is ${row.active ? "inactive" : "active"}.`)}
          />
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Journal names"
        description="A journal is created under a name. A name can require the approval workflow before its journals post and can default the offset account of movements."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/journal-names/new">
            New journal name
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="jn-entity" label="Legal entity" optional error={entityValid ? undefined : "Enter the legal entity id (a UUID)."}>
          {(control) => <Input {...control} value={legalEntityId} onChange={(event) => setLegalEntityId(event.target.value)} invalid={!entityValid} />}
        </FormField>
        <FormField id="jn-type" label="Journal type">
          {(control) => (
            <Select {...control} value={journalType} onChange={(event) => setJournalType(event.target.value)}>
              <option value="">All types</option>
              {KEYED_JOURNAL_TYPES.map((t) => (
                <option key={t} value={t}>
                  {typeLabels[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="jn-active" label="State">
          {(control) => (
            <Select {...control} value={active} onChange={(event) => setActive(event.target.value as "true" | "false" | "")}>
              <option value="true">Active</option>
              <option value="false">Inactive</option>
              <option value="">All</option>
            </Select>
          )}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {load.error ? <ProblemAlert title="Could not load journal names" problem={load.error} onRetry={load.reload} /> : null}
      {failure ? <ProblemAlert title="The journal name was not changed" problem={failure} /> : null}
      {load.error ? null : (
        <DataTable
          label="Journal names"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No journal names", description: "Create a journal name to route journals through approval or default their offset account." }}
        />
      )}
    </div>
  );
}
