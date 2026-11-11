import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { get, post, toBase64 } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";

interface Invoice { id: string; invoiceNumber: string; invoiceDate: string; currency: string; total: string; status: string }
interface Submission { id: string; status: string; invoiceNumber: string; total: string; reviewReasons: string[] }

const OCR_TYPES = ["application/pdf", "image/png", "image/jpeg", "image/tiff"];

/** An invoice document is XML (UBL) or a PDF or image (read by OCR). Anything else is refused before it leaves the browser. */
export function formatOf(file: { name: string; type: string }): { format: "UBL" | "OCR"; mediaType: string } | undefined {
  if (/\.xml$/i.test(file.name) || file.type === "application/xml" || file.type === "text/xml") return { format: "UBL", mediaType: "application/xml" };
  if (OCR_TYPES.includes(file.type)) return { format: "OCR", mediaType: file.type };
  return undefined;
}

export function Invoices() {
  const posted = useLoad(() => get<{ items: Invoice[] }>("/api/v1/invoices?limit=100"), []);
  const waiting = useLoad(() => get<{ items: Submission[] }>("/api/v1/invoice-documents?limit=100"), []);
  return (
    <Page title="Invoices" action={<Link className="button" to="/invoices/received">Invoice received goods</Link>}>
      <SubmitInvoice onSent={waiting.reload} />
      <h2>Waiting for review</h2>
      <State load={waiting} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Invoice documents you sent that are not yet posted</caption>
            <thead><tr><th scope="col">Invoice</th><th scope="col">Total</th><th scope="col">Status</th><th scope="col">Notes</th></tr></thead>
            <tbody>{d.items.map((s) => <tr key={s.id}><th scope="row">{s.invoiceNumber}</th><td>{s.total}</td><td><Badge value={s.status} /></td><td>{s.reviewReasons.join(", ")}</td></tr>)}</tbody>
          </table>
        )}
      </State>
      <h2>Posted invoices</h2>
      <State load={posted} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your posted invoices</caption>
            <thead><tr><th scope="col">Invoice</th><th scope="col">Date</th><th scope="col">Total</th><th scope="col">Status</th></tr></thead>
            <tbody>{d.items.map((i) => <tr key={i.id}><th scope="row">{i.invoiceNumber}</th><td>{day(i.invoiceDate)}</td><td>{i.total} {i.currency}</td><td><Badge value={i.status} /></td></tr>)}</tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

function SubmitInvoice({ onSent }: { onSent: () => void }) {
  const [file, setFile] = useState<File>();
  const [error, setError] = useState("");
  const [sent, setSent] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setSent("");
    if (!file) return setError("Choose the invoice file.");
    const kind = formatOf(file);
    if (!kind) return setError("Send a UBL XML file, or a PDF, PNG, JPEG or TIFF.");
    try {
      const s = await post<Submission>("/api/v1/invoice-documents", { format: kind.format, mediaType: kind.mediaType, content: await toBase64(file) });
      setSent(`Received invoice ${s.invoiceNumber}. ${s.status === "NEEDS_REVIEW" ? "It needs a check before it is posted." : "It will be posted after review."}`);
      onSent();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <form onSubmit={submit} noValidate>
      <h2>Send an invoice</h2>
      <Field label="Invoice file" hint="UBL XML, PDF, PNG, JPEG or TIFF. Up to 4 MB.">
        {(id) => <input id={id} type="file" accept=".xml,application/pdf,image/png,image/jpeg,image/tiff" aria-describedby={`${id}-hint`} onChange={(e) => setFile(e.target.files?.[0])} />}
      </Field>
      {error ? <Notice kind="error">{error}</Notice> : null}
      {sent ? <Notice kind="ok">{sent}</Notice> : null}
      <button type="submit">Send invoice</button>
    </form>
  );
}
