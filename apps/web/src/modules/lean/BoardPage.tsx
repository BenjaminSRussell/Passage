import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, EmptyState, FormField, PageHeader, Select, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import {
  describeProblem,
  fetchBoard,
  KANBAN_STATES,
  listFlows,
  listRules,
  moveKanban,
  type Kanban,
  type KanbanState,
  type Problem,
} from "./api/client";
import { useCommandKeys } from "./commandKeys";
import { formatDateTime, jobDriven, nextState, overdue, shortId, STATE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

/** The board refreshes on its own; SSE push is a later slice (PRD 4.24.19: 30 s polling is the fallback). */
export const REFRESH_MS = 30_000;

interface CardProps {
  card: Kanban;
  busy: boolean;
  onMove(card: Kanban, to: KanbanState): void;
}

function BoardCard({ card, busy, onMove }: CardProps) {
  const [target, setTarget] = useState<KanbanState | "">("");
  const step = nextState(card.state, card.replenishment);
  // A card with jobs reaches In progress and Completed through them (BR-24.6).
  const next = step && jobDriven(card, step) ? null : step;
  const late = overdue(card.due, card.state);
  return (
    <li className={styles.boardCard} aria-busy={busy || undefined}>
      <div className={styles.boardCardHead}>
        <Link className={styles.mono} to={`/lean/kanbans/${card.id}`}>
          {card.cardNumber}
        </Link>
        {late ? <Badge tone="warning">Overdue</Badge> : null}
      </div>
      <dl className={styles.boardFacts}>
        <div>
          <dt className={styles.term}>Quantity</dt>
          <dd className={styles.value}>{card.qty}</dd>
        </div>
        <div>
          <dt className={styles.term}>Item</dt>
          <dd className={styles.value}>
            <EntityName entity="product" id={card.itemId} fallback={shortId(card.itemId)} />
          </dd>
        </div>
        <div>
          <dt className={styles.term}>Due</dt>
          <dd className={styles.value}>{formatDateTime(card.due)}</dd>
        </div>
        {card.eventRefId ? (
          <div>
            <dt className={styles.term}>Demand</dt>
            <dd className={styles.value}>{card.eventRefId}</dd>
          </div>
        ) : null}
      </dl>
      {step && !next ? (
        <p className={styles.muted}>
          {`${card.jobCount} ${card.jobCount === 1 ? "job" : "jobs"}: the card moves on as they are started and completed on the `}
          <Link to={`/lean/kanbans/${card.id}`}>work-cell boards</Link>.
        </p>
      ) : null}
      <div className={styles.actions}>
        {next ? (
          <Button size="sm" variant="primary" loading={busy} onClick={() => onMove(card, next)} aria-label={`Move ${card.cardNumber} to ${STATE_LABELS[next]}`}>
            {`To ${STATE_LABELS[next]}`}
          </Button>
        ) : null}
        <label className="visually-hidden" htmlFor={`move-${card.id}`}>{`Move ${card.cardNumber} to column`}</label>
        <Select id={`move-${card.id}`} value={target} onChange={(e) => setTarget(e.target.value as KanbanState | "")}>
          <option value="">Other column…</option>
          {KANBAN_STATES.filter((s) => s !== card.state && !jobDriven(card, s)).map((s) => (
            <option key={s} value={s}>
              {STATE_LABELS[s]}
            </option>
          ))}
        </Select>
        <Button
          size="sm"
          disabled={target === "" || busy}
          onClick={() => {
            if (target !== "") onMove(card, target);
            setTarget("");
          }}
          aria-label={`Move ${card.cardNumber}`}
        >
          Move
        </Button>
      </div>
    </li>
  );
}

/**
 * Kanban board (FR-24.10): one column per card state. Every move is checked by
 * the service (BR-24.6); a refused move leaves the card where it was and shows
 * why (AC-24.7). The board reloads after each move and every 30 seconds.
 */
export function BoardPage() {
  const [params, setParams] = useSearchParams();
  const flowId = params.get("flowId") ?? "";
  const ruleId = params.get("ruleId") ?? "";
  const flows = useLoad((signal) => listFlows("", signal), "flows");
  const rules = useLoad((signal) => listRules(flowId, signal), `rules|${flowId}`);
  const board = useLoad((signal) => fetchBoard({ flowId, ruleId }, signal), `board|${flowId}|${ruleId}`);
  const [moving, setMoving] = useState<string | null>(null);
  const [failure, setFailure] = useState<(Problem & { card: string; from: KanbanState }) | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const keys = useCommandKeys();
  const { reload } = board;

  useEffect(() => {
    const timer = setInterval(reload, REFRESH_MS);
    return () => clearInterval(timer);
  }, [reload]);

  function filter(patch: { flowId?: string; ruleId?: string }) {
    const next = new URLSearchParams(params);
    for (const [k, v] of Object.entries(patch)) {
      if (v) next.set(k, v);
      else next.delete(k);
    }
    setParams(next, { replace: true });
  }

  async function move(card: Kanban, to: KanbanState) {
    setMoving(card.id);
    setFailure(null);
    setNotice(null);
    try {
      const body = { to, version: card.version };
      const moved = await keys.send(`move ${card.id} ${JSON.stringify(body)}`, (key) => moveKanban(card.id, body, key));
      setNotice(`${moved.cardNumber} moved to ${STATE_LABELS[moved.state]}.`);
    } catch (error) {
      setFailure({ ...describeProblem(error), card: card.cardNumber, from: card.state });
    } finally {
      setMoving(null);
      reload();
    }
  }

  const columns = board.data?.columns ?? [];
  const cards = columns.flatMap((c) => c.kanbans);
  const late = cards.filter((k) => overdue(k.due, k.state)).length;
  const wip = cards.filter((k) => k.state === "prepared" || k.state === "in-progress").length;

  return (
    <div className={styles.stack}>
      <PageHeader title="Kanban board" description="Move a card to the column where its work now is. Moves go one step at a time; the service refuses any other." />
      <div className={styles.toolbar}>
        <FormField id="board-flow" label="Production flow">
          {(c) => (
            <Select {...c} value={flowId} onChange={(e) => filter({ flowId: e.target.value, ruleId: "" })}>
              <option value="">All flows</option>
              {(flows.data ?? []).map((f) => (
                <option key={f.id} value={f.id}>
                  {f.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="board-rule" label="Kanban rule">
          {(c) => (
            <Select {...c} value={ruleId} onChange={(e) => filter({ ruleId: e.target.value })}>
              <option value="">All rules</option>
              {(rules.data ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {r.code}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button onClick={reload}>Refresh</Button>
      </div>
      {failure ? (
        <Alert tone="danger" title={`${failure.card} stays in ${STATE_LABELS[failure.from]}`} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {notice ? <Alert tone="success" title={notice} live="status" /> : null}
      {board.error ? (
        <Alert tone="danger" title="Could not load the board" live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
          <ProblemMessage problem={board.error} />
        </Alert>
      ) : !board.data ? (
        <div aria-busy="true" className={styles.loading}>
          <span className="visually-hidden">Loading the board…</span>
          <Skeleton height="tile" />
        </div>
      ) : cards.length === 0 ? (
        <EmptyState
          icon="grid"
          title="No kanbans on this board"
          description="Generate kanbans from a kanban rule; they appear here as work moves through the cell."
        />
      ) : (
        <>
          <StatGroup label="Board metrics">
            <StatTile label="Cards" value={cards.length} />
            <StatTile label="Work in progress" value={wip} />
            <StatTile label="Overdue" value={late} tone="warning" />
            {board.data.takt ? (
              <StatTile
                label="Takt vs cycle"
                value={`${board.data.takt.taktTimeS} s / ${board.data.takt.cycleTimeS} s`}
                hint={
                  board.data.takt.withinTakt
                    ? `The slowest activity, ${board.data.takt.bottleneckActivity}, keeps up with takt.`
                    : `${board.data.takt.bottleneckActivity} is slower than takt.`
                }
              />
            ) : null}
          </StatGroup>
          {board.data.truncated ? (
            <Alert tone="warning" title="Showing the first 500 cards">
              Filter by flow or rule to see the rest.
            </Alert>
          ) : null}
          <div className={styles.board}>
            {columns.map((column) => (
              <section key={column.state} className={styles.boardColumn} aria-labelledby={`col-${column.state}`}>
                <h3 id={`col-${column.state}`} className={styles.boardHeading}>
                  {`${STATE_LABELS[column.state]} (${column.kanbans.length})`}
                </h3>
                <ul className={styles.boardList} aria-label={`${STATE_LABELS[column.state]} cards`}>
                  {column.kanbans.map((card) => (
                    <BoardCard key={card.id} card={card} busy={moving === card.id} onMove={(c, to) => void move(c, to)} />
                  ))}
                </ul>
              </section>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
