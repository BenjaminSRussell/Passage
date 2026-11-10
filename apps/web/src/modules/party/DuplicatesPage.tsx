import { useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { getParty, type Party } from "./api/client";
import { displayOrDash, isUuid, partyTypeLabel } from "./format";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";

interface Pair {
  left: Party;
  right: Party;
}

interface CompareRow {
  field: string;
  left: string;
  right: string;
  same: boolean;
}

const columns: DataTableColumn<CompareRow>[] = [
  { key: "field", header: "Field" },
  { key: "left", header: "First party" },
  { key: "right", header: "Second party" },
  {
    key: "same",
    header: "Match",
    render: (row) => (row.same ? "Same" : "Different"),
  },
];

type FieldKey = "partyNumber" | "name" | "partyType" | "status" | "legalName" | "registrationNumber" | "givenName" | "familyName";

const FIELDS: { field: string; key: FieldKey }[] = [
  { field: "Number", key: "partyNumber" },
  { field: "Name", key: "name" },
  { field: "Type", key: "partyType" },
  { field: "Status", key: "status" },
  { field: "Legal name", key: "legalName" },
  { field: "Registration number", key: "registrationNumber" },
  { field: "Given name", key: "givenName" },
  { field: "Family name", key: "familyName" },
];

function value(party: Party, key: FieldKey): string {
  return key === "partyType" ? (partyTypeLabel[party.partyType] ?? party.partyType) : displayOrDash(party[key]);
}

function rows(left: Party, right: Party): CompareRow[] {
  return FIELDS.map(({ field, key }) => {
    const a = value(left, key);
    const b = value(right, key);
    return { field, left: a, right: b, same: a === b };
  });
}

function Comparison({ leftId, rightId }: { leftId: string; rightId: string }) {
  const state = useLoad(async (signal): Promise<Pair> => {
    const [left, right] = await Promise.all([getParty(leftId, signal), getParty(rightId, signal)]);
    return { left, right };
  }, `${leftId}|${rightId}`);

  if (state.error && !state.data) {
    return <ProblemAlert problem={state.error} title="Could not load those parties" retry={state.reload} />;
  }
  if (!state.data) {
    return <DataTable label="Field comparison" columns={columns} rows={[]} getRowKey={(row) => row.field} loading />;
  }

  const { left, right } = state.data;
  const sameRecord = left.id === right.id;
  return (
    <div className={styles.stack}>
      {sameRecord ? (
        <Alert tone="info" title="Same surviving record">
          Both ids resolve to {left.partyNumber}. One of them may already have been merged.
        </Alert>
      ) : null}
      {(left.redirected || right.redirected) && !sameRecord ? (
        <Alert tone="info" title="A merged id was opened">
          At least one of these ids redirected to its survivor. Merge is not offered here.
        </Alert>
      ) : null}
      <Card>
        <p className={styles.note}>
          <Link to={`/party/${left.id}`}>{left.name}</Link>
          {" and "}
          <Link to={`/party/${right.id}`}>{right.name}</Link>
          {". "}
          Each side shows the surviving record the service returned.
        </p>
        <dl className={styles.details} aria-label="Party statuses">
          <div className={styles.entry}>
            <dt className={styles.term}>{left.partyNumber}</dt>
            <dd className={styles.value}>
              <StatusPill status={left.status} />
            </dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>{right.partyNumber}</dt>
            <dd className={styles.value}>
              <StatusPill status={right.status} />
            </dd>
          </div>
        </dl>
      </Card>
      <DataTable label="Field comparison" columns={columns} rows={rows(left, right)} getRowKey={(row) => row.field} />
    </div>
  );
}

/** Review two parties side by side. There is no merge action (issue #450). */
export function DuplicatesPage() {
  const [params, setParams] = useSearchParams();
  const leftId = params.get("a") ?? "";
  const rightId = params.get("b") ?? "";
  const [draft, setDraft] = useState({ a: leftId, b: rightId });
  const [errors, setErrors] = useState<Record<string, string>>({});

  function compare(event: FormEvent) {
    event.preventDefault();
    const a = draft.a.trim();
    const b = draft.b.trim();
    const next: Record<string, string> = {};
    if (!isUuid(a)) next.a = "Enter the first party id (a UUID).";
    if (!isUuid(b)) next.b = "Enter the second party id (a UUID).";
    if (isUuid(a) && isUuid(b) && a === b) next.b = "Enter a different party id to compare.";
    setErrors(next);
    if (Object.keys(next).length > 0) return;
    setParams({ a, b });
  }

  const ready = isUuid(leftId) && isUuid(rightId) && leftId !== rightId;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Duplicate candidates"
        description="Compare two party records the service already knows. The duplicate queue and merge API are not on these screens: merge is ungranted until its approval workflow (#450) is built."
      />
      <form className={styles.form} onSubmit={compare} noValidate aria-label="Compare two parties">
        <FormField id="dup-a" label="First party" error={errors.a}>
          {(control) => (
            <EntityLookup entity="party"
              {...control}
              value={draft.a}
              onChange={(event) => setDraft({ ...draft, a: event.target.value })}
              invalid={Boolean(errors.a)}
            />
          )}
        </FormField>
        <FormField id="dup-b" label="Second party" error={errors.b}>
          {(control) => (
            <EntityLookup entity="party"
              {...control}
              value={draft.b}
              onChange={(event) => setDraft({ ...draft, b: event.target.value })}
              invalid={Boolean(errors.b)}
            />
          )}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" variant="primary">
            Compare
          </Button>
        </div>
      </form>
      {ready ? <Comparison leftId={leftId} rightId={rightId} /> : null}
    </div>
  );
}
