// Client for the print service's docgen routes (services/print/api/openapi.yaml, issue #46):
// document templates, standard text blocks and output rules. The BFF rewrites /api/v1/print/...
// to /print/v1/... on the service.
import { apiFetch } from "../../shell/api/http";

const base = "/api/v1/print";

export const DOCUMENT_TYPE_LABELS: Record<string, string> = {
  PURCHASE_ORDER: "Purchase order",
  CUSTOMER_INVOICE: "Customer invoice",
  PACKING_SLIP: "Packing slip",
  REMITTANCE_ADVICE: "Remittance advice",
  PERIODIC_STATEMENT: "Statement of account",
  PAYMENT_NOTICE: "Payment notice",
  BALANCE_CONFIRMATION: "Balance confirmation",
};

export const CHANNEL_LABELS: Record<string, string> = {
  PRINTER: "Printer queue",
  EMAIL: "E-mail",
  EINVOICE: "E-invoice network",
  ARCHIVE: "Archive",
  PORTAL: "Portal",
};

export interface TemplateConfig {
  title?: string;
  footerText?: string;
  renameColumns?: Record<string, string>;
  hideColumns?: string[];
  bodyBlock?: string;
  noteBlocks?: string[];
}

export interface DocumentTemplate {
  code: string;
  name: string;
  documentType: string;
  default: boolean;
  version: number;
  config: TemplateConfig;
}

export interface TextBlock {
  code: string;
  language: string;
  body: string;
  placeholders: string[];
  version: number;
}

export interface OutputRule {
  id: string;
  documentType: string;
  legalEntityId?: string;
  accountId?: string;
  copyKind: "ORIGINAL" | "COPY";
  channel: string;
  destination?: string;
  copies: number;
  sequence: number;
}

export interface Problem {
  code: string;
  message: string;
}

export class DocgenApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "DocgenApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof DocgenApiError) return { code: error.code, message: error.message };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed" };
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const res = await apiFetch(`${base}${path}`, { headers: { Accept: "application/json" }, credentials: "same-origin", signal });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown } | null;
    throw new DocgenApiError(
      res.status,
      typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`,
      typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`,
    );
  }
  const body = (await res.json()) as { items?: T[] };
  if (!Array.isArray(body.items)) throw new DocgenApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

export const listTemplates = (signal?: AbortSignal) => items<DocumentTemplate>("/document-templates", signal);
export const listTextBlocks = (signal?: AbortSignal) => items<TextBlock>("/text-blocks", signal);
export const listOutputRules = (signal?: AbortSignal) => items<OutputRule>("/output-rules", signal);
