import { useState, type FormEvent } from "react";
import { download, get, post, toBase64 } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";

interface Doc { id: string; direction: string; title: string; fileName: string; contentType: string; sizeBytes: number; at: string }
interface Distribution { id: string; docNo: string; revision: number; title: string; fileName: string; requiresAck: boolean; sentAt: string; acknowledgedAt?: string }

export const MAX_BYTES = 5 * 1024 * 1024;
const TYPES = ["application/pdf", "image/png", "image/jpeg", "text/plain", "text/csv", "application/zip"];

export function Documents() {
  const docs = useLoad(() => get<{ items: Doc[] }>("/api/v1/documents?limit=100"), []);
  const dist = useLoad(() => get<{ items: Distribution[] }>("/api/v1/distributions"), []);
  const [error, setError] = useState("");
  return (
    <Page title="Documents">
      {error ? <Notice kind="error">{error}</Notice> : null}
      <h2>From your buyer</h2>
      <State load={dist} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Controlled documents sent to you</caption>
            <thead><tr><th scope="col">Document</th><th scope="col">Revision</th><th scope="col">Sent</th><th scope="col">Acknowledged</th><th scope="col">Actions</th></tr></thead>
            <tbody>
              {d.items.map((x) => (
                <tr key={x.id}>
                  <th scope="row">{x.docNo} {x.title}</th><td>{x.revision}</td><td>{day(x.sentAt)}</td>
                  <td>{x.acknowledgedAt ? day(x.acknowledgedAt) : x.requiresAck ? <Badge value="WAITING" /> : "Not needed"}</td>
                  <td>
                    <button type="button" className="secondary" onClick={() => download(`/api/v1/distributions/${x.id}/content`, x.fileName).catch((e: unknown) => setError(messageOf(e)))}>Download {x.docNo}</button>
                    {x.requiresAck && !x.acknowledgedAt ? <button type="button" onClick={() => post(`/api/v1/distributions/${x.id}/acknowledge`).then(dist.reload, (e: unknown) => setError(messageOf(e)))}>Acknowledge {x.docNo}</button> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
      <h2>Exchanged with your buyer</h2>
      <State load={docs} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Documents exchanged with your buyer</caption>
            <thead><tr><th scope="col">Title</th><th scope="col">Direction</th><th scope="col">Date</th><th scope="col">Size</th><th scope="col">Download</th></tr></thead>
            <tbody>
              {d.items.map((x) => (
                <tr key={x.id}>
                  <th scope="row">{x.title}</th><td>{x.direction === "FROM_VENDOR" ? "Sent by you" : "Sent to you"}</td><td>{day(x.at)}</td><td>{Math.ceil(x.sizeBytes / 1024)} KB</td>
                  <td><button type="button" className="secondary" onClick={() => download(`/api/v1/documents/${x.id}/content`, x.fileName).catch((e: unknown) => setError(messageOf(e)))}>Download {x.title}</button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
      <Upload onSent={docs.reload} />
    </Page>
  );
}

function Upload({ onSent }: { onSent: () => void }) {
  const [title, setTitle] = useState("");
  const [file, setFile] = useState<File>();
  const [error, setError] = useState("");
  const [done, setDone] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setDone(false);
    if (!title.trim()) return setError("Give the document a title.");
    if (!file) return setError("Choose a file.");
    if (!TYPES.includes(file.type)) return setError("Send a PDF, PNG, JPEG, text, CSV or ZIP file.");
    if (file.size > MAX_BYTES) return setError("The file is larger than 5 MB.");
    setError("");
    try {
      await post("/api/v1/documents", { title: title.trim(), fileName: file.name, contentType: file.type, contentBase64: await toBase64(file) });
      setDone(true);
      setTitle("");
      onSent();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <form onSubmit={submit} noValidate>
      <h2>Send a document</h2>
      <Field label="Title">{(id) => <input id={id} value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />}</Field>
      <Field label="File" hint="PDF, PNG, JPEG, text, CSV or ZIP. Up to 5 MB. Files are checked for viruses and refused if one is found.">{(id) => <input id={id} type="file" aria-describedby={`${id}-hint`} onChange={(e) => setFile(e.target.files?.[0])} />}</Field>
      {error ? <Notice kind="error">{error}</Notice> : null}
      {done ? <Notice kind="ok">Your document was sent.</Notice> : null}
      <button type="submit">Send document</button>
    </form>
  );
}
