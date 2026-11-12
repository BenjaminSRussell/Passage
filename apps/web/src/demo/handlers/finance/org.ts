// Organization reference data for the tenant-admin screens (/api/v1/org): the legal entity in full, countries and
// currencies, fiscal calendars with generated years and their periods, and teams with their members. Registered
// before the shell's handlers, so these complete rows replace the minimal ones the setup checklist needs.
import type { LegalEntity } from "../../../modules/org/api/client";
import type { Country, Currency } from "../../../modules/org/api/reference";
import type { FiscalCalendar, FiscalPattern, FiscalPeriod, Team, TeamMember, TeamType } from "../../../modules/org/api/structure";
import { CUSTOMERS, LEGAL_ENTITY, LEGAL_ENTITY_DE, LEGAL_ENTITY_ES } from "../../company";
import { reply, route } from "../../backend";
import { at, day, newId, table, uid } from "../../store";
import { byUsername } from "./people";

const ORG = "/api/v1/org";
const K_CALENDAR = 648;
const K_TEAM = 640;
const K_TEAM_TYPE = 641;
const K_MEMBER = 642;

const THIS_YEAR = new Date().getFullYear();

// --- fiscal calendars -----------------------------------------------------------------------------

const CAL = uid(K_CALENDAR, 1);
const calendars = table<FiscalCalendar>([
  { id: CAL, code: "FY-CAL", name: "Fiscal year = calendar year", pattern: "calendar", startMonth: 1, weekStart: "monday", openingClosing: true, specialPeriods: 0, status: "active", version: 4, generatedYears: [THIS_YEAR - 2, THIS_YEAR - 1, THIS_YEAR] },
  { id: uid(K_CALENDAR, 2), code: "MFG-445", name: "Plant production weeks (4-4-5)", pattern: "4-4-5", startMonth: 1, weekStart: "monday", openingClosing: false, specialPeriods: 0, status: "active", version: 2, generatedYears: [THIS_YEAR - 1, THIS_YEAR] },
  { id: uid(K_CALENDAR, 3), code: "FY-JUL", name: "July fiscal year (former parent group)", pattern: "calendar", startMonth: 7, weekStart: "monday", openingClosing: false, specialPeriods: 0, status: "inactive", version: 6, generatedYears: [THIS_YEAR - 3, THIS_YEAR - 2] },
]);

const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const iso = (d: Date) => d.toISOString().slice(0, 10);
const addDays = (d: Date, n: number) => new Date(d.getTime() + n * 86_400_000);

/** Periods of one fiscal year, the way the service lays them out for each pattern. */
function periodsOf(c: FiscalCalendar, fiscalYear: number): FiscalPeriod[] {
  const out: FiscalPeriod[] = [];
  // A fiscal year named for the calendar year it ends in.
  const startYear = c.startMonth === 1 ? fiscalYear : fiscalYear - 1;
  const yearStart = new Date(Date.UTC(startYear, c.startMonth - 1, 1));
  if (c.pattern === "calendar") {
    if (c.openingClosing) out.push({ fiscalYear, periodNo: 0, name: `Opening ${fiscalYear}`, startDate: iso(yearStart), endDate: iso(yearStart), type: "opening", week53: false });
    for (let i = 0; i < 12; i++) {
      const s = new Date(Date.UTC(startYear, c.startMonth - 1 + i, 1));
      const e = new Date(Date.UTC(startYear, c.startMonth + i, 0));
      out.push({ fiscalYear, periodNo: i + 1, name: `${MONTHS[s.getUTCMonth()]} ${s.getUTCFullYear()}`, startDate: iso(s), endDate: iso(e), type: "operating", week53: false });
    }
    if (c.openingClosing) {
      const end = out[out.length - 1].endDate;
      out.push({ fiscalYear, periodNo: 13, name: `Closing ${fiscalYear}`, startDate: end, endDate: end, type: "closing", week53: false });
    }
    return out;
  }
  // Week patterns start on the first Monday on or before the first of the start month.
  let s = addDays(yearStart, -((yearStart.getUTCDay() + 6) % 7));
  const weeks = c.pattern === "13-period" ? Array(13).fill(4) : Array.from({ length: 12 }, (_, i) => Number(c.pattern.split("-")[i % 3]));
  // Every fifth or sixth year needs a 53rd week to stay in step; the last period takes it.
  const nextStart = (() => {
    const ny = new Date(Date.UTC(startYear + 1, c.startMonth - 1, 1));
    return addDays(ny, -((ny.getUTCDay() + 6) % 7));
  })();
  const total = weeks.reduce((a, b) => a + b, 0);
  const week53 = Math.round((nextStart.getTime() - s.getTime()) / (7 * 86_400_000)) > total;
  weeks.forEach((w, i) => {
    const len = w + (week53 && i === weeks.length - 1 ? 1 : 0);
    const e = addDays(s, len * 7 - 1);
    out.push({ fiscalYear, periodNo: i + 1, name: `P${String(i + 1).padStart(2, "0")} ${fiscalYear}`, startDate: iso(s), endDate: iso(e), type: "operating", week53: week53 && i === weeks.length - 1 });
    s = addDays(e, 1);
  });
  return out;
}

