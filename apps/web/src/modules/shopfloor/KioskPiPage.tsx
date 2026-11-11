import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { OrderNumber } from "../mfg-shared/orders";
import { ResourceName } from "../mfg-shared/resources";
import { Alert, Button, Card, PageHeader, Skeleton } from "../../shell/ui";
import { createProcessMessage, getControlRecipe, reportControlRecipe, type ControlRecipeDetail } from "./api/client";
import type { ProcessMessageDraft } from "./piSheet";
import { PiSheetRunner } from "./PiSheetRunner";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One kiosk recipe, rendered as a PI sheet. Starting it reports it received and in execution; finishing completes it. */
export function KioskPiPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getControlRecipe(id, signal), id);
  const [started, setStarted] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const recipe: ControlRecipeDetail | null = state.data;
  const status = useRef<string | null>(null);

  async function start(r: ControlRecipeDetail) {
    setBusy(true);
    setFailure(null);
    try {
      if (r.status === "sent") await reportControlRecipe(r.id, { status: "received" });
      if (r.status === "sent" || r.status === "received") await reportControlRecipe(r.id, { status: "in-execution" });
      status.current = "in-execution";
      setStarted(true);
    } catch (error) {
      setFailure(error instanceof Error ? error.message : "The recipe could not be started.");
    } finally {
      setBusy(false);
    }
  }

  async function record(r: ControlRecipeDetail, message: ProcessMessageDraft) {
    await createProcessMessage({
      source: "kiosk",
      sourceMessageId: message.sourceMessageId,
      categoryCode: message.categoryCode,
      orderId: r.orderId,
      phasePath: message.phasePath,
      occurredAt: new Date().toISOString(),
      values: message.values,
    });
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="PI sheet" description={<Link to="/shopfloor/kiosk">Back to the kiosk</Link>} />
      {state.error && !recipe ? (
        <Alert tone="danger" title="Could not load the recipe" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !recipe ? (
        <Card>
          <Skeleton />
        </Card>
      ) : recipe.status === "completed" ? (
        <Alert tone="success" title="This recipe is completed">Nothing is left to do.</Alert>
      ) : recipe.status === "error" ? (
        <Alert tone="danger" title="This recipe is in error">{recipe.lastError ?? "Ask your supervisor to send it again."}</Alert>
      ) : recipe.destination !== "kiosk" ? (
        <Alert tone="info" title="This recipe runs on a process control system">It is not worked at a kiosk.</Alert>
      ) : (
        <div className={`${styles.kiosk} ${styles.stack}`}>
          <Card>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Order</dt>
                <dd className={styles.value}><OrderNumber id={recipe.orderId} /></dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Resource</dt>
                <dd className={styles.value}><ResourceName id={recipe.resourceId} /></dd>
              </div>
            </dl>
          </Card>
          {failure ? <Alert tone="danger" title="Could not start" live="alert">{failure}</Alert> : null}
          {started || recipe.status === "in-execution" ? (
            <PiSheetRunner
              doc={{ procedure: recipe.recipe.procedure ?? { name: "", unitProcedures: [] } }}
              recipeId={recipe.id}
              onRecord={(message) => record(recipe, message)}
              onComplete={async () => {
                await reportControlRecipe(recipe.id, { status: "completed" });
              }}
            />
          ) : (
            <Button className={styles.kioskButton} variant="primary" loading={busy} onClick={() => void start(recipe)}>
              Start
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
