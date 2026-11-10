import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import { ProblemMessage } from "../../shell/errors";
import { OrderNumber } from "../mfg-shared/orders";
import { ResourceName } from "../mfg-shared/resources";
import {
  describeProblem,
  DUTY_CONTROL_RECIPE_CREATE,
  getControlRecipe,
  newKey,
  resendControlRecipe,
  type ControlRecipe,
  type ControlRecipeDetail,
  type Problem,
  type RecipeHistoryEntry,
  type RecipeWrite,
} from "./api/client";
import { DESTINATION_LABEL, RecipeStatusBadge } from "./ControlRecipesPage";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Resends are allowed five times (BR-25.11); the count here is the sends so far, the first included. */
export const MAX_SENDS = 6;

const historyColumns: DataTableColumn<RecipeHistoryEntry>[] = [
  { key: "at", header: "When", mono: true, render: (row) => formatDateTime(row.at) },
  { key: "toStatus", header: "Status", render: (row) => <RecipeStatusBadge status={row.toStatus} /> },
  { key: "actor", header: "By" },
  { key: "note", header: "Note", render: (row) => (row.note ? `${row.note}${row.forced ? " (forced)" : ""}` : row.forced ? "Forced" : dash) },
];

const writeColumns: DataTableColumn<RecipeWrite>[] = [
  { key: "at", header: "When", mono: true, render: (row) => formatDateTime(row.at) },
  { key: "phasePath", header: "Phase", mono: true },
  { key: "node", header: "Node", mono: true },
  { key: "value", header: "Setpoint", align: "end", mono: true },
  { key: "readback", header: "Read back", align: "end", mono: true, render: (row) => row.readback ?? dash },
  {
    key: "result",
    header: "Result",
    render: (row) =>
      !row.written ? (
        <Badge tone="danger">{`Refused: ${row.reason ?? "not written"}`}</Badge>
      ) : row.deviation ? (
        <Badge tone="warning">Readback differs</Badge>
      ) : (
        <Badge tone="success">Written</Badge>
      ),
  },
];

/** One control recipe: its instructions, status history, the connector's write audit and, for a recipe in error, resend (BR-25.11). */
export function ControlRecipeDetailPage() {
  const { id = "" } = useParams();
  const canResend = holdsDuty(usePrincipalDuties(), DUTY_CONTROL_RECIPE_CREATE);
  const state = useLoad((signal) => getControlRecipe(id, signal), id);
  const [sent, setSent] = useState<ControlRecipe | null>(null);
  const recipe: ControlRecipeDetail | null = state.data;
  const status = sent && sent.id === id ? sent.status : recipe?.status;

  return (
    <div className={styles.stack}>
      <PageHeader title="Control recipe" description={<Link to="/shopfloor/control-recipes">All control recipes</Link>} />
      {state.error && !recipe ? (
        <Alert tone="danger" title="Could not load the control recipe" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !recipe ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          {sent ? (
            <Alert tone="success" title="Sent again" live="status">
              The recipe went back to the destination and is waiting to be received.
            </Alert>
          ) : null}
          <Card>
            <dl className={styles.details}>
              <Entry term="Order" value={<OrderNumber id={recipe.orderId} />} />
              <Entry term="Resource" value={<ResourceName id={recipe.resourceId} />} />
              <Entry term="Destination" value={DESTINATION_LABEL[recipe.destination] ?? recipe.destination} />
              <Entry term="Status" value={status ? <RecipeStatusBadge status={status} /> : dash} />
              <Entry term="Times sent" value={String(sent && sent.id === id ? sent.sendCount : recipe.sendCount)} />
              <Entry term="Last error" value={recipe.lastError ?? "None"} />
            </dl>
          </Card>
          {canResend && status === "error" ? (
            <ResendForm
              recipe={recipe}
              onSent={(saved) => {
                setSent(saved);
                state.reload();
              }}
            />
          ) : null}
          <Card title="Instructions">
            {(recipe.recipe.procedure?.unitProcedures ?? []).map((up) =>
              up.operations.map((op) =>
                op.phases.map((ph) => (
                  <section key={`${up.name}/${op.name}/${ph.name}`} aria-label={`${up.name}, ${op.name}, ${ph.name}`}>
                    <h3>{`${up.name} / ${op.name} / ${ph.name}`}</h3>
                    <ol>
                      {ph.instructions.map((ins) => (
                        <li key={ins.seq}>
                          {ins.category === "process-parameter-setpoint"
                            ? `Set ${ins.node ?? "the setpoint"} to ${ins.value}${ins.text ? `: ${ins.text}` : ""}`
                            : (ins.text ?? ins.category)}
                        </li>
                      ))}
                    </ol>
                  </section>
                )),
              ),
            )}
          </Card>
          <Card title="History" padded={false}>
            <DataTable label="Status history" columns={historyColumns} rows={recipe.history} getRowKey={(row) => row.id} empty={{ title: "No history", description: "" }} />
          </Card>
          {recipe.destination !== "kiosk" ? (
            <Card title="Setpoint writes" padded={false}>
              <DataTable
                label="Setpoint writes"
                columns={writeColumns}
                rows={recipe.writes}
                getRowKey={(row) => row.id}
                empty={{ title: "No writes yet", description: "The connector writes each setpoint once the recipe is in execution and records the value it reads back." }}
              />
            </Card>
          ) : null}
        </>
      )}
    </div>
  );
}

function Entry({ term, value }: { term: string; value: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{value}</dd>
    </div>
  );
}

function ResendForm({ recipe, onSent }: { recipe: ControlRecipeDetail; onSent(saved: ControlRecipe): void }) {
  const [force, setForce] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const used = recipe.sendCount - 1;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (force && reason.trim() === "") {
      setError("Say why it is safe to send the recipe again, up to 1000 characters.");
      return;
    }
    setError(null);
    setBusy(true);
    try {
      onSent(await resendControlRecipe(recipe.id, { force, reason: reason.trim() }, key.current));
      key.current = newKey();
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reason) setError(problem.fields.reason);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Send again">
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={styles.note}>{`This recipe was sent again ${used} of 5 times. If the process control system has already reported on this order it may have started the recipe, so sending it again needs force and a reason.`}</p>
        <label className={styles.checkRow} htmlFor="resend-force">
          <Input id="resend-force" type="checkbox" checked={force} onChange={(event) => setForce(event.target.checked)} />
          The process control system may have started this recipe (force)
        </label>
        <FormField id="resend-reason" label="Reason" optional={!force} error={error ?? undefined}>
          {(control) => <Textarea {...control} rows={3} maxLength={1000} value={reason} onChange={(event) => setReason(event.target.value)} invalid={Boolean(error)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not send the recipe again" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Send recipe again
          </Button>
        </div>
      </form>
    </Card>
  );
}
