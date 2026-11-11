import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { DPIS, describeProblem, listPrinters, mediaFor, mediaLabel, updatePrinter, type MediaSize, type Printer, type PrinterEdit, type Problem } from "./api/print";
import { ProblemMessage } from "../../shell/errors";
import { validatePrinter, type PrinterValues } from "./NewPrinterPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Edit a printer or move it to another queue or failover rank (FR-32.13). The code and device type stay. */
export function EditPrinterPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => listPrinters(signal), `printer-${id}`);
  const printer: Printer | undefined = state.data?.find((p) => p.id === id);
  const [values, setValues] = useState<PrinterValues | null>(null);
  const [errors, setErrors] = useState<Partial<Record<keyof PrinterValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (printer && !values) {
      setValues({
        code: printer.code,
        name: printer.name,
        deviceType: printer.deviceType,
        dpi: String(printer.dpi),
        mediaSize: printer.mediaSize,
        host: printer.host,
        port: String(printer.port),
        queueCode: printer.queueCode,
        failoverRank: String(printer.failoverRank),
      });
    }
  }, [printer, values]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!values || !printer) return;
    const { errors: found, input } = validatePrinter(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    const edit: PrinterEdit = {
      name: input.name,
      dpi: input.dpi,
      mediaSize: input.mediaSize,
      host: input.host,
      port: input.port ?? printer.port,
      queueCode: input.queueCode,
      failoverRank: input.failoverRank,
    };
    setBusy(true);
    try {
      await updatePrinter(printer.id, edit);
      navigate("/mobile-flows/printers");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "PRN-0006") setErrors({ failoverRank: "Another printer in this queue already has this failover rank." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  if (state.loading && !state.data) return <p>Loading printer…</p>;
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load the printer" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  if (!printer || !values) {
    return (
      <Alert tone="danger" title="Printer not found" live="alert">
        It may have been retired. <Link to="/mobile-flows/printers">Back to printers</Link>
      </Alert>
    );
  }

  const set = (patch: Partial<PrinterValues>) => setValues({ ...values, ...patch });
  const text = (field: "name" | "host" | "port" | "queueCode" | "failoverRank", label: string, hint?: string) => (
    <FormField id={`printer-${field}`} label={label} hint={hint} error={errors[field]}>
      {(control) => <Input {...control} value={values[field]} onChange={(e) => set({ [field]: e.target.value })} invalid={Boolean(errors[field])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Edit ${printer.code}`}
        description="Change the printer's details, or move it to another queue or failover rank. Its code and device type cannot change; register a new printer for that."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("name", "Name")}
          <FormField id="printer-dpi" label="Resolution" error={errors.dpi}>
            {(control) => (
              <Select {...control} value={values.dpi} onChange={(e) => set({ dpi: e.target.value })}>
                {DPIS.map((d) => (
                  <option key={d} value={String(d)}>
                    {d} dpi
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="printer-mediaSize" label="Media size" error={errors.mediaSize}>
            {(control) => (
              <Select {...control} value={values.mediaSize} onChange={(e) => set({ mediaSize: e.target.value as MediaSize })}>
                {mediaFor(values.deviceType).map((m) => (
                  <option key={m} value={m}>
                    {mediaLabel(m)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("host", "Host", "Host name or IP address on the site network.")}
          {text("port", "Port")}
          {text("queueCode", "Queue", "Moving the printer to another queue takes it out of this one.")}
          {text("failoverRank", "Failover rank", "1 is tried first; unique within the queue.")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the printer" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save printer
            </Button>
            <Link className={buttonClassName("ghost")} to="/mobile-flows/printers">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
