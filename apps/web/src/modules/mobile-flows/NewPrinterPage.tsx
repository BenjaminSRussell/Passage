import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  CODE,
  createPrinter,
  describeProblem,
  DEVICE_TYPES,
  DPIS,
  mediaFor,
  mediaLabel,
  newKey,
  printsOn,
  type DeviceType,
  type MediaSize,
  type NewPrinter,
  type Problem,
} from "./api/print";
import styles from "./pages.module.css";

export interface PrinterValues {
  code: string;
  name: string;
  deviceType: DeviceType;
  dpi: string;
  /** Label media for ZPL II, sheet media for PDF and PCL 6 (FR-32.13). */
  mediaSize: MediaSize;
  host: string;
  /** Empty means the service default: 9100 for ZPL II and PCL 6, 631 for PDF over IPP. */
  port: string;
  queueCode: string;
  failoverRank: string;
}

type Field = keyof PrinterValues;
type Errors = Partial<Record<Field, string>>;

const WHOLE = /^\d{1,5}$/;

/** Checks that mirror the service, and the request the form sends. */
export function validatePrinter(values: PrinterValues): { errors: Errors; input?: NewPrinter } {
  const errors: Errors = {};
  const code = values.code.trim();
  const queue = values.queueCode.trim();
  const host = values.host.trim();
  const port = values.port.trim();
  if (!CODE.test(code)) errors.code = "Use 2 to 40 capital letters, digits, _ or -, such as DOCK-1.";
  if (values.name.trim() === "" || values.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!DPIS.includes(Number(values.dpi) as (typeof DPIS)[number])) errors.dpi = "Choose 203, 300 or 600 dpi.";
  if (!mediaFor(values.deviceType).includes(values.mediaSize)) {
    errors.mediaSize = values.deviceType === "ZPL_II" ? "Choose a label size for a ZPL II printer." : "Choose A4 or Letter for this printer.";
  }
  if (host === "" || host.length > 253) errors.host = "Enter the printer's host name or IP address.";
  if (port !== "" && (!WHOLE.test(port) || Number(port) < 1 || Number(port) > 65535)) errors.port = "Enter a port from 1 to 65535.";
  if (!CODE.test(queue)) errors.queueCode = "Use 2 to 40 capital letters, digits, _ or -, such as DOCK.";
  if (!WHOLE.test(values.failoverRank.trim()) || Number(values.failoverRank) < 1) errors.failoverRank = "Enter a whole number of 1 or more.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewPrinter = {
    code,
    name: values.name.trim(),
    deviceType: values.deviceType,
    dpi: Number(values.dpi),
    mediaSize: values.mediaSize,
    host,
    queueCode: queue,
    failoverRank: Number(values.failoverRank),
  };
  if (port !== "") input.port = Number(port);
  return { errors, input };
}

/** Register a printer in a queue (FR-32.13). */
export function NewPrinterPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<PrinterValues>({
    code: "",
    name: "",
    deviceType: "ZPL_II",
    dpi: "203",
    mediaSize: "4X6_IN",
    host: "",
    port: "",
    queueCode: "",
    failoverRank: "1",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PrinterValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePrinter(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createPrinter(input, key.current);
      navigate("/mobile-flows/printers");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of Object.keys(values) as Field[]) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "PRN-0003") fieldErrors.code = "A printer with this code is already registered.";
      if (problem.code === "PRN-0006") fieldErrors.failoverRank = "Another printer in this queue already has this failover rank.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Exclude<Field, "deviceType" | "dpi" | "mediaSize">, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`printer-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(event) => update({ [id]: event.target.value } as Partial<PrinterValues>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Register printer"
        description="The print service on the site network opens the connection to the printer; nothing connects inbound to the cluster."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("code", "Printer code", "Unique per tenant, e.g. DOCK-1.", false, { maxLength: 40 })}
          {text("name", "Name", undefined, false, { maxLength: 120 })}
          <FormField id="printer-deviceType" label="Device type" hint="Labels print on ZPL II devices; PDF and PCL 6 devices take office documents.">
            {(control) => (
              <Select
                {...control}
                value={values.deviceType}
                onChange={(event) => {
                  const deviceType = event.target.value as DeviceType;
                  // A device type takes only its own kind of media, so switching resets it to the type's default.
                  update({ deviceType, mediaSize: mediaFor(deviceType)[0] });
                }}
              >
                {DEVICE_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t === "ZPL_II" ? "ZPL II" : t === "PDF_IPP" ? "PDF over IPP" : "PCL 6"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="printer-dpi" label="Resolution" error={errors.dpi}>
            {(control) => (
              <Select {...control} value={values.dpi} onChange={(event) => update({ dpi: event.target.value })} invalid={Boolean(errors.dpi)}>
                {DPIS.map((d) => (
                  <option key={d} value={String(d)}>
                    {d} dpi
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField
            id="printer-mediaSize"
            label="Media size"
            hint={`The label or sheet size loaded in the printer. Prints: ${printsOn(values.deviceType, values.mediaSize)}.`}
            error={errors.mediaSize}
          >
            {(control) => (
              <Select
                {...control}
                value={values.mediaSize}
                onChange={(event) => update({ mediaSize: event.target.value as MediaSize })}
                invalid={Boolean(errors.mediaSize)}
              >
                {mediaFor(values.deviceType).map((m) => (
                  <option key={m} value={m}>
                    {mediaLabel(m)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("host", "Host", "Host name or IP address on the site network.", false, { maxLength: 253 })}
          {text("port", "Port", values.deviceType === "PDF_IPP" ? "Empty means IPP port 631." : "Empty means raw TCP port 9100.", true, { inputMode: "numeric" })}
          {text("queueCode", "Queue", "Printers that share a queue back each other up.", false, { maxLength: 40 })}
          {text("failoverRank", "Failover rank", "1 is tried first; unique within the queue.", false, { inputMode: "numeric" })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the printer" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register printer
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
