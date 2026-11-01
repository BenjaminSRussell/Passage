import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, HOLD_STATES, listHolds, releaseHold, type Hold, type HoldState, type Problem } from "./api/client";
import { dash, DECIMAL, describeDimensions, sentence, shortId } from "./format";
import { ALL_DIMENSIONS, ProblemAlert, stateTones } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Most holds the service returns in one call; it has no cursor, so the screen says when the list may be cut off. */
export const HOLD_LIST_LIMIT = 200;

/** Checks an optional partial release quantity against what the hold still holds. */
export function validateReleaseQty(raw: string, held: string): string | undefined {
  const qty = raw.trim();
  if (qty === "") return undefined;
  if (!DECIMAL.test(qty) || Number(qty) <= 0) return "Enter a quantity above zero, or leave it empty to release the whole hold.";
  if (Number(qty) > Number(held)) return `Only ${held} is still held.`;
  return undefined;
}

/** Release controls of one active hold: an optional quantity, empty releases what is left. */
function ReleaseHold({ hold, onDone, onError }: { hold: Hold; onDone(message: string): void; onError(problem: Problem): void }) {
  const [qty, setQty] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  async function release() {
    const found = validateReleaseQty(qty, hold.heldQty);
    setError(found);
    if (found) return;
    setBusy(true);
    try {
      const out = await releaseHold(hold.id, hold.version, qty.trim() || undefined);
      onDone(`Released ${qty.trim() || hold.heldQty} of hold ${shortId(out.id)}: it is available again.`);
    } catch (e) {
      onError(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <span className={styles.check}>
      <Input
        aria-label={`Quantity to release from hold ${shortId(hold.id)}`}
        aria-invalid={error ? true : undefined}
        inputMode="decimal"
        placeholder={hold.heldQty}
        value={qty}
        onChange={(event) => setQty(event.target.value)}
      />
      <Button size="sm" loading={busy} onClick={release} aria-label={`Release hold ${shortId(hold.id)}`}>
        Release
      </Button>
      {error ? <span role="alert">{error}</span> : null}
    </span>
  );
}

/** Hold inquiry: stock kept out of availability for a quality, damage, recall or legal reason, with release. */
export function HoldsPage() {
  const [state, setState] = useState<HoldState | "">("ACTIVE");
  const [source, setSource] = useState("");
  const [attempt, setAttempt] = useState(0);
  const load = useLoad((signal) => listHolds({ state, sourceType: source.trim().toUpperCase(), limit: HOLD_LIST_LIMIT }, signal), `${state}|${source}|${attempt}`);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");

  const columns: DataTableColumn<Hold>[] = [
    { key: "productId", header: "Product", mono: true, render: (row) => shortId(row.productId) },
    {
      key: "dimensions",
      header: "Dimensions",
      render: (row) => (row.wholeLot ? `Whole lot, ${describeDimensions(row.dimensions, ALL_DIMENSIONS)}` : describeDimensions(row.dimensions, ALL_DIMENSIONS)),
    },
    { key: "heldQty", header: "Held", align: "end", mono: true },
    { key: "qty", header: "Placed", align: "end", mono: true },
    { key: "reason", header: "Reason", render: (row) => (row.note ? `${sentence(row.reason)}: ${row.note}` : sentence(row.reason)) },
    { key: "source", header: "Source", render: (row) => `${row.sourceType} ${row.sourceRef}` },
    { key: "state", header: "State", render: (row) => <Badge tone={stateTones[row.state] ?? "neutral"}>{sentence(row.state)}</Badge> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.state === "ACTIVE" ? (
          <ReleaseHold
            hold={row}
            onDone={(message) => {
              setFailure(null);
              setNotice(message);
              setAttempt((n) => n + 1);
            }}
            onError={(problem) => {
              setNotice("");
              setFailure(problem);
              // A stale version (someone released it meanwhile): reread so the next try uses the current one.
              if (problem.code === "conflict_version") setAttempt((n) => n + 1);
            }}
          />
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Holds"
        description="Stock kept out of availability without moving it. Held stock stays in physical stock and cannot be issued, reserved or picked until it is released."
        actions={
          <Link className={buttonClassName("primary")} to="/inventory/holds/new">
            Place hold
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="holds-state" label="State">
          {(control) => (
            <Select {...control} value={state} onChange={(event) => setState(event.target.value as HoldState | "")}>
              <option value="">All states</option>
              {HOLD_STATES.map((s) => (
                <option key={s} value={s}>
                  {sentence(s)}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="holds-source" label="Source type" optional>
          {(control) => <Input {...control} value={source} maxLength={32} onChange={(event) => setSource(event.target.value)} />}
        </FormField>
      </div>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {load.error ? <ProblemAlert title="Could not load holds" problem={load.error} onRetry={load.reload} /> : null}
      {failure ? <ProblemAlert title="The hold was not released" problem={failure} /> : null}
      {(load.data?.length ?? 0) >= HOLD_LIST_LIMIT ? (
        <Alert tone="info">Showing the first {HOLD_LIST_LIMIT} holds. Filter by state or source type to see the rest.</Alert>
      ) : null}
      {load.error ? null : (
        <DataTable
          label="Holds"
          columns={columns}
          rows={load.data ?? []}
          getRowKey={(row) => row.id}
          loading={load.loading}
          empty={{ title: "No holds", description: "Place a hold to keep a quantity or a whole lot out of availability while it is checked." }}
        />
      )}
    </div>
  );
}
