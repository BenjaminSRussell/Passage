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
  /** The vendor of a purchase order, or the customer of an invoice or packing slip. */
  counterparty?: DocumentParty;
  /** Up to two further parties printed beside the counterparty, such as "Deliver to". */
  additionalParties?: DocumentParty[];
  fields?: { label: string; value: string }[];
  columns: { header: string; align?: "L" | "R" }[];
  lines: string[][];
  totals?: { label: string; value: string }[];
  notes?: string;
}

const RENDER = "/api/v1/print/documents:render";
const ORG = "/api/v1/org";
/** The service prints at most this many address lines per party. */
const MAX_ADDRESS_LINES = 8;

/** Address purposes of the org service, in the order a document prefers them. */
export type AddressPurpose = "business" | "delivery" | "invoice" | "remit_to";

interface PostalAddress {
  formatted?: string;
  purposes?: { purpose: string; primary?: boolean }[];
}

async function getJson<T>(path: string, legalEntityId?: string): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  // Addresses sit behind the legal-entity policy of the org service, so their reads name the document's legal entity.
  if (legalEntityId) headers["X-Passage-LegalEntity"] = legalEntityId;
  const res = await apiFetch(path, { headers });
  if (!res.ok) throw await readProblem(res);
  return (await res.json()) as T;
}

/** The printed lines of an address: the org service formats one line per row; older text is comma-separated. */
export function addressLines(formatted: string | undefined): string[] {
  const text = formatted ?? "";
  return (text.includes("\n") ? text.split("\n") : text.split(", "))
    .map(pdfText)
    .filter((l) => l !== "")
    .slice(0, MAX_ADDRESS_LINES);
}

async function addressById(addressId: string | null | undefined, legalEntityId: string): Promise<string[]> {
  if (!addressId) return [];
  try {
    return addressLines((await getJson<PostalAddress>(`${ORG}/addresses/${encodeURIComponent(addressId)}`, legalEntityId)).formatted);
  } catch {
    return [];
  }
}

/** The legal entity as the document letterhead shows it: its name and the lines of its primary address. */
export async function legalEntityParty(legalEntityId: string): Promise<DocumentParty> {
  const entity = await getJson<{ name: string; primaryAddressId?: string | null }>(`${ORG}/legal-entities/${encodeURIComponent(legalEntityId)}`);
  return { name: pdfText(entity.name), address: await addressById(entity.primaryAddressId, legalEntityId) };
}

/**
 * The address a document prints for a party: its primary address for the first of `purposes` that has one, then any
 * address with one of those purposes, then the party's first address. A party without an address, or an address the
 * user may not read, gives no lines; the document still prints the name.
 */
export async function partyAddress(partyId: string, purposes: AddressPurpose[], legalEntityId: string): Promise<string[]> {
  try {
    const query = new URLSearchParams({ ownerType: "party", ownerId: partyId, limit: "100" });
    const page = await getJson<{ items?: PostalAddress[] }>(`${ORG}/addresses?${query}`, legalEntityId);
    const all = Array.isArray(page.items) ? page.items : [];
    const has = (a: PostalAddress, purpose: string, primaryOnly: boolean) => (a.purposes ?? []).some((p) => p.purpose === purpose && (!primaryOnly || p.primary === true));
    const pick =
      purposes.map((purpose) => all.find((a) => has(a, purpose, true))).find((a) => a !== undefined) ??
      purposes.map((purpose) => all.find((a) => has(a, purpose, false))).find((a) => a !== undefined) ??
      all[0];
    return addressLines(pick?.formatted);
  } catch {
    return [];
  }
}

export interface Counterparty {
  party: DocumentParty;
  /** The vendor or customer account number, for a header field; empty when unknown. */
  accountNumber: string;
}

/**
 * A vendor or customer as the document shows it: the party's name (never an id) and its address for the given
 * purposes. `fallbackName` stands in when the name cannot be read, so the document still renders.
 */
