import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { get, post } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";

export interface Question { id: string; label: string; type: "TEXT" | "YESNO" | "CHOICE"; required: boolean; options?: string[] }
export interface VendorRequest { id: string; kind: string; status: string; subject: string; details: string; questions?: Question[]; answers: Record<string, string>; changes?: Record<string, string>; createdAt: string; decisionNote?: string; appliedAt?: string }

/** The company details a supplier can ask to change; approving the request writes them to the buyer's record. */
export const PROFILE_FIELDS: Array<[string, string]> = [["legalName", "Legal name"], ["tradeName", "Trade name"], ["registrationNumber", "Company registration number"], ["industryCode", "Industry code"]];

export function Requests() {
  const load = useLoad(() => get<{ items: VendorRequest[] }>("/api/v1/requests?limit=100"), []);
  const [error, setError] = useState("");
  const [subject, setSubject] = useState("");
  const [details, setDetails] = useState("");
  const [kind, setKind] = useState("PROFILE_CHANGE");
  const [changes, setChanges] = useState<Record<string, string>>({});

  async function create(e: FormEvent) {
    e.preventDefault();
    setError("");
    const wanted = Object.fromEntries(Object.entries(changes).filter(([, v]) => v.trim() !== "").map(([k, v]) => [k, v.trim()]));
    if (kind === "PROFILE_CHANGE" && Object.keys(wanted).length === 0) return setError("Fill in at least one company detail you want to change.");
    try {
      await post("/api/v1/requests", { kind, subject, details, ...(kind === "PROFILE_CHANGE" ? { changes: wanted } : {}) });
      setSubject("");
      setDetails("");
      setChanges({});
      load.reload();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  const open = (load.data?.items ?? []).filter((r) => r.kind === "ONBOARDING" && r.status === "INVITED");
  return (
    <Page title="Requests">
      {open.length > 0 ? <Notice kind="ok">You have a questionnaire to complete: <Link to={`/requests/${open[0]?.id}`}>open it</Link>.</Notice> : null}
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your requests</caption>
            <thead><tr><th scope="col">Request</th><th scope="col">Type</th><th scope="col">Sent</th><th scope="col">Status</th></tr></thead>
            <tbody>{d.items.map((r) => <tr key={r.id}><th scope="row"><Link to={`/requests/${r.id}`}>{r.subject || "Onboarding questionnaire"}</Link></th><td>{r.kind.replace("_", " ").toLowerCase()}</td><td>{day(r.createdAt)}</td><td><Badge value={r.status} /></td></tr>)}</tbody>
          </table>
        )}
      </State>
      <form onSubmit={create} noValidate>
        <h2>Ask your buyer for a change</h2>
        <Field label="Type">{(id) => <select id={id} value={kind} onChange={(e) => setKind(e.target.value)}><option value="PROFILE_CHANGE">Change to my company details</option><option value="OTHER">Something else</option></select>}</Field>
        {kind === "PROFILE_CHANGE" ? (
          <fieldset>
            <legend>New company details</legend>
            <p>Fill in only what changes. When your buyer approves, these replace what they have on record.</p>
            {PROFILE_FIELDS.map(([key, label]) => <Field key={key} label={label}>{(id) => <input id={id} value={changes[key] ?? ""} maxLength={200} onChange={(e) => setChanges((c) => ({ ...c, [key]: e.target.value }))} />}</Field>)}
          </fieldset>
        ) : null}
        <Field label="Subject">{(id) => <input id={id} value={subject} maxLength={200} onChange={(e) => setSubject(e.target.value)} />}</Field>
        <Field label="Details">{(id) => <textarea id={id} value={details} maxLength={4000} onChange={(e) => setDetails(e.target.value)} />}</Field>
        {error ? <Notice kind="error">{error}</Notice> : null}
        <button type="submit">Send request</button>
      </form>
    </Page>
  );
}

export function RequestDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<VendorRequest>(`/api/v1/requests/${id}`), [id]);
  return (
    <Page title="Request">
      <State load={load}>
        {(r) => (
          <>
            <p><strong>{r.subject || "Onboarding questionnaire"}</strong> · <Badge value={r.status} /></p>
            {r.details ? <p>{r.details}</p> : null}
            {r.changes && Object.keys(r.changes).length > 0 ? (
              <>
                <h2>Details you asked to change</h2>
                <ul>{PROFILE_FIELDS.filter(([k]) => r.changes?.[k] !== undefined).map(([k, label]) => <li key={k}>{label}: {r.changes?.[k]}</li>)}</ul>
                {r.appliedAt ? <Notice kind="ok">Your buyer approved this and updated your company details on {day(r.appliedAt)}.</Notice> : null}
              </>
            ) : null}
            {r.decisionNote ? <p>Buyer's note: {r.decisionNote}</p> : null}
            {r.kind === "ONBOARDING" ? <Questionnaire request={r} onSent={load.reload} /> : null}
            {r.status === "SUBMITTED" || r.status === "INVITED" ? <Withdraw id={r.id} onDone={load.reload} /> : null}
          </>
        )}
      </State>
    </Page>
  );
}

function Withdraw({ id, onDone }: { id: string; onDone: () => void }) {
  const [error, setError] = useState("");
  return (
    <>
      {error ? <Notice kind="error">{error}</Notice> : null}
      <button type="button" className="secondary" onClick={() => post(`/api/v1/requests/${id}/withdraw`).then(onDone, (e: unknown) => setError(messageOf(e)))}>Withdraw request</button>
    </>
  );
}

/** The onboarding questionnaire. Required questions are checked here; the service checks them again. */
export function Questionnaire({ request, onSent }: { request: VendorRequest; onSent: () => void }) {
  const editable = request.status === "INVITED";
  const [answers, setAnswers] = useState<Record<string, string>>(request.answers ?? {});
  const [missing, setMissing] = useState<string[]>([]);
  const [error, setError] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    const gaps = (request.questions ?? []).filter((q) => q.required && !(answers[q.id] ?? "").trim()).map((q) => q.id);
    setMissing(gaps);
    if (gaps.length > 0) return setError("Answer the questions marked required.");
    setError("");
    try {
      await post(`/api/v1/requests/${request.id}/submit`, { answers });
      onSent();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <form onSubmit={submit} noValidate>
      {(request.questions ?? []).map((q) => (
        <Field key={q.id} label={`${q.label}${q.required ? " (required)" : ""}`}>
          {(fid) => {
            const common = { id: fid, disabled: !editable, "aria-invalid": missing.includes(q.id) || undefined, value: answers[q.id] ?? "", onChange: (e: { target: { value: string } }) => setAnswers((a) => ({ ...a, [q.id]: e.target.value })) };
            if (q.type === "TEXT") return <textarea {...common} maxLength={2000} />;
            const options = q.type === "YESNO" ? ["YES", "NO"] : (q.options ?? []);
            return <select {...common}><option value="">Choose</option>{options.map((o) => <option key={o} value={o}>{o}</option>)}</select>;
          }}
        </Field>
      ))}
      {error ? <Notice kind="error">{error}</Notice> : null}
      {editable ? <button type="submit">Send answers</button> : null}
    </form>
  );
}