route("GET", `${ORG}/fiscal-calendars`, ({ query }) => {
  const status = query.get("status");
  return { items: calendars.all().filter((c) => !status || c.status === status).sort((a, b) => a.code.localeCompare(b.code)) };
});
route("POST", `${ORG}/fiscal-calendars`, ({ body }) => {
  const input = (body ?? {}) as { code?: string; name?: string; pattern?: FiscalPattern; startMonth?: number };
  const code = (input.code ?? "").toUpperCase();
  if (calendars.all().some((c) => c.code === code)) return reply.problem(409, "ORG-CAL-CODE-DUPLICATE", "a fiscal calendar with this code exists");
  return reply.created(calendars.insert({ id: newId(), code, name: input.name ?? code, pattern: input.pattern ?? "calendar", startMonth: input.startMonth ?? 1, weekStart: "monday", openingClosing: false, specialPeriods: 0, status: "active", version: 1, generatedYears: [] }));
});
route("GET", `${ORG}/fiscal-calendars/{id}/periods`, ({ params, query }) => {
  const c = calendars.get(params.id);
  const year = Number(query.get("fiscalYear"));
  if (!c) return reply.problem(404, "ORG-0404", "No fiscal calendar with that id");
  return { items: c.generatedYears.includes(year) ? periodsOf(c, year) : [] };
});
route("POST", `${ORG}/fiscal-calendars/{id}:generate`, ({ params, body }) => {
  const c = calendars.get(params.id);
  if (!c) return reply.problem(404, "ORG-0404", "No fiscal calendar with that id");
  const input = (body ?? {}) as { fiscalYear?: number; force?: boolean };
  const year = Number(input.fiscalYear);
  if (c.generatedYears.includes(year) && !input.force) return reply.problem(409, "ORG-CAL-YEAR-GENERATED", `${year} is already generated; regenerate it to replace its periods`);
  calendars.update(c.id, { generatedYears: [...new Set([...c.generatedYears, year])].sort(), version: c.version + 1 });
  return { calendarId: c.id, fiscalYear: year, generated: true, periods: periodsOf(c, year) };
});
route("GET", `${ORG}/fiscal-calendars/{id}`, ({ params }) => calendars.get(params.id) ?? reply.problem(404, "ORG-0404", "No fiscal calendar with that id"));
route("PATCH", `${ORG}/fiscal-calendars/{id}`, ({ params, body }) => {
  const c = calendars.get(params.id);
  if (!c) return reply.problem(404, "ORG-0404", "No fiscal calendar with that id");
  const input = (body ?? {}) as { version?: number; name?: string; status?: FiscalCalendar["status"] };
  if (input.version !== undefined && input.version !== c.version) return reply.problem(409, "ORG-VERSION-CONFLICT", "the calendar changed since you opened it");
  return calendars.update(c.id, { ...(input.name ? { name: input.name } : {}), ...(input.status ? { status: input.status } : {}), version: c.version + 1 });
});

// --- legal entity, countries, currencies ----------------------------------------------------------

