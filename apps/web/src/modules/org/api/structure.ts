import { OrgApiError, type LegalEntity } from "./client";
import { referenceFetch, withQuery } from "./reference";

// Tenant-level administration calls: they carry no legal-entity scope header, because creating a legal entity,
// generating a fiscal year and maintaining teams are refused for a legal-entity-scoped caller.

export interface CreateLegalEntityInput {
  code: string;
  name: string;
  country: string;
  currency: string;
  timeZone: string;
  defaultLanguage?: string;
  registrationNumber?: string;
}

export const createLegalEntity = (input: CreateLegalEntityInput) =>
  referenceFetch<LegalEntity>("/legal-entities", { method: "POST", body: JSON.stringify(input) });

export const getLegalEntity = (id: string) => referenceFetch<LegalEntity>(`/legal-entities/${encodeURIComponent(id)}`);

export const setLegalEntityStatus = (id: string, version: number, status: "active" | "inactive") =>
  referenceFetch<LegalEntity>(`/legal-entities/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, status }) });

export const fiscalPatterns = ["calendar", "4-4-5", "4-5-4", "5-4-4", "13-period"] as const;
export type FiscalPattern = (typeof fiscalPatterns)[number] | "custom";

export interface FiscalCalendar {
  id: string;
  code: string;
  name: string;
  pattern: FiscalPattern;
  startMonth: number;
  weekStart: string;
  openingClosing: boolean;
  specialPeriods: number;
  status: "active" | "inactive";
  version: number;
  generatedYears: number[];
}

export interface FiscalPeriod {
  fiscalYear: number;
  periodNo: number;
  name: string;
  startDate: string;
  endDate: string;
  type: "opening" | "operating" | "closing" | "adjustment";
  week53: boolean;
}

export interface FiscalYear {
  calendarId: string;
  fiscalYear: number;
  generated: boolean;
  periods: FiscalPeriod[];
}

export interface CreateFiscalCalendarInput {
  code: string;
  name: string;
  pattern: FiscalPattern;
  startMonth: number;
}

interface Options {
  signal?: AbortSignal;
}

async function items<T>(path: string, filters: Record<string, string | undefined>, options?: Options): Promise<T[]> {
  const out: T[] = [];
  let cursor: string | undefined;
  do {
    const body = await referenceFetch<{ items?: unknown; nextCursor?: unknown }>(withQuery(path, { limit: "100", ...filters, cursor }), {
      signal: options?.signal,
    });
    if (Array.isArray(body?.items)) {
      out.push(...(body.items as T[]));
    }
    const next = body?.nextCursor;
    cursor = typeof next === "string" && next !== "" && next !== cursor ? next : undefined;
  } while (cursor);
  return out;
}

export const listFiscalCalendars = (options?: Options) => items<FiscalCalendar>("/fiscal-calendars", {}, options);

export const createFiscalCalendar = (input: CreateFiscalCalendarInput) =>
  referenceFetch<FiscalCalendar>("/fiscal-calendars", { method: "POST", body: JSON.stringify(input) });

export interface GenerateFiscalYearOptions {
  // Custom-pattern calendars carry their own periods, one start and end date (YYYY-MM-DD) each.
  periods?: { start: string; end: string }[];
  force?: boolean;
}

export const generateFiscalYear = (calendarId: string, fiscalYear: number, options: GenerateFiscalYearOptions = {}) =>
  referenceFetch<FiscalYear>(`/fiscal-calendars/${encodeURIComponent(calendarId)}:generate`, {
    method: "POST",
    body: JSON.stringify({ fiscalYear, ...(options.periods && options.periods.length > 0 ? { periods: options.periods } : {}), ...(options.force ? { force: true } : {}) }),
  });

export const getFiscalCalendar = (id: string) => referenceFetch<FiscalCalendar>(`/fiscal-calendars/${encodeURIComponent(id)}`);

export const renameFiscalCalendar = (id: string, version: number, name: string) =>
  referenceFetch<FiscalCalendar>(`/fiscal-calendars/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, name }) });

