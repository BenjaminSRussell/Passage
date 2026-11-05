import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card } from "../../shell/ui";
import { FxApiError, IMPORT_PROVIDERS, importNow, type ImportRun, type RateType } from "./api/client";
import { ApiFailure } from "./formSupport";
import styles from "./pages.module.css";

const DUTY = "fx.rate.administer";

const STILL_RUNNING = { title: "Import still running", message: "An import for this day is still running. Try again shortly.", tone: "warning" } as const;

function summary(run: ImportRun): { title: string; message: string; tone: "success" | "warning" } {
  if (run.degraded) {
    return { title: "The provider is unavailable", message: `No new rates were stored. Lookups keep using the last imported rates${run.lastSuccessAt ? ` from ${run.lastSuccessAt}` : ""}.`, tone: "warning" };
  }
  if (run.pending) {
    return { title: "Latest rates stored", message: `${run.provider} has not published rates for today yet. The rates dated ${run.date} were stored (${run.imported} new, ${run.unchanged} unchanged); import again later.`, tone: "warning" };
  }
  if (run.alreadyDone) {
    return { title: "Already imported", message: `${run.provider} rates dated ${run.date} were imported earlier, so nothing changed.`, tone: "success" };
  }
  return { title: "Import finished", message: `${run.imported} rate${run.imported === 1 ? "" : "s"} stored and ${run.unchanged} unchanged from ${run.provider} dated ${run.date}.`, tone: "success" };
}

export function ImportNow({ rateType }: { rateType: RateType | undefined }) {
  const queryClient = useQueryClient();
  const run = useMutation({
    mutationFn: ({ provider, code }: { provider: string; code: string }) => importNow(provider, code),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["fx", "history"] });
    },
  });
  const provider = rateType?.provider;
  if (!rateType || !provider || !IMPORT_PROVIDERS.includes(provider)) return null;
  const stillRunning = run.error instanceof FxApiError && run.error.code === "FX_IMPORT_IN_PROGRESS";
  const result = run.data ? summary(run.data) : stillRunning ? STILL_RUNNING : null;
  return (
    <Card title={`Import ${rateType.code} now`}>
      <div className={styles.stack}>
        <p>{`Fetch the latest rates from ${provider} now instead of waiting for the schedule. Running it again the same day changes nothing.`}</p>
        <div className={styles.formActions}>
          <Button variant="secondary" loading={run.isPending} onClick={() => run.mutate({ provider, code: rateType.code })}>
            Import now
          </Button>
        </div>
        <Alert tone={result?.tone ?? "success"} live="status" title={result?.title}>
          {result?.message}
        </Alert>
        {run.isError && !stillRunning ?<ApiFailure error={run.error} title="Could not import the rates" duty={DUTY} /> : null}
      </div>
    </Card>
  );
}