const legalEntities = table<LegalEntity>([
  {
    id: LEGAL_ENTITY.id,
    code: LEGAL_ENTITY.code,
    name: LEGAL_ENTITY.name,
    country: LEGAL_ENTITY.country,
    currency: LEGAL_ENTITY.currency,
    timeZone: "America/Chicago",
    defaultLanguage: "en-US",
    status: "active",
    version: 7,
    createdAt: at(-1500),
    updatedAt: at(-45),
    fiscalCalendarId: CAL,
    registrationNumber: "EIN 42-1876530",
  },
  // The group's European subsidiaries, so a screen that names one (an e-invoice of the German entity) shows its name.
  ...([
    [LEGAL_ENTITY_DE, "Europe/Berlin", "de-DE", "HRB 218834"],
    [LEGAL_ENTITY_ES, "Europe/Madrid", "es-ES", "B-85123456"],
  ] as const).map(([e, timeZone, defaultLanguage, registrationNumber]) => ({
    id: e.id,
    code: e.code,
    name: e.name,
    country: e.country,
    currency: e.currency,
    timeZone,
    defaultLanguage,
    status: "active" as const,
    version: 1,
    createdAt: at(-900),
    updatedAt: at(-30),
    fiscalCalendarId: CAL,
    registrationNumber,
  })),
]);
const matches = (q: string | null, ...fields: string[]) => {
  const needle = (q ?? "").trim().toLowerCase();
  return needle === "" || fields.some((f) => f.toLowerCase().includes(needle));
};
route("GET", `${ORG}/legal-entities`, ({ query }) => ({ items: legalEntities.all().filter((e) => matches(query.get("q"), e.name, e.code)) }));
route("GET", `${ORG}/legal-entities/{id}`, ({ params }) => legalEntities.all().find((e) => e.id === params.id || e.code === params.id) ?? reply.problem(404, "ORG-0404", "No legal entity with that id"));

const countries: Country[] = [
  { code: "US", name: "United States", status: "active", regionRequired: true, postalRegex: "^\\d{5}(-\\d{4})?$" },
  { code: "CA", name: "Canada", status: "active", regionRequired: true, postalRegex: "^[A-Z]\\d[A-Z] ?\\d[A-Z]\\d$" },
  { code: "MX", name: "Mexico", status: "active", regionRequired: true, postalRegex: "^\\d{5}$" },
  { code: "DE", name: "Germany", status: "active", regionRequired: false, postalRegex: "^\\d{5}$" },
  { code: "FR", name: "France", status: "active", regionRequired: false, postalRegex: "^\\d{5}$" },
  { code: "NL", name: "Netherlands", status: "active", regionRequired: false, postalRegex: "^\\d{4} ?[A-Z]{2}$" },
  { code: "PL", name: "Poland", status: "active", regionRequired: false, postalRegex: "^\\d{2}-\\d{3}$" },
  { code: "IT", name: "Italy", status: "active", regionRequired: false, postalRegex: "^\\d{5}$" },
  { code: "GB", name: "United Kingdom", status: "inactive", regionRequired: false },
];
const currencies: Currency[] = [
  { code: "USD", name: "US dollar", status: "active", numericCode: "840", minorUnits: 2 },
  { code: "CAD", name: "Canadian dollar", status: "active", numericCode: "124", minorUnits: 2 },
  { code: "EUR", name: "Euro", status: "active", numericCode: "978", minorUnits: 2 },
  { code: "MXN", name: "Mexican peso", status: "active", numericCode: "484", minorUnits: 2 },
  { code: "GBP", name: "Pound sterling", status: "inactive", numericCode: "826", minorUnits: 2 },
];
route("GET", `${ORG}/countries`, ({ query }) => ({ items: countries.filter((c) => !query.get("status") || c.status === query.get("status")) }));
route("GET", `${ORG}/currencies`, ({ query }) => ({ items: currencies.filter((c) => !query.get("status") || c.status === query.get("status")) }));

// --- teams ----------------------------------------------------------------------------------------

const types = table<TeamType>([
  { id: uid(K_TEAM_TYPE, 1), code: "APPROVAL", name: "Approval group", memberKinds: ["user"] },
  { id: uid(K_TEAM_TYPE, 2), code: "ACCOUNT", name: "Customer account team", memberKinds: ["user", "party"] },
  { id: uid(K_TEAM_TYPE, 3), code: "WORKGROUP", name: "Working group", memberKinds: ["user"] },
]);

const teams = table<Team>([
  { id: uid(K_TEAM, 1), code: "CLOSE", name: "Month-end close", description: "Owns the close checklist, accruals and the statement pack.", teamTypeId: uid(K_TEAM_TYPE, 3), effectiveFrom: day(-900), status: "active", version: 6 },
  { id: uid(K_TEAM, 2), code: "AP-25K", name: "Invoice approvers over 25,000", description: "Second approval on vendor invoices and payment proposals above 25,000 USD.", teamTypeId: uid(K_TEAM_TYPE, 1), effectiveFrom: day(-700), status: "active", version: 3 },
  { id: uid(K_TEAM, 3), code: "CAPEX", name: "Capital expenditure committee", description: "Reviews capex requests above 50,000 USD before the CFO signs.", teamTypeId: uid(K_TEAM_TYPE, 1), effectiveFrom: day(-540), status: "active", version: 2 },
  { id: uid(K_TEAM, 4), code: "KA-COASTAL", name: "Coastal Desalination account team", description: "Sales, service and collections for the Corpus Christi plant.", teamTypeId: uid(K_TEAM_TYPE, 2), effectiveFrom: day(-380), status: "active", version: 4 },
  { id: uid(K_TEAM, 5), code: "ERP-CUTOVER", name: "ERP cutover project", description: "Data migration and go-live; disbanded after the first year-end close.", teamTypeId: uid(K_TEAM_TYPE, 3), effectiveFrom: day(-820), effectiveTo: day(-300), status: "inactive", version: 5 },
]);

