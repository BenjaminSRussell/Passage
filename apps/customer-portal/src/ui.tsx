import { useCallback, useEffect, useState, type ReactNode } from "react";
import { ApiError, download } from "./api";

export interface Loaded<T> {
  data: T | undefined;
  error: string;
  loading: boolean;
  reload: () => void;
}

/** Loads data once per dependency change; the error is a message fit to show the user. */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[]): Loaded<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(fn, deps);
  useEffect(() => {
    let live = true;
    setLoading(true);
    run().then(
      (d) => live && (setData(d), setError(""), setLoading(false)),
      (e: unknown) => live && (setError(messageOf(e)), setLoading(false)),
    );
    return () => {
      live = false;
    };
  }, [run, tick]);
  return { data, error, loading, reload: () => setTick((t) => t + 1) };
}

export function messageOf(e: unknown): string {
  if (e instanceof ApiError) {
    const fields = Object.entries(e.fields).map(([k, v]) => `${k}: ${v}`);
    return [e.message, ...fields].join(" ");
  }
  return "Something went wrong. Try again.";
}

export function Page({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section aria-labelledby="page-title">
      <div className="page-head">
        <h1 id="page-title" tabIndex={-1}>
          {title}
        </h1>
        {action}
      </div>
      {children}
    </section>
  );
}

export function State<T>({ load, empty, children }: { load: Loaded<T>; empty?: (d: T) => boolean; children: (d: T) => ReactNode }) {
  if (load.loading && load.data === undefined) return <p role="status">Loading…</p>;
  if (load.error) return <p role="alert" className="error">{load.error}</p>;
  if (load.data === undefined) return null;
  if (empty?.(load.data)) return <p>Nothing here yet.</p>;
  return <>{children(load.data)}</>;
}

export function Notice({ kind, children }: { kind: "error" | "ok"; children: ReactNode }) {
  return (
    <p role={kind === "error" ? "alert" : "status"} className={kind}>
      {children}
    </p>
  );
}

export function Field({ label, children, hint }: { label: string; children: (id: string) => ReactNode; hint?: string }) {
  const id = `f-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id)}
      {hint ? <small id={`${id}-hint`}>{hint}</small> : null}
    </div>
  );
}

export function Badge({ value }: { value: string }) {
  return <span className={`badge badge-${value.toLowerCase().replace(/_/g, "-")}`}>{value.replace(/_/g, " ").toLowerCase()}</span>;
}

export const day = (iso?: string): string => (iso ? iso.slice(0, 10) : "");
export const money = (amount: string, currency: string): string => `${amount} ${currency}`;

/** A button that downloads a PDF from the BFF, with a busy state and the reason when it fails. */
export function DownloadButton({ path, fileName, label }: { path: string; fileName: string; label: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <span className="download">
      <button
        type="button"
        aria-busy={busy || undefined}
        disabled={busy}
        onClick={() => {
          setBusy(true);
          setError("");
          download(path, fileName)
            .catch((e: unknown) => setError(messageOf(e)))
            .finally(() => setBusy(false));
        }}
      >
        {busy ? "Preparing…" : label}
      </button>
      {error ? <Notice kind="error">{error}</Notice> : null}
    </span>
  );
}
