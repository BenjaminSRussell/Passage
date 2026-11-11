import { useEffect, useMemo, useState } from "react";
import { canCapture, completionBlockers, MAX_PHOTOS_PER_ACTION, MOVE_LABEL, newAction, newId, nextMoves, STATUS_LABEL } from "./domain/actions";
import { currentPosition, navigateUrl } from "./domain/geo";
import { preparePhoto, toBase64 } from "./domain/image";
import { customerSummary } from "./domain/summary";
import type { DayVisit } from "./domain/types";
import { SignaturePad } from "./SignaturePad";
import { browserDeps, useTechnician, type TechnicianDeps } from "./useTechnician";
import "./technicianApp.css";

export interface TechnicianAppProps {
  /** Supplied by tests; the browser's storage and the real API otherwise. */
  deps?: TechnicianDeps;
  /** A local date (YYYY-MM-DD); today in the technician's time zone by default. */
  date?: string;
}

function clock(iso: string, zone: string): string {
  return new Intl.DateTimeFormat("en-US", { hour: "numeric", minute: "2-digit", timeZone: zone }).format(new Date(iso));
}

/**
 * The technician's PWA (FR-45.8): today's route, navigation, start travel and work, checklist, parts from
 * truck stock, photos, signature, a summary for the customer, and completion. Everything the technician does is
 * kept on the device first and sent when there is a connection.
 */