const member = (n: number, memberKind: "user" | "party", memberId: string, memberRole: string, validFromDaysAgo: number, extra: Partial<TeamMember> = {}): TeamMember => ({ id: uid(K_MEMBER, n), memberKind, memberId, memberRole, validFrom: day(-validFromDaysAgo), source: "manual", ...extra });
const u = (username: string) => byUsername(username).id;
const coastal = CUSTOMERS.find((c) => c.number === "C-10063")!;

const members = new Map<string, TeamMember[]>([
  [uid(K_TEAM, 1), [member(1, "user", u("jordan.avery"), "lead", 900), member(2, "user", u("ana.ortiz"), "member", 900), member(3, "user", u("dana.kim"), "member", 820), member(4, "user", u("maya.chen"), "member", 410)]],
  [uid(K_TEAM, 2), [member(5, "user", u("marcus.lee"), "lead", 700), member(6, "user", u("jordan.avery"), "member", 700)]],
  [uid(K_TEAM, 3), [member(7, "user", u("marcus.lee"), "lead", 540), member(8, "user", u("dana.kim"), "member", 540), member(9, "user", u("kevin.brandt"), "member", 540), member(10, "user", u("jordan.avery"), "member", 300)]],
  [uid(K_TEAM, 4), [member(11, "user", u("dana.whitfield"), "lead", 380), member(12, "user", u("maya.chen"), "member", 380), member(13, "party", coastal.id, "member", 380, { source: "rule" })]],
  [uid(K_TEAM, 5), [member(14, "user", u("tom.becker"), "lead", 820, { validTo: day(-300) }), member(15, "user", u("brian.kessler"), "member", 820, { validTo: day(-300) })]],
]);

route("GET", `${ORG}/team-types`, () => ({ items: types.all() }));
route("POST", `${ORG}/team-types`, ({ body }) => {
  const input = (body ?? {}) as Partial<TeamType>;
  return reply.created(types.insert({ id: newId(), code: (input.code ?? "").toUpperCase(), name: input.name ?? "", memberKinds: input.memberKinds ?? ["user"] }));
});
route("GET", `${ORG}/teams`, () => ({ items: teams.all() }));
route("POST", `${ORG}/teams`, ({ body }) => {
  const input = (body ?? {}) as Partial<Team>;
  return reply.created(teams.insert({ id: newId(), code: (input.code ?? "").toUpperCase(), name: input.name ?? "", ...(input.description ? { description: input.description } : {}), teamTypeId: input.teamTypeId ?? uid(K_TEAM_TYPE, 3), effectiveFrom: input.effectiveFrom ?? day(0), status: "active", version: 1 }));
});
route("GET", `${ORG}/teams/{id}/members`, ({ params }) => ({ items: members.get(params.id) ?? [] }));
route("POST", `${ORG}/teams/{id}/members`, ({ params, body }) => {
  const input = (body ?? {}) as Partial<TeamMember>;
  const row: TeamMember = { id: newId(), memberKind: input.memberKind ?? "user", memberId: input.memberId ?? "", memberRole: input.memberRole ?? "member", validFrom: input.validFrom ?? day(0), ...(input.validTo ? { validTo: input.validTo } : {}), source: "manual" };
  members.set(params.id, [...(members.get(params.id) ?? []), row]);
  return reply.created(row);
});
route("GET", `${ORG}/teams/{id}`, ({ params }) => teams.get(params.id) ?? reply.problem(404, "ORG-0404", "No team with that id"));
route("PATCH", `${ORG}/teams/{id}`, ({ params, body }) => {
  const t = teams.get(params.id);
  if (!t) return reply.problem(404, "ORG-0404", "No team with that id");
  const { version, ...patch } = (body ?? {}) as Partial<Team>;
  if (version !== undefined && version !== t.version) return reply.problem(409, "ORG-VERSION-CONFLICT", "the team changed since you opened it");
  return teams.update(t.id, { ...patch, version: t.version + 1 });
});
