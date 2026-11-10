import { useState } from "react";
import { holdsDuty } from "../dutyGate";
import { ProblemMessage } from "../errors";
import { apiFetch, readProblem } from "../api/http";
import { formatOption, resolveOption, type LookupEntityKey } from "../lookup/entities";
import { usePrincipalDuties } from "../principal";
import { Alert, Button } from "../ui";

/** Business documents the print service renders as PDF (services/print, POST /print/v1/documents:render). */
export type DocumentType = "PURCHASE_ORDER" | "CUSTOMER_INVOICE" | "PACKING_SLIP";

export interface DocumentParty {
  label?: string;
  name: string;
  address?: string[];
}

export interface DocumentRequest {
  documentType: DocumentType;
  number: string;
  legalEntity: DocumentParty;
  counterparty?: DocumentParty;
  fields?: { label: string; value: string }[];
  columns: { header: string; align?: "L" | "R" }[];
  lines: string[][];
  totals?: { label: string; value: string }[];
  notes?: string;
}

const RENDER = "/api/v1/print/documents:render";

async function getJson<T>(path: string): Promise<T> {
  const res = await apiFetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return (await res.json()) as T;
}

/** The legal entity as the document letterhead shows it: its name and the lines of its primary address. */
export async function legalEntityParty(legalEntityId: string): Promise<DocumentParty> {
  const entity = await getJson<{ name: string; primaryAddressId?: string | null }>(`/api/v1/org/legal-entities/${encodeURIComponent(legalEntityId)}`);
  let address: string[] = [];
  if (entity.primaryAddressId) {
    try {
      const a = await getJson<{ formatted: string }>(`/api/v1/org/addresses/${encodeURIComponent(entity.primaryAddressId)}`);
      address = a.formatted.split(/\n|, /).map((l) => l.trim()).filter((l) => l !== "");
    } catch {
      address = [];
    }
  }
  return { name: entity.name, address };
}

/** The display name of a master-data record, never its id. */
export async function entityLabel(entity: LookupEntityKey, id: string | undefined): Promise<string> {
  if (!id) return "";
  const found = await resolveOption(entity, id);
  return found ? formatOption(found) : "";
}

/** Plain text the PDF font can print: curly quotes, dashes and the like become their ASCII form. */
export function pdfText(s: string | undefined): string {
  return (s ?? "")
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/[–—]/g, "-")
    .replace(/…/g, "...")
    .replace(/[^ -~ -ÿ]/g, "?");
}

export async function renderDocumentPdf(request: DocumentRequest): Promise<Blob> {
  const res = await apiFetch(RENDER, {
    method: "POST",
    headers: { Accept: "application/pdf", "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
  if (!res.ok) throw await readProblem(res);
  return res.blob();
}

function save(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

/**
 * The "Download PDF" action of a document page. `build` gathers the document's data (names, not ids);
 * a document the service cannot render shows its reason in place, and the page keeps working.
 */
export function DownloadPdfButton({ build, filename }: { build: () => Promise<DocumentRequest>; filename: string }) {
  const duties = usePrincipalDuties();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<unknown>(null);
  if (!holdsDuty(duties, "print.document.render")) return null;

  async function run() {
    setBusy(true);
    setProblem(null);
    try {
      save(await renderDocumentPdf(await build()), filename);
    } catch (err) {
      setProblem(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Button loading={busy} onClick={() => void run()}>
        Download PDF
      </Button>
      {problem ? (
        <Alert tone="danger" title="This document can't be downloaded as a PDF" live="alert">
          <ProblemMessage problem={problem} />
        </Alert>
      ) : null}
    </>
  );
}
