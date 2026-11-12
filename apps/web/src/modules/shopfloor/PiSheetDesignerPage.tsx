import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { ProblemMessage } from "../../shell/errors";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import {
  createPISheet,
  describeProblem,
  DUTY_PI_SHEET_MAINTAIN,
  getPISheet,
  newPISheetVersion,
  obsoletePISheet,
  releasePISheet,
  updatePISheet,
  type PISheet,
  type Problem,
  type RecipeDocument,
} from "./api/client";
import { documentProblems, emptyDocument } from "./piSheet";
import { PiSheetEditor } from "./PiSheetEditor";
import { PiSheetRunner } from "./PiSheetRunner";
import { PiStatusBadge } from "./PiSheetsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** New PI sheet. */
export function NewPiSheetPage() {
  return <Designer sheet={null} reload={() => undefined} />;
}

/** One PI sheet: edit a draft, release it, make a new version of a released one, or retire it (FR-25.16). */
export function PiSheetDesignerPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPISheet(id, signal), id);
  if (state.error && !state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="PI sheet" description={<Link to="/shopfloor/pi-sheets">All PI sheets</Link>} />
        <Alert tone="danger" title="Could not load the PI sheet" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <Card>
        <Skeleton />
      </Card>
    );
  }
  return <Designer key={`${state.data.id}:${state.data.rev}`} sheet={state.data} reload={state.reload} />;
}

function Designer({ sheet, reload }: { sheet: PISheet | null; reload(): void }) {
  const navigate = useNavigate();
  const canDesign = holdsDuty(usePrincipalDuties(), DUTY_PI_SHEET_MAINTAIN);
  const [code, setCode] = useState(sheet?.code ?? "");
  const [name, setName] = useState(sheet?.name ?? "");
  const [doc, setDoc] = useState<RecipeDocument>(sheet?.recipe ?? emptyDocument());
  const [saved, setSaved] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState(false);
  const rev = useRef(sheet?.rev ?? 0);
  const readOnly = !canDesign || (sheet !== null && sheet.status !== "draft");
  const problems = documentProblems(doc);

  async function run(work: () => Promise<string | void>) {
    setBusy(true);
    setFailure(null);
    setFields({});
    setSaved(null);
    try {
      const message = await work();
      if (message) setSaved(message);
    } catch (error) {
      const problem = describeProblem(error);
      setFields(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const save = () =>
    run(async () => {
      if (sheet === null) {
        const created = await createPISheet({ code: code.trim(), name: name.trim(), recipe: doc });
        navigate(`/shopfloor/pi-sheets/${created.id}`);
        return;
      }
      await updatePISheet(sheet.id, { name: name.trim(), rev: rev.current, recipe: doc });
      reload();
      return "Saved.";
    });

  const release = () =>
    run(async () => {
      if (!sheet) return;
      const draft = await updatePISheet(sheet.id, { name: name.trim(), rev: rev.current, recipe: doc });
      await releasePISheet(sheet.id, draft.rev);
      reload();
      return "Released. Control recipes can be made from this version now.";
    });

  return (
    <div className={styles.stack}>
      <PageHeader
        title={sheet ? `${sheet.code}, version ${sheet.version}` : "New PI sheet"}
        description={<Link to="/shopfloor/pi-sheets">All PI sheets</Link>}
      />
      {sheet ? (
        <p className={styles.note}>
          <PiStatusBadge status={sheet.status} />
          {sheet.status !== "draft" ? " A released sheet is never edited: make a new version to change it." : null}
        </p>
      ) : null}
      {saved ? <Alert tone="success" title={saved} live="status" /> : null}
      {failure ? (
        <Alert tone="danger" title="That did not work" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card>
        <div className={styles.form}>
          {sheet === null ? (
            <FormField id="pi-code" label="Code" hint="Upper-case letters, digits, '_', '.' or '-'. It stays the same for every version." error={fields.code}>
              {(control) => <Input {...control} value={code} maxLength={40} invalid={Boolean(fields.code)} onChange={(e) => setCode(e.target.value.toUpperCase())} />}
            </FormField>
          ) : null}
          <FormField id="pi-name" label="Name" error={fields.name}>
            {(control) => <Input {...control} value={name} maxLength={120} disabled={readOnly} invalid={Boolean(fields.name)} onChange={(e) => setName(e.target.value)} />}
          </FormField>
        </div>
      </Card>
      <PiSheetEditor doc={doc} onChange={setDoc} readOnly={readOnly} />
      {problems.length > 0 && !readOnly ? (
        <Alert tone="warning" title="Before this sheet can be released" live="status">
          <ul>
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <div className={styles.actions}>
        {canDesign && !readOnly ? (
          <>
            <Button variant="primary" loading={busy} onClick={() => void save()}>
              {sheet ? "Save draft" : "Create draft"}
            </Button>
            {sheet ? (
              <Button loading={busy} disabled={problems.length > 0} onClick={() => void release()}>
                Release
              </Button>
            ) : null}
          </>
        ) : null}
        {canDesign && sheet?.status === "released" ? (
          <>
            <Button
              loading={busy}
              onClick={() =>
                void run(async () => {
                  const next = await newPISheetVersion(sheet.id);
                  navigate(`/shopfloor/pi-sheets/${next.id}`);
                })
              }
            >
              New version
            </Button>
            <Button variant="danger" loading={busy} onClick={() => void run(async () => { await obsoletePISheet(sheet.id, sheet.rev); reload(); return "Retired. No more recipes can be made from it."; })}>
              Retire
            </Button>
          </>
        ) : null}
        <Button variant="ghost" onClick={() => setPreview((p) => !p)}>
          {preview ? "Hide preview" : "Preview on the kiosk"}
        </Button>
      </div>
      {preview ? (
        <Card title="Kiosk preview">
          <PiSheetRunner key={JSON.stringify(doc)} doc={doc} recipeId="preview" />
        </Card>
      ) : null}
    </div>
  );
}