export function TechnicianApp({ deps: given, date }: TechnicianAppProps) {
  const [deps, setDeps] = useState<TechnicianDeps | null>(given ?? null);
  const [failure, setFailure] = useState<string | null>(null);
  useEffect(() => {
    if (given) return;
    browserDeps().then(setDeps, () => setFailure("This device cannot keep work for later (no secure storage). Open the app in a current browser."));
  }, [given]);
  const t = useTechnician(deps, date);
  const [openId, setOpenId] = useState<string | null>(null);
  const open = t.visits.find((v) => v.id === openId) ?? null;
  const zone = t.snapshot?.day.technician.timeZone ?? "UTC";
  const names = t.snapshot?.names ?? {};

  if (failure) return <main className="tech-app"><p role="alert">{failure}</p></main>;
  return (
    <main className="tech-app">
      <header className="tech-header">
        <div>
          <h1>{open ? open.number : "My day"}</h1>
          <p className="tech-sub">{t.snapshot ? `${t.snapshot.day.technician.name}, ${t.snapshot.day.date}` : "Loading"}</p>
        </div>
        <div className="tech-status" role="status" aria-live="polite">
          <span className={t.online ? "tech-pill tech-pill--on" : "tech-pill tech-pill--off"}>{t.online ? "Online" : "Offline"}</span>
          <span className="tech-pill">{t.waiting === 0 ? "Everything sent" : `${t.waiting} waiting to send`}</span>
          <button type="button" onClick={() => void t.syncNow()} disabled={t.busy || t.waiting === 0}>
            {t.busy ? "Sending" : "Send now"}
          </button>
        </div>
      </header>

      {t.attention.length > 0 && (
        <section aria-label="Needs your attention" className="tech-attention">
          <h2>Needs your attention</h2>
          <ul>
            {t.attention.map((a, i) => (
              <li key={`${a.at}-${i}`} role="alert">
                <span>{a.reason}</span>
                <button type="button" onClick={() => void t.dismiss(i)}>
                  Dismiss
                </button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {t.problem && <p role="alert">{t.problem}</p>}
      {!t.snapshot && !t.problem && <p aria-busy="true">Loading your day</p>}

      {t.snapshot && !open && (
        <section aria-label="Route">
          {t.visits.length === 0 ? (
            <p>No jobs are published for you today.</p>
          ) : (
            <ol className="tech-route">
              {t.visits.map((v) => (
                <li key={v.id}>
                  <button type="button" className="tech-card" onClick={() => setOpenId(v.id)}>
                    <span className="tech-card__time">{clock(v.assignedStart, zone)}</span>
                    <span className="tech-card__main">
                      <strong>{names[v.customerPartyId] ?? "Customer"}</strong>
                      <span>{v.address || "No address"}</span>
                      <span>{v.caseDescription ?? ""}</span>
                    </span>
                    <span className="tech-pill">{STATUS_LABEL[v.status] ?? v.status}</span>
                  </button>
                </li>
              ))}
            </ol>
          )}
          <p className="tech-sub">
            {t.snapshot.day.totals.completed} of {t.snapshot.day.totals.visits} jobs done. Last updated {clock(t.snapshot.savedAt, zone)}.
          </p>
        </section>
      )}

      {t.snapshot && open && (
        <Visit
          visit={open}
          customer={names[open.customerPartyId] ?? "Customer"}
          zone={zone}
          products={t.snapshot.products}
          truck={t.snapshot.truck}
          technicianName={t.snapshot.day.technician.name}
          onBack={() => setOpenId(null)}
          act={t.act}
          capture={t.capture}
          now={deps?.now ?? (() => new Date())}
        />
      )}
    </main>
  );
}

interface VisitProps {
  visit: DayVisit;
  customer: string;
  zone: string;
  products: Record<string, string>;
  truck: { productId: string; onHandQty: string; unit: string; short: boolean }[];
  technicianName: string;
  onBack: () => void;
  act: ReturnType<typeof useTechnician>["act"];
  capture: ReturnType<typeof useTechnician>["capture"];
  now: () => Date;
}

function Visit({ visit: v, customer, zone, products, truck, onBack, act, capture, now }: VisitProps) {
  const [message, setMessage] = useState("");
  const [signer, setSigner] = useState("");
  const [note, setNote] = useState(v.technicianNote);
  const [used, setUsed] = useState<Record<string, string>>({});
  const nav = navigateUrl(v);
  const blockers = useMemo(() => completionBlockers(v), [v]);
  const summary = customerSummary(v, customer);
  const capturing = canCapture(v.status);
  const working = v.status === "IN_PROGRESS" || v.status === "ON_HOLD";

  async function run(f: () => Promise<void>) {
    setMessage("");
    try {
      await f();
    } catch (e) {
      setMessage(e instanceof Error ? e.message : "That did not work.");
    }
  }

  const photo = (file: File | undefined) =>
    run(async () => {
      if (!file) return;
      if (v.photos.length >= MAX_PHOTOS_PER_ACTION * 4) throw new Error("This job already has plenty of photos.");
      const img = await preparePhoto(file);
      const id = newId();
      const pos = await currentPosition(1500);
      await capture(v, { captureId: id, kind: "PHOTO", contentType: img.contentType, data: img.data, capturedAt: now().toISOString(), ...pos }, newAction("PHOTO", now(), { captureIds: [id] }));
    });

  const sign = (png: Blob) =>
    run(async () => {
      if (signer.trim() === "") throw new Error("Type the name of the person who is signing first.");
      const id = newId();
      const pos = await currentPosition();
      const data = toBase64(new Uint8Array(await png.arrayBuffer()));
      await capture(
        v,
        { captureId: id, kind: "SIGNATURE", contentType: "image/png", data, capturedAt: now().toISOString(), ...pos },
        newAction("SIGNATURE", now(), { signedBy: signer.trim(), signatureRef: `capture:${id}`, ...pos }),
      );
    });

  return (
    <section aria-label="Job">
      <button type="button" onClick={onBack}>
        Back to my day
      </button>
      <h2>{customer}</h2>
      <p>
        <span className="tech-pill">{STATUS_LABEL[v.status] ?? v.status}</span> {clock(v.assignedStart, zone)} to {clock(v.assignedEnd, zone)}
        {v.windowStart && v.windowEnd ? `, customer expects you ${clock(v.windowStart, zone)} to ${clock(v.windowEnd, zone)}` : ""}
      </p>
      {v.address && <p>{v.address}</p>}
      {nav && (
        <p>
          <a className="tech-link" href={nav} target="_blank" rel="noreferrer">
            Navigate
          </a>
        </p>
      )}
      {v.caseDescription && <p>{v.caseDescription}</p>}
      {v.serviceObjectLabel && <p>Equipment: {v.serviceObjectLabel}</p>}

      <div className="tech-row" aria-label="Job actions">
        {nextMoves(v.status).map((k) => (
          <button key={k} type="button" className={k === "COMPLETE" ? "tech-primary" : ""} disabled={k === "COMPLETE" && blockers.length > 0} onClick={() => void run(() => act(v, newAction(k, now())))}>
            {MOVE_LABEL[k]}
          </button>
        ))}
      </div>
      {v.status === "IN_PROGRESS" && blockers.length > 0 && (
        <div className="tech-blockers">
          <p>Before you complete the job:</p>
          <ul>
            {blockers.map((b) => (
              <li key={b}>{b}</li>
            ))}
          </ul>
        </div>
      )}
      {message && <p role="alert">{message}</p>}

      {v.checklist.length > 0 && (
        <fieldset disabled={!working}>
          <legend>Checklist</legend>
          {v.checklist.map((c) => (
            <label key={c.id} className="tech-check">
              <input type="checkbox" checked={c.done} onChange={(e) => void run(() => act(v, newAction("CHECKLIST", now(), { itemId: c.id, done: e.target.checked })))} />
              <span>
                {c.text}
                {c.required ? " (required)" : ""}
              </span>
            </label>
          ))}
          {!working && <p className="tech-sub">Start work to tick items.</p>}
        </fieldset>
      )}

      {v.lines.some((l) => l.kind === "PART") && (
        <fieldset disabled={!working}>
          <legend>Parts from your truck</legend>
          {v.lines
            .filter((l) => l.kind === "PART")
            .map((l) => {
              const left = Math.max(0, Number(l.qty) - Number(l.consumedQty));
              const held = l.productId ? truck.find((t) => t.productId === l.productId) : undefined;
              const label = l.description ?? (l.productId ? products[l.productId] : undefined) ?? "Part";
              return (
                <div key={l.id} className="tech-part">
                  <p>
                    <strong>{label}</strong>: {l.consumedQty} of {l.qty} {l.unit ?? ""} used
                    {held ? `. On your truck: ${held.onHandQty}${held.short ? " (running low)" : ""}` : ""}
                  </p>
                  {left > 0 && (
                    <div className="tech-row">
                      <label>
                        Quantity used
                        <input inputMode="decimal" value={used[l.id] ?? String(left)} onChange={(e) => setUsed({ ...used, [l.id]: e.target.value })} />
                      </label>
                      <button
                        type="button"
                        onClick={() =>
                          void run(async () => {
                            const qty = Number(used[l.id] ?? left);
                            if (!(qty > 0) || qty > left) throw new Error(`Enter a quantity between 0 and ${left}.`);
                            await act(v, newAction("PART_USED", now(), { lineId: l.id, qty: String(qty) }));
                            setUsed({ ...used, [l.id]: "" });
                          })
                        }
                      >
                        Record
                      </button>
                    </div>
                  )}
                </div>
              );
            })}
        </fieldset>
      )}

      <fieldset disabled={!capturing && v.status !== "COMPLETED"}>
        <legend>Photos ({v.photos.length})</legend>
        <label className="tech-file">
          Take or add a photo
          <input type="file" accept="image/*" capture="environment" onChange={(e) => void photo(e.target.files?.[0])} />
        </label>
      </fieldset>

      <fieldset disabled={!capturing}>
        <legend>Note</legend>
        <label>
          Note for the office
          <textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <button type="button" onClick={() => void run(() => act(v, newAction("NOTE", now(), { text: note })))} disabled={note === v.technicianNote}>
          Save note
        </button>
      </fieldset>

      <fieldset disabled={!capturing && !working}>
        <legend>Customer signature</legend>
        {v.signature.signed ? (
          <p>Signed by {v.signature.signedBy ?? "the customer"}.</p>
        ) : v.signature.exempt ? (
          <p>This job needs no signature.</p>
        ) : (
          <>
            <label>
              Name of the person signing
              <input value={signer} onChange={(e) => setSigner(e.target.value)} autoComplete="off" />
            </label>
            <SignaturePad onSave={(png) => void sign(png)} />
          </>
        )}
      </fieldset>

      <section aria-label="Summary for the customer" className="tech-summary">
        <h3>{summary.heading}</h3>
        <dl>
          {summary.rows.map((r) => (
            <div key={r.label}>
              <dt>{r.label}</dt>
              <dd>{r.value}</dd>
            </div>
          ))}
        </dl>
        {summary.parts.length > 0 && (
          <>
            <h4>Parts used</h4>
            <ul>
              {summary.parts.map((p) => (
                <li key={p.text}>{p.text}</li>
              ))}
            </ul>
          </>
        )}
        {summary.checklist.length > 0 && (
          <>
            <h4>Work checked</h4>
            <ul>
              {summary.checklist.map((c) => (
                <li key={c.text}>
                  {c.done ? "Done" : "Not done"}: {c.text}
                </li>
              ))}
            </ul>
          </>
        )}
        {summary.note && <p>{summary.note}</p>}
        <p>{summary.signedBy ? `Signed by ${summary.signedBy}.` : "Not signed yet."}</p>
      </section>
    </section>
  );
}
