import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { ApiError, type BomCopyRequest } from "./api/client";
import styles from "./pages.module.css";

const forbiddenStatus = 403;

/**
 * Copy-to-new-draft form shared by the BOM and route version screens (FR-9.12). Product and site
 * start as the source's and are sent only when changed; for a BOM, `lines` lists the source line
 * numbers and unticked lines are left out of the copy.
 */
export function CopyVersionCard({
  noun,
  forbiddenText,
  source,
  siteRequired,
  lines,
  copy,
  onCopied,
}: {
  noun: string;
  forbiddenText: string;
  source: { productId: string; siteId?: string };
  /** A route needs a site; a BOM with an empty site applies to every site. */
  siteRequired?: boolean;
  lines?: number[];
  copy: (input: BomCopyRequest, idempotencyKey: string) => Promise<{ id: string }>;
  onCopied?: (id: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [product, setProduct] = useState(source.productId);
  const [site, setSite] = useState(source.siteId ?? "");
  const [chosen, setChosen] = useState<number[]>(lines ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef({ body: "", key: "" });

  function toggle(lineNo: number, on: boolean) {
    setChosen((current) => (on ? [...current, lineNo] : current.filter((n) => n !== lineNo)));
  }

  function build(): BomCopyRequest | string {
    if (!from) return "New effective from is required";
    if (to && to < from) return "Effective to must not be before effective from";
    const productId = product.trim();
    const siteId = site.trim();
    if (!productId) return "Product is required";
    if (siteRequired && !siteId) return "Site is required";
    if (lines && lines.length > 0 && chosen.length === 0) return "Choose at least one line to copy";
    return {
      effectiveFrom: from,
      ...(to ? { effectiveTo: to } : {}),
      ...(productId !== source.productId ? { productId } : {}),
      ...(siteId !== (source.siteId ?? "") ? { siteId } : {}),
      ...(lines && chosen.length < lines.length ? { lineNos: lines.filter((n) => chosen.includes(n)) } : {}),
    };
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    const input = build();
    if (typeof input === "string") {
      setError(input);
      return;
    }
    // The same key is reused for a retry of the same request, so a double submit cannot create two drafts.
    const body = JSON.stringify(input);
    if (attempt.current.body !== body) attempt.current = { body, key: crypto.randomUUID() };
    setBusy(true);
    try {
      const created = await copy(input, attempt.current.key);
      setOpen(false);
      onCopied?.(created.id);
    } catch (e) {
      setError(e instanceof ApiError && e.status === forbiddenStatus ? forbiddenText : e instanceof Error ? e.message : "Request failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className={styles.toolbar}>
        {open ? null : (
          <Button size="sm" onClick={() => setOpen(true)}>
            Copy to new version
          </Button>
        )}
      </div>
      {open ? (
        <Card title={`Copy to a new draft ${noun}`}>
          <form className={styles.form} onSubmit={submit} noValidate>
            <div className={styles.field}>
              <FormField id="copy-from" label="New effective from">
                {(control) => <Input {...control} type="date" value={from} onChange={(e) => setFrom(e.target.value)} required />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="copy-to" label="New effective to">
                {(control) => <Input {...control} type="date" value={to} onChange={(e) => setTo(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="copy-product" label="Product" hint="Change it to copy to another product.">
                {(control) => <Input {...control} value={product} onChange={(e) => setProduct(e.target.value)} required maxLength={64} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="copy-site" label="Site" hint={siteRequired ? "Change it to copy to another site." : "Change it to copy to another site; leave empty for all sites."}>
                {(control) => <Input {...control} value={site} onChange={(e) => setSite(e.target.value)} required={siteRequired} maxLength={64} />}
              </FormField>
            </div>
            {lines && lines.length > 0 ? (
              <fieldset className={styles.fieldset}>
                <legend className={styles.legend}>Lines to copy</legend>
                <div className={styles.checkList}>
                  {lines.map((lineNo) => (
                    <span key={lineNo} className={styles.check}>
                      <Input id={`copy-line-${lineNo}`} type="checkbox" checked={chosen.includes(lineNo)} onChange={(e) => toggle(lineNo, e.target.checked)} />
                      <label htmlFor={`copy-line-${lineNo}`}>Line {lineNo}</label>
                    </span>
                  ))}
                </div>
              </fieldset>
            ) : null}
            <Button type="submit" variant="primary" loading={busy}>
              Create copy
            </Button>
            <Button onClick={() => setOpen(false)} disabled={busy}>
              Cancel copy
            </Button>
          </form>
          <div className={styles.alertSlot}>
            <Alert tone="danger" live="alert">
              {error || null}
            </Alert>
          </div>
        </Card>
      ) : null}
    </>
  );
}
