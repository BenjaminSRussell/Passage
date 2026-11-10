import { base, OrgApiError } from "./client";
import { apiFetch } from "../../../shell/api/http";

// The organization model package (FR-6.12): POST /v1/org/export and /v1/org/import. Both routes are
// tenant-level (no legal-entity header) and need org.package.manage with tenant.administer.

export interface ImportCounts {
  created: number;
  unchanged: number;
}

export interface ImportDifference {
  kind: string;
  code: string;
  field: string;
}

export interface ImportSkip {
  kind: string;
  code: string;
  reason: string;
}

export interface ImportReport {
  dryRun: boolean;
  created: number;
  unchanged: number;
  kinds: Record<string, ImportCounts>;
  differences: ImportDifference[];
  skipped?: ImportSkip[];
}

export type OrgPackage = Record<string, unknown> & { schemaVersion: number };

export class PackageError extends OrgApiError {
  constructor(
    status: number,
    code: string,
    message: string,
    readonly fields: Record<string, string>,
  ) {
    super(status, code, message);
    this.name = "PackageError";
  }
}

async function send<T>(path: string, init: RequestInit): Promise<T> {
  const res = await apiFetch(`${base}${path}`, init);
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
    const fields =
      body?.errors && typeof body.errors === "object"
        ? Object.fromEntries(Object.entries(body.errors as Record<string, unknown>).map(([key, value]) => [key, String(value)]))
        : {};
    throw new PackageError(
      res.status,
      typeof body?.code === "string" && body.code !== "" ? body.code : "COM-0001",
      typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (${res.status})`,
      fields,
    );
  }
  return (await res.json()) as T;
}

export const exportPackage = () => send<OrgPackage & { exportedAt: string }>("/export", { method: "POST" });

export const importPackage = (body: string, dryRun: boolean) =>
  send<ImportReport>(`/import${dryRun ? "?dryRun=true" : ""}`, { method: "POST", headers: { "Content-Type": "application/json" }, body });
