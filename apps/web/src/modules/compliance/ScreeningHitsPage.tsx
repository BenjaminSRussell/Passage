import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { HIT_STATES, listScreeningHits, SUBJECT_TYPES, type HitFilter, type HitState, type ScreeningHit, type SubjectType } from "./api/client";
import { ageDays, formatDateTime, hitStateTone, scoreText, sentence } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ScreeningHit>[] = [
  { key: "subjectName", header: "Subject", render: (h) => <span title={h.subjectId}>{h.subjectName}</span> },
  { key: "subjectType", header: "Type", render: (h) => sentence(h.subjectType) },
  { key: "listCode", header: "List", mono: true },
  { key: "listEntryName", header: "List entry", render: (h) => <span title={h.listEntryId}>{h.listEntryName || h.listEntryId}</span> },
  { key: "score", header: "Score", align: "end", mono: true, render: (h) => scoreText(h.score) },
  { key: "matchedFields", header: "Matched on", render: (h) => h.matchedFields.join(", ") },
  { key: "state", header: "State", render: (h) => <Badge tone={hitStateTone[h.state]}>{sentence(h.state)}</Badge> },
  { key: "age", header: "Age", align: "end", render: (h) => <span title={formatDateTime(h.detectedAt)}>{ageDays(h.detectedAt)} d</span> },
];

/**
 * Screening workbench (PRD §9.48.6, duty TradeComplianceView): the hits that
 * screening raised, filtered by state, list and subject type. Defaults to
 * OPEN, the queue a trade compliance officer clears or confirms.
 */
export function ScreeningHitsPage() {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<{ state: HitState | ""; listCode: string; subjectType: SubjectType | "" }>({ state: "OPEN", listCode: "", subjectType: "" });
  const [filter, setFilter] = useState<HitFilter>({ state: "OPEN" });
  const [extra, setExtra] = useState<{ items: ScreeningHit[]; cursor?: string; loading: boolean; error?: string }>({ items: [], loading: false });
  const state = useLoad((signal) => listScreeningHits(filter, signal), JSON.stringify(filter));
  const rows = [...(state.data?.items ?? []), ...extra.items];
  const cursor = extra.cursor ?? state.data?.nextCursor;

  async function more() {
    if (!cursor) return;
    setExtra((e) => ({ ...e, loading: true, error: undefined }));
    try {
      const page = await listScreeningHits({ ...filter, cursor });
      setExtra((e) => ({ items: [...e.items, ...page.items], cursor: page.nextCursor ?? "", loading: false }));
    } catch (error) {
      setExtra((e) => ({ ...e, loading: false, error: error instanceof Error ? error.message : "Request failed" }));
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Screening hits"
        description="Parties and documents that match a sanctions or denied-party list entry. An open or confirmed hit holds order release, shipment, purchase order sending and payment for that subject."
        actions={<Link to="/compliance/screening/check">Screen a party or document</Link>}
      />
      <form
        className={styles.toolbar}
        aria-label="Filter screening hits"
        onSubmit={(e) => {
          e.preventDefault();
          setExtra({ items: [], loading: false });
          setFilter({ state: draft.state, listCode: draft.listCode.trim().toUpperCase(), subjectType: draft.subjectType });
        }}
      >
        <FormField id="hit-state" label="State">
          {(control) => (
            <Select {...control} value={draft.state} onChange={(e) => setDraft({ ...draft, state: e.target.value as HitState | "" })}>
              <option value="">Any state</option>
              {HIT_STATES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="hit-list" label="List" optional>
          {(control) => <Input {...control} className={styles.mono} placeholder="OFAC_SDN" value={draft.listCode} onChange={(e) => setDraft({ ...draft, listCode: e.target.value })} />}
        </FormField>
        <FormField id="hit-subject-type" label="Subject type">
          {(control) => (
            <Select {...control} value={draft.subjectType} onChange={(e) => setDraft({ ...draft, subjectType: e.target.value as SubjectType | "" })}>
              <option value="">Parties and documents</option>
              {SUBJECT_TYPES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit">Filter</Button>
      </form>
      {state.error ? (
        <Alert tone="danger" title="Could not load screening hits" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Screening hits"
          columns={columns}
          rows={rows}
          getRowKey={(h) => h.hitId}
          loading={state.loading}
          onRowOpen={(h) => navigate(`/compliance/screening/hits/${h.hitId}`)}
          empty={{ title: "No hits match", description: "Hits appear here when screening matches a party or document to a list entry at or above the tenant threshold." }}
        />
      )}
      {extra.error ? (
        <Alert tone="danger" live="alert">
          {extra.error}
        </Alert>
      ) : null}
      {cursor && !state.loading && !state.error ? (
        <div className={styles.more}>
          <Button loading={extra.loading} onClick={() => void more()}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}
