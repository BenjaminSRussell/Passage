import { useState } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import { describeFlowError, saveDraft } from "./api/client";
import { CORE_FLOWS, RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import { type FlowDraft, type GraphIssue, validateFlow } from "./domain/flowGraph";
import { checkScan, type Classified, classifyScan, type ScanClass, type ScanVerdict } from "./domain/gs1";
import { FlowSimulator } from "./FlowSimulator";
import styles from "./pages.module.css";

/** The designer opens on the bundled receive/put-away flow; any bundled core flow can be loaded as a starting point. */
const SAMPLE: FlowDraft = RECEIVE_PUTAWAY_FLOW;

const SCAN_CLASS_LABELS: Record<ScanClass, string> = { LP: "LP", GTIN: "GTIN", LOCATION: "Location", ANY: "Any barcode" };

/** Flow designer: edit the graph as JSON, validate it (MOB-0113), save a draft, test single scans and run the whole flow. */
export function FlowDesigner() {
  const [text, setText] = useState(() => JSON.stringify(SAMPLE, null, 2));
  const [startFrom, setStartFrom] = useState(SAMPLE.code);
  const [issues, setIssues] = useState<GraphIssue[]>([]);
  const [message, setMessage] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [scan, setScan] = useState("");
  const [expected, setExpected] = useState<ScanClass>("LP");
  const [simulated, setSimulated] = useState<{ classified: Classified; verdict: ScanVerdict | null } | null>(null);

  function parse(): FlowDraft | null {
    try {
      return JSON.parse(text) as FlowDraft;
    } catch {
      setIssues([{ code: "MOB-0113", message: "The flow is not valid JSON." }]);
      setMessage(null);
      return null;
    }
  }

  function onValidate() {
    setFailure(null);
    const flow = parse();
    if (flow) {
      const found = validateFlow(flow);
      setIssues(found);
      setMessage(found.length === 0 ? "Flow graph is valid and can be published." : null);
    }
  }

  async function onSave() {
    setFailure(null);
    setMessage(null);
    const flow = parse();
    if (!flow) return;
    const found = validateFlow(flow);
    setIssues(found);
    if (found.length > 0) return;
    setBusy(true);
    try {
      const saved = await saveDraft(flow, crypto.randomUUID());
      setMessage(`Saved ${saved.code} as draft version ${saved.version}.`);
    } catch (e) {
      setFailure(describeFlowError(e));
    } finally {
      setBusy(false);
    }
  }

  /** Replaces the editor with a bundled core flow (FR-32.4). */
  function onStartFrom(code: string) {
    const flow = CORE_FLOWS.find((f) => f.code === code);
    if (!flow) return;
    setStartFrom(code);
    setText(JSON.stringify(flow, null, 2));
    setIssues([]);
    setMessage(null);
    setFailure(null);
  }

  function onSimulate() {
    const classified = classifyScan(scan, { expectGs1: true });
    setSimulated({ classified, verdict: checkScan(expected, classified) });
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Mobile flow designer"
        description="Build a handheld flow as a step graph. Publishing checks that every step is reachable and every path ends; devices pick up a published version on their next menu load."
      />
      <Card title="Flow definition">
        <div className={styles.form}>
          <FormField id="flow-start" label="Start from" hint="Loads a bundled core flow into the editor, replacing the current text.">
            {(control) => (
              <Select {...control} value={startFrom} onChange={(e) => onStartFrom(e.target.value)}>
                {CORE_FLOWS.map((f) => (
                  <option key={f.code} value={f.code}>
                    {f.code}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.wide}>
            <FormField id="flow-json" label="Flow definition (JSON)" hint="Steps: scan, input, select, confirm, display, branch, call. A null next target ends the flow.">
              {(control) => <Textarea {...control} rows={18} mono value={text} onChange={(e) => setText(e.target.value)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button onClick={onValidate}>Validate</Button>
            <Button variant="primary" loading={busy} onClick={onSave}>
              Save draft
            </Button>
          </div>
        </div>
      </Card>
      {issues.length > 0 ? (
        <Alert tone="danger" title="The flow cannot be published yet">
          <ul aria-label="Validation issues" className={styles.plainList}>
            {issues.map((i, n) => (
              <li key={n}>
                {i.code}: {i.message}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {message ? (
        <p role="status" className={styles.note}>
          {message}
        </p>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not save the draft" live="alert">
          {failure}
        </Alert>
      ) : null}

      <Card title="Virtual scanner">
        <div className={styles.toolbar}>
          <FormField id="sim-scan" label="Scan value" hint="GS1 strings may start with ]C1, ]d2 or ]Q3; use the GS character between variable fields.">
            {(control) => <Input {...control} className={styles.code} value={scan} onChange={(e) => setScan(e.target.value)} />}
          </FormField>
          <FormField id="sim-expected" label="Expected class">
            {(control) => (
              <Select {...control} value={expected} onChange={(e) => setExpected(e.target.value as ScanClass)}>
                {(Object.keys(SCAN_CLASS_LABELS) as ScanClass[]).map((c) => (
                  <option key={c} value={c}>
                    {SCAN_CLASS_LABELS[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <Button onClick={onSimulate}>Simulate scan</Button>
        </div>
        {simulated ? (
          <div role="status" aria-label="Simulation result" className={styles.stack}>
            <p className={styles.value}>{simulated.verdict ? `${simulated.verdict.code} ${simulated.verdict.message}` : "Accepted."}</p>
            {simulated.classified.gs1 ? (
              <ul className={styles.plainList}>
                {simulated.classified.gs1.order.map((ai) => (
                  <li key={ai} className={styles.code}>
                    AI {ai}: {simulated.classified.gs1?.fields[ai]}
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : null}
      </Card>

      <FlowSimulator flowText={text} />
    </div>
  );
}
