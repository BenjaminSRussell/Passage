import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select } from "../../shell/ui";
import type { FlowDraft, FlowStep, GraphIssue } from "./domain/flowGraph";
import { goBack, startFlow, submitStep, type RunnerResult, type RunnerState } from "./domain/flowRunner";
import { CLASS_LABEL } from "./domain/gs1";
import { appendTiming, rollup, type StepRollup, type StepTiming } from "./domain/telemetry";
import styles from "./pages.module.css";

/** The simulator runs as a designer user on a virtual device (§6.32.6 "designer simulation"). */
const SIM_CONTEXT = { deviceId: "designer-simulator", userId: "designer" };

interface Notice {
  tone: "danger" | "warning";
  text: string;
}

function parseFlow(text: string): FlowDraft | null {
  try {
    return JSON.parse(text) as FlowDraft;
  } catch {
    return null;
  }
}

function expectedText(step: FlowStep): string {
  return (step.expectedScan ?? ["ANY"]).map((c) => CLASS_LABEL[c]).join(" or ");
}

/**
 * Runs the flow in the editor step by step with the device's runner (BR-32.2, AC-32.7): a
 * virtual scanner feeds every step, so a scan on a non-scan step is shown as ignored, and
 * each step's timing is rolled up as the KPI layer would (FR-32.11).
 */