export const setFiscalCalendarStatus = (id: string, version: number, status: "active" | "inactive") =>
  referenceFetch<FiscalCalendar>(`/fiscal-calendars/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, status }) });

export async function listFiscalPeriods(calendarId: string, fiscalYear: number, options?: Options): Promise<FiscalPeriod[]> {
  const body = await referenceFetch<{ items?: unknown }>(
    withQuery(`/fiscal-calendars/${encodeURIComponent(calendarId)}/periods`, { fiscalYear: String(fiscalYear) }),
    { signal: options?.signal },
  );
  return Array.isArray(body?.items) ? (body.items as FiscalPeriod[]) : [];
}

export interface TeamType {
  id: string;
  code: string;
  name: string;
  memberKinds: string[];
}

export interface Team {
  id: string;
  code: string;
  name: string;
  description?: string;
  teamTypeId: string;
  operatingUnitId?: string;
  effectiveFrom: string;
  effectiveTo?: string;
  status: "active" | "inactive";
  version: number;
}

export interface TeamMember {
  id: string;
  memberKind: string;
  memberId: string;
  memberRole?: string;
  validFrom: string;
  validTo?: string;
  // "rule" members come from a membership rule: the service refuses to remove them (ORG-TEAM-MEMBER-RULE-SOURCED).
  source?: "manual" | "rule";
}

export const memberKinds = ["user", "party"] as const;
export const memberRoles = ["member", "lead"] as const;

export interface AddTeamMemberInput {
  memberKind: string;
  memberId: string;
  memberRole: string;
  validFrom: string;
  validTo?: string;
}

export interface CreateTeamInput {
  code: string;
  name: string;
  teamTypeId: string;
  effectiveFrom: string;
  description?: string;
}

export const listTeamTypes = (options?: Options) => items<TeamType>("/team-types", {}, options);

export const listTeams = (options?: Options) => items<Team>("/teams", {}, options);

export const createTeam = (input: CreateTeamInput) => referenceFetch<Team>("/teams", { method: "POST", body: JSON.stringify(input) });

export const getTeam = (id: string) => referenceFetch<Team>(`/teams/${encodeURIComponent(id)}`);

export const setTeamStatus = (id: string, version: number, status: "active" | "inactive") =>
  referenceFetch<Team>(`/teams/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, status }) });

export const createTeamType = (input: { code: string; name: string; memberKinds: string[] }) =>
  referenceFetch<TeamType>("/team-types", { method: "POST", body: JSON.stringify(input) });

export const updateTeam = (id: string, version: number, patch: { name?: string; description?: string; effectiveTo?: string }) =>
  referenceFetch<Team>(`/teams/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ version, ...patch }) });

export const addTeamMember = (teamId: string, input: AddTeamMemberInput) =>
  referenceFetch<TeamMember>(`/teams/${encodeURIComponent(teamId)}/members`, { method: "POST", body: JSON.stringify(input) });

export const endTeamMember = (teamId: string, memberRowId: string, validTo: string) =>
  referenceFetch<TeamMember>(`/teams/${encodeURIComponent(teamId)}/members/${encodeURIComponent(memberRowId)}:end`, {
    method: "POST",
    body: JSON.stringify({ validTo }),
  });

export const removeTeamMember = (teamId: string, memberRowId: string) =>
  referenceFetch<void>(`/teams/${encodeURIComponent(teamId)}/members/${encodeURIComponent(memberRowId)}`, { method: "DELETE" });

export const listTeamMembers = (teamId: string, options?: Options) => items<TeamMember>(`/teams/${encodeURIComponent(teamId)}/members`, {}, options);

const VERSION_CONFLICT = "ORG-VERSION-CONFLICT";

// withFreshRowOnConflict runs a versioned write; on ORG-VERSION-CONFLICT it reloads the row so the table shows the
// current status and version (and a retry can succeed), then rethrows so the caller still shows the conflict.
export async function withFreshRowOnConflict<T>(write: () => Promise<T>, reload: () => Promise<T>, apply: (row: T) => void): Promise<void> {
  try {
    apply(await write());
  } catch (error) {
    if (error instanceof OrgApiError && error.code === VERSION_CONFLICT) {
      await reload().then(apply, () => undefined);
    }
    throw error;
  }
}