export async function counterparty(
  kind: "vendor" | "customer",
  id: string | undefined,
  opts: { label: string; fallbackName: string; purposes: AddressPurpose[]; legalEntityId: string },
): Promise<Counterparty> {
  if (!id) return { party: { label: opts.label, name: opts.fallbackName, address: [] }, accountNumber: "" };
  const [found, address] = await Promise.all([
    resolveOption(kind, id),
    getJson<{ partyId?: string }>(`/api/v1/party/${kind}s/${encodeURIComponent(id)}`)
      .then((role) => (role.partyId ? partyAddress(role.partyId, opts.purposes, opts.legalEntityId) : []))
      .catch(() => [] as string[]),
  ]);
  return {
    party: { label: opts.label, name: pdfText(found?.name) || opts.fallbackName, address },
    accountNumber: pdfText(found?.number),
  };
}

/** A site or warehouse as a document party (for example "Deliver to"), or null when it cannot be read. */
export async function orgUnitParty(kind: "sites" | "warehouses", id: string | undefined, label: string, legalEntityId: string): Promise<DocumentParty | null> {
  if (!id) return null;
  try {
    const unit = await getJson<{ name: string; addressId?: string | null; siteId?: string }>(`${ORG}/${kind}/${encodeURIComponent(id)}`);
    let address = await addressById(unit.addressId, legalEntityId);
    if (address.length === 0 && kind === "warehouses" && unit.siteId) {
      // A warehouse without its own address is at its site's.
      const site = await getJson<{ addressId?: string | null }>(`${ORG}/sites/${encodeURIComponent(unit.siteId)}`).catch(() => null);
      address = await addressById(site?.addressId, legalEntityId);
    }
    const name = pdfText(unit.name);
    return name ? { label, name, address } : null;
  } catch {
    return null;
  }
}

/** The display name of a master-data record, never its id. */
export async function entityLabel(entity: LookupEntityKey, id: string | undefined): Promise<string> {
  if (!id) return "";
  const found = await resolveOption(entity, id);
  return found ? formatOption(found) : "";
}

/**
 * Text as the PDF prints it. The document fonts print any Unicode text, so nothing is replaced; tabs and line breaks
 * become spaces and other control characters are dropped, because the service refuses them in single-line values.
 */
export function pdfText(s: string | undefined): string {
  return (s ?? "")
    .replace(/[\t\n\r\u2028\u2029]+/g, " ")
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, "")
    .trim();
}

/** Text that may keep its line breaks (table cells and notes). */
export function pdfMultilineText(s: string | undefined): string {
  return (s ?? "")
    .replace(/\r\n?|[\u2028\u2029]/g, "\n")
    .replace(/\t/g, " ")
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/g, "")
    .trim();
}

export interface RenderedPdf {
  blob: Blob;
  /** Characters the document fonts lack; each is printed as a placeholder mark. */
  unprintable: number;
}

export async function renderDocument(request: DocumentRequest): Promise<RenderedPdf> {
  const res = await apiFetch(RENDER, {
    method: "POST",
    headers: { Accept: "application/pdf", "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
  if (!res.ok) throw await readProblem(res);
  const unprintable = Number(res.headers?.get?.("X-Passage-Unprintable-Characters") ?? 0);
  return { blob: await res.blob(), unprintable: Number.isFinite(unprintable) && unprintable > 0 ? unprintable : 0 };
}

export async function renderDocumentPdf(request: DocumentRequest): Promise<Blob> {
  return (await renderDocument(request)).blob;
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
 * a document the service cannot render shows its reason in place, and the page keeps working. A document with
 * characters the PDF fonts lack is still downloaded, with a note that says how many print as a placeholder.
 */
export function DownloadPdfButton({ build, filename }: { build: () => Promise<DocumentRequest>; filename: string }) {
  const duties = usePrincipalDuties();
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<unknown>(null);
  const [unprintable, setUnprintable] = useState(0);
  if (!holdsDuty(duties, "print.document.render")) return null;

  async function run() {
    setBusy(true);
    setProblem(null);
    setUnprintable(0);
    try {
      const rendered = await renderDocument(await build());
      save(rendered.blob, filename);
      setUnprintable(rendered.unprintable);
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
      {unprintable > 0 ? (
        <Alert tone="warning" title="The PDF was downloaded, but some characters could not be printed" live="status">
          {unprintable === 1 ? "1 character is" : `${unprintable} characters are`} not in the PDF font and{" "}
          {unprintable === 1 ? "shows" : "show"} as a placeholder mark (�). The PDF prints Latin, Greek and Cyrillic text; it cannot yet print
          scripts such as Chinese, Japanese, Korean or Arabic. Everything else in the document is complete.
        </Alert>
      ) : null}
    </>
  );
}