export function FlowSimulator({ flowText, now = Date.now }: { flowText: string; now?: () => number }) {
  const [flow, setFlow] = useState<FlowDraft | null>(null);
  const [state, setState] = useState<RunnerState | null>(null);
  const [issues, setIssues] = useState<GraphIssue[]>([]);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [ignored, setIgnored] = useState<string[]>([]);
  const [timings, setTimings] = useState<StepTiming[]>([]);
  const [scan, setScan] = useState("");
  const [value, setValue] = useState("");

  function run() {
    const parsed = parseFlow(flowText);
    setNotice(null);
    setIgnored([]);
    setTimings([]);
    setScan("");
    setValue("");
    if (!parsed) {
      setFlow(null);
      setState(null);
      setIssues([{ code: "MOB-0113", message: "The flow is not valid JSON." }]);
      return;
    }
    const started = startFlow(parsed, now());
    if (!started.ok) {
      setFlow(null);
      setState(null);
      setIssues(started.issues);
      return;
    }
    setIssues([]);
    setFlow(parsed);
    setState(started.state);
  }

  function apply(result: RunnerResult) {
    if (result.ok || !result.ignored) {
      const t = result.timing;
      if (t) setTimings((log) => {
        const next = appendTiming(log, t, now());
        return "log" in next ? next.log : log;
      });
    }
    if (result.ok) {
      setNotice(null);
      setState(result.state);
      setValue("");
      return;
    }
    if (result.ignored) {
      setIgnored((log) => [...log, result.message]);
      setNotice({ tone: "warning", text: result.message });
      return;
    }
    setNotice({ tone: "danger", text: result.code ? `${result.code} ${result.message}` : result.message });
  }

  function onScan(event: FormEvent) {
    event.preventDefault();
    if (!flow || !state || scan.trim() === "") return;
    apply(submitStep(flow, state, { kind: "scan", raw: scan.trim(), source: "KEYBOARD_WEDGE" }, SIM_CONTEXT, now()));
    setScan("");
  }

  function onValue(event: FormEvent) {
    event.preventDefault();
    if (flow && state) apply(submitStep(flow, state, { kind: "value", value }, SIM_CONTEXT, now()));
  }

  function onConfirm() {
    if (flow && state) apply(submitStep(flow, state, { kind: "confirm" }, SIM_CONTEXT, now()));
  }

  function onBack() {
    if (state) setState(goBack(state, now()));
    setNotice(null);
  }

  const step = flow && state?.stepId ? flow.steps.find((s) => s.id === state.stepId) : undefined;
  const rows = rollup(timings);

  return (
    <Card title="Flow simulator" actions={<Button onClick={run}>{state ? "Restart flow" : "Run flow"}</Button>}>
      <div className={styles.stack}>
        <p className={styles.note}>
          Runs the flow above on a virtual device. Scans go through the same rules as a handheld: the expected class, GS1 check digits and dates, and
          scans that arrive while a non-scan step has focus are ignored.
        </p>
        {issues.length > 0 ? (
          <Alert tone="danger" title="The flow cannot run">
            <ul aria-label="Simulator issues" className={styles.plainList}>
              {issues.map((i, n) => (
                <li key={n}>
                  {i.code}: {i.message}
                </li>
              ))}
            </ul>
          </Alert>
        ) : null}
        {flow && state ? (
          <section aria-label="Simulated device" className={styles.stack}>
            {step ? (
              <>
                <p className={styles.value}>
                  <strong>{step.prompt}</strong>
                </p>
                <p className={styles.note}>
                  Step {step.id} ({step.type}){step.type === "scan" ? `, expects ${expectedText(step)}` : ""}
                </p>
                <form className={styles.toolbar} onSubmit={onScan}>
                  <FormField id="sim-run-scan" label="Virtual scanner" hint="Sent as a scan to whichever step has focus.">
                    {(control) => <Input {...control} className={styles.code} value={scan} onChange={(e) => setScan(e.target.value)} />}
                  </FormField>
                  <Button type="submit">Scan</Button>
                </form>
                {step.type === "input" || step.type === "select" ? (
                  <form className={styles.toolbar} onSubmit={onValue}>
                    <FormField id="sim-run-value" label="Value">
                      {(control) => <Input {...control} value={value} onChange={(e) => setValue(e.target.value)} />}
                    </FormField>
                    <Button type="submit">Enter</Button>
                  </form>
                ) : null}
                {step.type === "branch" ? (
                  <form className={styles.toolbar} onSubmit={onValue}>
                    <FormField id="sim-run-outcome" label="Outcome">
                      {(control) => (
                        <Select {...control} value={value} onChange={(e) => setValue(e.target.value)}>
                          <option value="">Choose an outcome</option>
                          {Object.keys(step.next).map((o) => (
                            <option key={o} value={o}>
                              {o}
                            </option>
                          ))}
                        </Select>
                      )}
                    </FormField>
                    <Button type="submit">Choose</Button>
                  </form>
                ) : null}
                <div className={styles.formActions}>
                  {step.type === "confirm" || step.type === "display" || step.type === "call" ? (
                    <Button variant="primary" onClick={onConfirm}>
                      Confirm
                    </Button>
                  ) : null}
                  <Button variant="ghost" onClick={onBack} disabled={state.history.length === 0}>
                    Back
                  </Button>
                </div>
              </>
            ) : (
              <Alert tone="success" title="Flow finished">
                <ul aria-label="Captured values" className={styles.plainList}>
                  {state.history.map((id) => (
                    <li key={id} className={styles.code}>
                      {id}: {state.values[id] ?? "confirmed"}
                    </li>
                  ))}
                </ul>
              </Alert>
            )}
            {notice ? (
              <Alert tone={notice.tone} live="alert">
                {notice.text}
              </Alert>
            ) : null}
            {ignored.length > 0 ? (
              <div>
                <p className={styles.term}>Ignored scans ({ignored.length})</p>
                <ul aria-label="Ignored scans" className={styles.plainList}>
                  {ignored.map((m, n) => (
                    <li key={n} className={styles.note}>
                      {m}
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
            <DataTable<StepRollup>
              label="Step timing"
              rows={rows}
              getRowKey={(r) => r.stepId}
              empty={{ title: "No steps run yet", description: "Each scan, value or confirmation adds a timing for its step." }}
              columns={[
                { key: "stepId", header: "Step", mono: true },
                { key: "count", header: "Attempts", align: "end" },
                { key: "errors", header: "Errors", align: "end" },
                { key: "medianS", header: "Median (s)", align: "end" },
                { key: "p95S", header: "p95 (s)", align: "end" },
              ]}
            />
          </section>
        ) : null}
      </div>
    </Card>
  );
}
