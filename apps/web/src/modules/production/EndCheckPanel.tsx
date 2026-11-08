import { useCallback, useEffect, useState } from "react";
import { Alert, Badge, Button, Card, Skeleton } from "../../shell/ui";
import { describeProblem, getEndCheck, type EndCheck, type Problem } from "./api/client";
import styles from "./pages.module.css";

export interface EndCheckPanelProps {
  orderId: string;
  /** The order's version and status: the check is read again when either changes. */
  version: number;
  status: string;
}

/**
 * "Ready to end?" (FR-21.8): what `:end` would refuse (output not reported or zero output without the duty,
 * a missing as-built record, unposted journals, an end date before the last posting, no posting profile:
 * PROD-0012, PROD-0013, PROD-0021) and what it accepts with a warning (component serials no finished serial
 * holds, PROD-0020). It is a snapshot read with the order's read duty; the command checks again.
 */
export function EndCheckPanel({ orderId, version, status }: EndCheckPanelProps) {
  const [check, setCheck] = useState<EndCheck | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const [loading, setLoading] = useState(true);
  const [round, setRound] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    getEndCheck(orderId, controller.signal).then(
      (value) => {
        if (controller.signal.aborted) return;
        setCheck(value);
        setError(null);
        setLoading(false);
      },
      (failure) => {
        if (!controller.signal.aborted) {
          setCheck(null);
          setError(describeProblem(failure));
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [orderId, version, status, round]);

  const again = useCallback(() => setRound((n) => n + 1), []);

  return (
    <Card
      title="Ready to end?"
      actions={
        <Button variant="ghost" onClick={again} loading={loading && check !== null}>
          Check again
        </Button>
      }
    >
      {loading && !check ? (
        <div role="status" aria-label="Checking whether the order can end">
          <Skeleton height="control" />
        </div>
      ) : error ? (
        <Alert tone="danger" live="alert" title="The end check could not be read">
          {error.message} ({error.code})
        </Alert>
      ) : check ? (
        <div className={styles.stack}>
          <p>
            <Badge tone={check.canEnd ? "success" : "warning"}>{check.canEnd ? "Can end" : "Cannot end yet"}</Badge>
          </p>
          {check.blockers.length > 0 ? (
            <Alert tone="danger" title="Ending is refused until these are resolved">
              <ul className={styles.findings}>
                {check.blockers.map((f) => (
                  <li key={f.code}>{f.message}</li>
                ))}
              </ul>
            </Alert>
          ) : null}
          {check.warnings.length > 0 ? (
            <Alert tone="warning" title="Ending is allowed, with warnings">
              <ul className={styles.findings}>
                {check.warnings.map((f) => (
                  <li key={f.code}>{f.message}</li>
                ))}
              </ul>
            </Alert>
          ) : null}
          {check.blockers.length === 0 && check.warnings.length === 0 ? (
            <p className={styles.hint}>Nothing stands in the way of ending this order.</p>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}
