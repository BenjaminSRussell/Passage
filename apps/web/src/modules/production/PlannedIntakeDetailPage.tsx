import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Alert, Badge, Button, Card, FormField, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, dismissPlannedIntake, getPlannedIntake, type PlannedIntake, type Problem } from "./api/client";
import { dash, intakeStatusLabel, intakeStatusTone } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";

export interface PlannedIntakeDetailPageProps {
  intakeId: string;
  onBack?(): void;
  /** Opens the create form that converts this planned order. */
  onConvert?(row: PlannedIntake): void;
  /** Opens the production order a converted row made. */
  onOpenOrder?(orderId: string): void;
}

const maxReason = 500;

function Detail({ term, children, mono = false }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={`${styles.value} ${mono ? styles.mono : ""}`}>{children}</dd>
    </div>
  );
}

/** One firmed planned order (FR-22.10): its facts, then convert it into a production order or dismiss it. */
export function PlannedIntakeDetailPage({ intakeId, onBack, onConvert, onOpenOrder }: PlannedIntakeDetailPageProps) {
  const [row, setRow] = useState<PlannedIntake | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Problem | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        setRow(await getPlannedIntake(intakeId, signal));
        setError(null);
      } catch (failed) {
        if (!signal?.aborted) {
          setError(describeProblem(failed));
        }
      } finally {
        if (!signal?.aborted) {
          setLoading(false);
        }
      }
    },
    [intakeId],
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const dismiss = async (event: FormEvent) => {
    event.preventDefault();
    if (!row) {
      return;
    }
    if (reason.trim() === "" || reason.length > maxReason) {
      setReasonError(`Enter a reason of up to ${maxReason} characters.`);
      return;
    }
    setReasonError("");
    setBusy(true);
    setFailure(null);
    setNotice("");
    try {
      setRow(await dismissPlannedIntake(row.id, row.version, reason.trim()));
      setNotice("Planned order dismissed: no production order is created for it.");
    } catch (failed) {
      setFailure(describeProblem(failed));
    } finally {
      setBusy(false);
    }
  };

  if (loading) {
    return (
      <div className={styles.stack} aria-busy="true">
        <Skeleton height="text" />
        <Skeleton height="tile" />
      </div>
    );
  }
  if (error || !row) {
    return (
      <div className={styles.stack}>
        <Alert tone={error?.status === 404 ? "info" : "danger"} title="The planned order is not available" live="alert">
          {error?.status === 404 ? "This planned order does not exist." : `${error?.message ?? "Request failed"} (${error?.code ?? "COM-0001"})`}
        </Alert>
        {onBack ? <Button onClick={onBack}>Back to planned orders</Button> : null}
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Planned order of ${row.quantity}`}
        description="Planning firmed this production order. Converting it creates the order under the planned order's id."
        actions={
          <>
            {onBack ? <Button onClick={onBack}>Back to planned orders</Button> : null}
            {row.status === "open" && onConvert ? (
              <Button variant="primary" onClick={() => onConvert(row)}>
                Convert to production order
              </Button>
            ) : null}
            {row.orderId && onOpenOrder ? <Button onClick={() => onOpenOrder(row.orderId as string)}>Open production order</Button> : null}
          </>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" live="alert" title="The planned order was not saved">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Planned order">
        <dl className={styles.details}>
          <Detail term="Status">
            <Badge tone={intakeStatusTone[row.status]}>{intakeStatusLabel[row.status]}</Badge>
          </Detail>
          <Detail term="Product">
            <EntityName entity="product" id={row.productId} fallback={row.productId} />
          </Detail>
          <Detail term="Quantity">{row.quantity}</Detail>
          <Detail term="Order date">{row.orderDate}</Detail>
          <Detail term="Delivery date">{row.deliveryDate}</Detail>
          <Detail term="Site">
            <EntityName entity="site" id={row.siteId} fallback={row.siteId} />
          </Detail>
          <Detail term="Legal entity">
            {row.legalEntityId ? <EntityName entity="legalEntity" id={row.legalEntityId} fallback="Not available" /> : "Not given by planning"}
          </Detail>
          <Detail term="Warehouse">
            {row.warehouseId ? <EntityName entity="warehouse" id={row.warehouseId} fallback={row.warehouseId} /> : "Site scope: enter the warehouse when converting"}
          </Detail>
          <Detail term="Planned order" mono>
            {row.plannedOrderId}
          </Detail>
          <Detail term="Production order id" mono>
            {row.targetId}
          </Detail>
          <Detail term="Dismissed because">{row.dismissReason ?? dash}</Detail>
        </dl>
      </Card>
      {row.status === "open" ? (
        <Card title="Dismiss">
          <form className={styles.form} onSubmit={dismiss} noValidate aria-label="Dismiss the planned order">
            <FormField id="pi-reason" label="Reason" hint="Why no production order is created." error={reasonError}>
              {(control) => <Textarea {...control} value={reason} onChange={(e) => setReason(e.target.value)} rows={3} />}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" loading={busy} disabled={busy}>
                Dismiss planned order
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
