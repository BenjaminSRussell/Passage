import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { CODE, createJob, describeProblem, newKey, pdfToBase64, type DocumentType, type NewPrintJob, type Problem } from "./api/print";
import { DOCUMENT_LABELS } from "./format";
import styles from "./pages.module.css";
import type { Target } from "./NewPrintJobPage";
import { ProblemMessage } from "../../shell/errors";

/** Office documents that print on PDF over IPP devices (FR-32.13). */
export const PDF_DOCUMENT_TYPES = ["PICK_LIST", "COUNT_SHEET", "COA", "BOL"] as const satisfies readonly DocumentType[];
export type PdfDocumentType = (typeof PDF_DOCUMENT_TYPES)[number];

export interface DocumentValues {
  documentType: PdfDocumentType;
  target: Target;
  queueCode: string;
  zone: string;
  warehouseId: string;
  file: File | null;
}

type Field = "documentType" | "queueCode" | "zone" | "warehouseId" | "file";
type Errors = Partial<Record<Field, string>>;

/**
 * Checks the form and builds the request. The file is read as base64 only once the other
 * fields pass, and a file that is not a PDF or is over 2 MB is refused before any request.
 */
export async function buildDocumentJob(values: DocumentValues): Promise<{ errors: Errors; input?: NewPrintJob }> {
  const errors: Errors = {};
  const input: NewPrintJob = { format: "PDF" };
  if (values.target === "queue") {
    if (!CODE.test(values.queueCode.trim())) errors.queueCode = "Use the queue code, such as OFFICE.";
    input.queueCode = values.queueCode.trim();
  } else {
    input.documentType = values.documentType;
    if (values.zone.trim()) input.zone = values.zone.trim();
    if (values.warehouseId.trim()) input.warehouseId = values.warehouseId.trim();
  }
  if (!values.file) errors.file = "Choose the PDF to print.";
  if (Object.keys(errors).length > 0 || !values.file) return { errors };
  try {
    input.documentBase64 = await pdfToBase64(values.file);
  } catch (error) {
    return { errors: { file: error instanceof Error ? error.message : "The file could not be read." } };
  }
  return { errors, input };
}

/** Opens the PDF in a browser tab for the browser's own print dialog (BR-32.12 fallback). */
export function openForBrowserPrint(file: Blob): void {
  const url = URL.createObjectURL(file);
  window.open(url, "_blank", "noopener");
  // The new tab has loaded the document by the time a user prints; release the URL later.
  window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

const FIELD_FROM_SERVICE: Record<string, Field> = {
  documentBase64: "file",
  queueCode: "queueCode",
  documentType: "documentType",
  zone: "zone",
  warehouseId: "warehouseId",
};

/** Print a PDF office document on a PDF over IPP printer (FR-32.13), on a named queue or by routing rule. */
export function NewPrintDocumentPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<DocumentValues>({ documentType: "PICK_LIST", target: "queue", queueCode: "", zone: "", warehouseId: "", file: null });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DocumentValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setBusy(true);
    try {
      const { errors: found, input } = await buildDocumentJob(values);
      setErrors(found);
      if (!input) return;
      const job = await createJob(input, key.current);
      navigate(`/mobile-flows/print-jobs/${job.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const field = FIELD_FROM_SERVICE[name];
        if (field) fieldErrors[field] = message;
      }
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: "queueCode" | "zone" | "warehouseId", label: string, hint?: string, optional?: boolean) => (
    <FormField id={`doc-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => <Input {...control} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<DocumentValues>)} invalid={Boolean(errors[id])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Print document"
        description="Sends a PDF to the first PDF over IPP printer of the queue that answers; label printers in the queue are skipped. The job is recorded as printed or failed."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="doc-type" label="Document type" error={errors.documentType}>
            {(control) => (
              <Select {...control} value={values.documentType} onChange={(event) => update({ documentType: event.target.value as PdfDocumentType })}>
                {PDF_DOCUMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {DOCUMENT_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="doc-target" label="Printer selection">
            {(control) => (
              <Select {...control} value={values.target} onChange={(event) => update({ target: event.target.value as Target })}>
                <option value="queue">A named queue</option>
                <option value="rule">{`Routing rule for ${DOCUMENT_LABELS[values.documentType].toLowerCase()}`}</option>
              </Select>
            )}
          </FormField>
          {values.target === "queue"
            ? text("queueCode", "Queue")
            : [
                <div key="zone">{text("zone", "Zone", "Matches zone rules.", true)}</div>,
                <div key="warehouse">{text("warehouseId", "Warehouse", "Matches warehouse rules.", true)}</div>,
              ]}
          <FormField id="doc-file" label="PDF file" hint="A PDF of up to 2 MB." error={errors.file}>
            {(control) => (
              <Input
                {...control}
                type="file"
                accept="application/pdf,.pdf"
                onChange={(event) => update({ file: event.target.files?.[0] ?? null })}
                invalid={Boolean(errors.file)}
              />
            )}
          </FormField>
          {failure && failure.code === "MOB-0112" && values.target === "rule" && values.file ? (
            <div className={styles.formActions}>
              <Alert
                tone="warning"
                title="No printer is routed for this document"
                live="alert"
                action={
                  <Button size="sm" onClick={() => values.file && openForBrowserPrint(values.file)}>
                    Print from the browser
                  </Button>
                }
              >
                <ProblemMessage problem={failure} /> Open the PDF in a new tab and print it with the browser instead.
              </Alert>
            </div>
          ) : failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not print the document" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Print document
            </Button>
            <Link className={buttonClassName("ghost")} to="/mobile-flows/print-jobs">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
