import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { validateActivity, type ActivityValues } from "./ActivitiesPage";
import { emptyArticle, helpfulText, validateArticle } from "./KnowledgePage";
import service from "./module";
import { defaultWarranty, validateObject, validateShipment, warrantyActive, type ObjectValues } from "./ServiceObjectsPage";

const CUSTOMER = "11111111-0000-4000-8000-000000000001";
const LE = "22222222-0000-4000-8000-000000000002";
const ITEM = "33333333-0000-4000-8000-000000000003";
const OBJECT = "44444444-0000-4000-8000-000000000004";
const CASE = "55555555-0000-4000-8000-000000000005";
const OWNER = "66666666-0000-4000-8000-000000000006";
const ACTIVITY = "77777777-0000-4000-8000-000000000007";
const ARTICLE = "88888888-0000-4000-8000-000000000008";
const AUTHOR = "99999999-0000-4000-8000-000000000009";
const SHIPMENT = "aaaaaaaa-0000-4000-8000-00000000000a";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {service.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method !== "GET" && c.url.pathname.endsWith(suffix));
const empty = { status: 200, body: { items: [] } };

function unit(overrides: Record<string, unknown> = {}) {
  return {
    id: OBJECT,
    itemId: ITEM,
    serial: "SN-001",
    customerPartyId: CUSTOMER,
    legalEntityId: LE,
    shipmentId: SHIPMENT,
    shippedOn: "2026-01-10",
    status: "ACTIVE",
    warranty: { startTrigger: "SHIP", months: 12, partsCovered: true, laborCovered: false, travelCovered: false },
    warrantyStartsOn: "2026-01-10",
    warrantyEndsOn: "2027-01-10",
    createdAt: "2026-01-10T10:00:00Z",
    version: 1,
    ...overrides,
  };
}

function activity(overrides: Record<string, unknown> = {}) {
  return {
    id: ACTIVITY,
    kind: "TASK",
    subject: "Call the customer back",
    ownerWorkerId: OWNER,
    dueAt: "2026-10-27T14:00:00Z",
    status: "OPEN",
    associations: [{ entityType: "case", entityId: CASE }],
    createdAt: "2026-10-26T09:00:00Z",
    version: 1,
    ...overrides,
  };
}

function article(overrides: Record<string, unknown> = {}) {
  return {
    id: ARTICLE,
    number: "KB-000004",
    version: 1,
    language: "en",
    title: "Pump noise at start-up",
    symptom: "Grinding noise when the pump starts",
    cause: "Worn bearing",
    solution: "Replace the bearing",
    applicability: { itemIds: [ITEM], itemGroupIds: [], incidentTypeIds: [], assetTypeIds: [], categoryIds: [] },
    status: "DRAFT",
    authorId: AUTHOR,
    helpfulYes: 0,
    helpfulNo: 0,
    createdAt: "2026-10-26T09:00:00Z",
    revision: 1,
    ...overrides,
  };
}

const objectValues = (overrides: Partial<ObjectValues> = {}): ObjectValues => ({
  itemId: ITEM,
  serial: "SN-001",
  customerPartyId: CUSTOMER,
  legalEntityId: LE,
  parentId: "",
  shippedOn: "2026-01-10",
  installedOn: "",
  warranty: defaultWarranty(),
  ...overrides,
});

describe("installed base", () => {
  it("validates a unit like the service does", () => {
    expect(validateObject(objectValues()).input?.warranty).toEqual({ startTrigger: "SHIP", months: 12, partsCovered: true, laborCovered: false, travelCovered: false });
    expect(validateObject(objectValues({ shippedOn: "" })).errors.shippedOn).toMatch(/ship date/);
    expect(validateObject(objectValues({ shippedOn: "", warranty: { ...defaultWarranty(), startTrigger: "INSTALL" } })).input).toBeDefined();
    expect(validateObject(objectValues({ serial: " SN" })).errors.serial).toBeDefined();
    expect(validateObject(objectValues({ warranty: { ...defaultWarranty(), months: "241" } })).errors.warranty).toBeDefined();
    const shipment = validateShipment({ shipmentId: SHIPMENT, shipDate: "2026-10-23", customerPartyId: CUSTOMER, legalEntityId: LE, itemId: ITEM, serials: "A\nA", warranty: defaultWarranty() });
    expect(shipment.errors.serials).toMatch(/once/);
  });

  it("reads the warranty as active only between its start and its end", () => {
    expect(warrantyActive({ warrantyStartsOn: "2026-01-10", warrantyEndsOn: "2027-01-10" }, "2027-01-09")).toBe(true);
    expect(warrantyActive({ warrantyStartsOn: "2026-01-10", warrantyEndsOn: "2027-01-10" }, "2027-01-10")).toBe(false);
    expect(warrantyActive({}, "2026-10-26")).toBeNull();
  });

  it("lists units with their warranty and opens one", async () => {
    handler = (url) => (url.pathname === "/api/v1/service/service-objects" ? { status: 200, body: { items: [unit()] } } : { status: 200, body: unit(), etag: '"1"' });
    const { container } = renderAt("/service/objects");
    const table = await findTable({ name: "Service objects" });
    expect(within(table).getByText("SN-001")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByText("SN-001"));
    expect(await screen.findByText("Service object SN-001")).toBeInTheDocument();
  });

  it("shows the empty state and a failed load with a retry", async () => {
    handler = () => empty;
    renderAt("/service/objects");
    expect(await screen.findByText("No service objects found")).toBeInTheDocument();
    cleanupAndRender("/service/objects", () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } }));
    expect(await findAlert()).toHaveTextContent("Could not load service objects");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("registers a unit with an Idempotency-Key and lands on it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: unit(), etag: '"1"' } : { status: 200, body: unit(), etag: '"1"' });
    renderAt("/service/objects/new");
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Serial number"), { target: { value: "SN-001" } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/Shipped on/), { target: { value: "2026-01-10" } });
    fireEvent.click(screen.getByRole("button", { name: "Register unit" }));
    expect(await screen.findByText("Service object SN-001")).toBeInTheDocument();
    const [created] = posts("/service-objects");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toMatchObject({ itemId: ITEM, serial: "SN-001", shippedOn: "2026-01-10", warranty: { startTrigger: "SHIP", months: 12 } });
  });

  it("receives a shipment with one serial per line and reports what it created", async () => {
    handler = () => ({ status: 200, body: { created: 2, existing: 1 } });
    renderAt("/service/objects/receive");
    fireEvent.change(screen.getByLabelText("Shipment"), { target: { value: SHIPMENT } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Serial numbers"), { target: { value: "A1\n A2 \n\nA3" } });
    fireEvent.click(screen.getByRole("button", { name: "Create units" }));
    expect(await findStatus()).toHaveTextContent("2 units created, 1 already registered.");
    expect(posts(":receive-shipment")[0]?.body).toMatchObject({ lines: [{ itemId: ITEM, serials: ["A1", "A2", "A3"] }] });
  });

  it("records the installation with If-Match and shows when the warranty starts", async () => {
    const pending = unit({ shippedOn: undefined, warranty: { startTrigger: "INSTALL", months: 24, partsCovered: true, laborCovered: true, travelCovered: false }, warrantyStartsOn: undefined, warrantyEndsOn: undefined });
    handler = (url, init) => {
      if (init.method === "POST") return { status: 200, body: unit({ ...pending, installedOn: "2026-10-20", warrantyStartsOn: "2026-10-20", warrantyEndsOn: "2028-10-20", version: 2 }), etag: '"2"' };
      if (url.pathname.endsWith("/service-objects") || url.pathname.endsWith("/activities")) return empty;
      return { status: 200, body: pending, etag: '"1"' };
    };
    renderAt(`/service/objects/${OBJECT}`);
    expect(await screen.findByText("Not started")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Installed on"), { target: { value: "2026-10-20" } });
    fireEvent.click(screen.getByRole("button", { name: "Record installation" }));
    expect(await findStatus()).toHaveTextContent("Installed; warranty runs from 2026-10-20.");
    const [install] = posts(":install");
    expect(install?.headers.get("If-Match")).toBe('"1"');
    expect(install?.body).toEqual({ installedOn: "2026-10-20" });
    expect(screen.queryByRole("button", { name: "Record installation" })).not.toBeInTheDocument();
  });
});

function cleanupAndRender(path: string, next: Handler) {
  document.body.innerHTML = "";
  handler = next;
  return renderAt(path);
}

const activityValues = (overrides: Partial<ActivityValues> = {}): ActivityValues => ({
  kind: "TASK",
  subject: "Call back",
  ownerWorkerId: OWNER,
  dueAt: "2026-10-27T14:00",
  startAt: "",
  endAt: "",
  reminderMinutes: "",
  entityType: "case",
  entityId: CASE,
  ...overrides,
});

describe("activities", () => {
  it("validates an activity like the service does and reads times as UTC", () => {
    expect(validateActivity(activityValues()).input).toEqual({
      kind: "TASK",
      subject: "Call back",
      ownerWorkerId: OWNER,
      dueAt: "2026-10-27T14:00:00Z",
      associations: [{ entityType: "case", entityId: CASE }],
    });
    expect(validateActivity(activityValues({ startAt: "2026-10-27T10:00", endAt: "2026-10-27T09:00" })).errors.endAt).toBeDefined();
    expect(validateActivity(activityValues({ reminderMinutes: "20000" })).errors.reminderMinutes).toBeDefined();
    expect(validateActivity(activityValues({ entityType: "", entityId: CASE })).errors.entityType).toBeDefined();
  });

  it("asks for the owner, then lists their open activities with the related record and a calendar link", async () => {
    handler = () => ({ status: 200, body: { items: [activity()] } });
    const { container } = renderAt("/service/activities");
    expect(screen.getByText(/Enter the owner/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: OWNER } });
    const table = await findTable({ name: "My activities" });
    expect(within(table).getByText("Call the customer back")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Case 55555555" })).toHaveAttribute("href", `/service/cases/${CASE}`);
    expect(calls[0]?.url.searchParams.get("owner")).toBe(OWNER);
    expect(calls[0]?.url.searchParams.get("status")).toBe("OPEN");
    expect(screen.getByRole("link", { name: "Download calendar" }).getAttribute("href")).toContain(`activities:calendar?owner=${OWNER}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates an activity on the record it was opened from", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: activity(), etag: '"1"' } : { status: 200, body: activity(), etag: '"1"' });
    renderAt(`/service/activities/new?entityType=case&entityId=${CASE}&owner=${OWNER}`);
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "Call the customer back" } });
    fireEvent.click(screen.getByRole("button", { name: "Create activity" }));
    expect(await screen.findByRole("heading", { name: "Call the customer back" })).toBeInTheDocument();
    const [created] = posts("/activities");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toMatchObject({ kind: "TASK", ownerWorkerId: OWNER, associations: [{ entityType: "case", entityId: CASE }] });
  });

  it("marks an activity done with If-Match and offers no more moves", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 200, body: activity({ status: "DONE", completedAt: "2026-10-27T15:00:00Z", version: 2 }), etag: '"2"' } : { status: 200, body: activity(), etag: '"1"' };
    renderAt(`/service/activities/${ACTIVITY}`);
    fireEvent.click(await screen.findByRole("button", { name: "Mark done" }));
    expect(await findStatus()).toHaveTextContent("Activity done.");
    expect(posts(":complete")[0]?.headers.get("If-Match")).toBe('"1"');
    expect(screen.getByText("2026-10-27 15:00 UTC")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark done" })).not.toBeInTheDocument();
  });

  it("reschedules in UTC and reloads after a version conflict", async () => {
    let reads = 0;
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 412, body: { code: "conflict_version", detail: "current version is 2" } };
      reads++;
      return { status: 200, body: activity(), etag: '"1"' };
    };
    renderAt(`/service/activities/${ACTIVITY}`);
    const due = await screen.findByLabelText(/^Due/, { selector: "#reschedule-dueAt" });
    fireEvent.change(due, { target: { value: "2026-10-28T09:30" } });
    fireEvent.click(screen.getByRole("button", { name: "Reschedule" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this activity");
    expect(posts(":reschedule")[0]?.body).toEqual({ dueAt: "2026-10-28T09:30:00Z", startAt: null, endAt: null });
    await waitFor(() => expect(reads).toBe(2));
  });
});

describe("knowledge base", () => {
  it("validates an article like the service does and keeps the applicability it does not edit", () => {
    const keep = { itemIds: [], itemGroupIds: [], incidentTypeIds: [ITEM], assetTypeIds: [], categoryIds: [] };
    const ok = validateArticle({ ...emptyArticle(), title: " Pump noise ", solution: "Replace the bearing", itemIds: `${ITEM}\n` }, keep);
    expect(ok.input).toMatchObject({ title: "Pump noise", language: "en", applicability: { itemIds: [ITEM], incidentTypeIds: [ITEM] } });
    expect(validateArticle({ ...emptyArticle(), title: "T" }).errors.solution).toBeDefined();
    expect(validateArticle({ ...emptyArticle(), title: "T", solution: "S", language: "english" }).errors.language).toBeDefined();
    expect(validateArticle({ ...emptyArticle(), title: "T", solution: "S", itemIds: "not-a-uuid" }).errors.itemIds).toBeDefined();
  });

  it("shows the helpful share only from ten votes", () => {
    expect(helpfulText({ helpfulYes: 3, helpfulNo: 1 })).toBe("4 votes (shown from 10)");
    expect(helpfulText({ helpfulYes: 9, helpfulNo: 1, helpfulPercent: "90" })).toBe("90 % of 10 votes");
    expect(helpfulText({ helpfulYes: 0, helpfulNo: 0 })).toBe("No votes yet");
  });

  it("lists articles by status and searches the published ones", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/search")) return { status: 200, body: { items: [{ article: article({ status: "PUBLISHED" }), score: 0.85 }] } };
      return { status: 200, body: { items: [article()] } };
    };
    const { container } = renderAt("/service/knowledge");
    const table = await findTable({ name: "Knowledge articles" });
    expect(within(table).getByText("KB-000004 v1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PUBLISHED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("PUBLISHED"));
    fireEvent.change(screen.getByLabelText(/Search published articles/), { target: { value: "pump grinding" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    const results = await findTable({ name: "Search results" });
    expect(within(results).getByText("0.85")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("q")).toBe("pump grinding");
  });

  it("shows the empty state and a failed load with a retry", async () => {
    handler = () => empty;
    renderAt("/service/knowledge");
    expect(await screen.findByText("No articles")).toBeInTheDocument();
    cleanupAndRender("/service/knowledge", () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } }));
    expect(await findAlert()).toHaveTextContent("Could not load knowledge articles");
  });

  it("saves a new article as a draft with an Idempotency-Key and lands on it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: article(), etag: '"1"' };
      if (url.pathname.endsWith("/knowledge/articles")) return { status: 200, body: { items: [article()] } };
      return { status: 200, body: article(), etag: '"1"' };
    };
    renderAt("/service/knowledge/new");
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Pump noise at start-up" } });
    fireEvent.change(screen.getByLabelText("Solution"), { target: { value: "Replace the bearing" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByRole("heading", { name: "Pump noise at start-up" })).toBeInTheDocument();
    const [created] = posts("/knowledge/articles");
    expect(created?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(created?.body).toMatchObject({ title: "Pump noise at start-up", language: "en", solution: "Replace the bearing" });
  });

  it("edits a draft with If-Match and submits it for review", async () => {
    let current = article();
    handler = (url, init) => {
      if (init.method === "PUT") current = article({ title: "Pump noise", revision: 2 });
      if (url.pathname.endsWith(":submit")) current = article({ title: "Pump noise", status: "IN_REVIEW", revision: 3 });
      if (url.pathname.endsWith("/knowledge/articles")) return { status: 200, body: { items: [current] } };
      return { status: 200, body: current, etag: `"${current.revision}"` };
    };
    renderAt(`/service/knowledge/${ARTICLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit draft" }));
    expect(screen.getByLabelText("Language")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Pump noise" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await findStatus()).toHaveTextContent("Draft saved.");
    expect(calls.find((c) => c.method === "PUT")?.headers.get("If-Match")).toBe('"1"');
    fireEvent.click(screen.getByRole("button", { name: "Submit for review" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Submitted for review."));
    expect(posts(":submit")[0]?.headers.get("If-Match")).toBe('"2"');
    expect(screen.getByRole("button", { name: "Publish" })).toBeInTheDocument();
  });

  it("explains KB-PUB-001 when the author tries to publish", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 422, body: { code: "KB-PUB-001", detail: "the reviewer must differ from the author" } };
      if (url.pathname.endsWith("/knowledge/articles")) return empty;
      return { status: 200, body: article({ status: "IN_REVIEW", revision: 3 }), etag: '"3"' };
    };
    renderAt(`/service/knowledge/${ARTICLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Publish" }));
    expect(await findAlert()).toHaveTextContent("another reviewer must publish it");
  });

  it("publishes and reports the version it retired (KB-PUB-002)", async () => {
    handler = (url, init) => {
      if (init.method === "POST")
        return { status: 200, body: { article: article({ version: 2, status: "PUBLISHED", reviewerId: OWNER, publishedAt: "2026-10-27T10:00:00Z", revision: 4 }), retiredVersion: 1, warnings: ["KB-PUB-002: publishing retires version 1"] }, etag: '"4"' };
      if (url.pathname.endsWith("/knowledge/articles")) return empty;
      return { status: 200, body: article({ version: 2, status: "IN_REVIEW", revision: 3 }), etag: '"3"' };
    };
    renderAt(`/service/knowledge/${ARTICLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Publish" }));
    expect(await findStatus()).toHaveTextContent("Published. KB-PUB-002: publishing retires version 1");
    expect(posts(":publish")[0]?.headers.get("If-Match")).toBe('"3"');
    expect(screen.getByRole("button", { name: "Helpful" })).toBeInTheDocument();
  });

  it("votes on a published article and asks before retiring it", async () => {
    handler = (url) => {
      if (url.pathname.endsWith(":vote")) return { status: 200, body: article({ status: "PUBLISHED", helpfulYes: 1, revision: 4 }), etag: '"4"' };
      if (url.pathname.endsWith(":retire")) return { status: 200, body: article({ status: "RETIRED", revision: 5 }), etag: '"5"' };
      if (url.pathname.endsWith("/knowledge/articles")) return empty;
      return { status: 200, body: article({ status: "PUBLISHED", revision: 4 }), etag: '"4"' };
    };
    renderAt(`/service/knowledge/${ARTICLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Helpful" }));
    expect(await findStatus()).toHaveTextContent("your vote counts as helpful");
    expect(posts(":vote")[0]?.body).toEqual({ helpful: true });
    fireEvent.click(screen.getByRole("button", { name: "Retire" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Retire this article?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep it published" }));
    expect(posts(":retire")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Retire" }));
    fireEvent.click(within(await screen.findByRole("alertdialog", { name: "Retire this article?" })).getByRole("button", { name: "Retire" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Retired"));
    expect(posts(":retire")[0]?.headers.get("If-Match")).toBe('"4"');
  });

  it("starts a new version and lands on it", async () => {
    const NEXT = "bbbbbbbb-0000-4000-8000-00000000000b";
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: article({ id: NEXT, version: 2, revision: 1 }), etag: '"1"' };
      if (url.pathname.endsWith("/knowledge/articles")) return empty;
      if (url.pathname.endsWith(NEXT)) return { status: 200, body: article({ id: NEXT, version: 2 }), etag: '"1"' };
      return { status: 200, body: article({ status: "RETIRED", revision: 6 }), etag: '"6"' };
    };
    renderAt(`/service/knowledge/${ARTICLE}`);
    fireEvent.click(await screen.findByRole("button", { name: "New version" }));
    expect(await screen.findByText("KB-000004, version 2, en")).toBeInTheDocument();
    expect(posts(":new-version")[0]?.headers.get("If-Match")).toBe('"6"');
  });
});

describe("case detail (slice 3)", () => {
  const serviceCase = {
    id: CASE,
    number: "CS-000042",
    customerPartyId: CUSTOMER,
    legalEntityId: LE,
    serviceObjectId: OBJECT,
    channel: "PHONE",
    priority: 2,
    description: "The pump makes a grinding noise",
    slaProfileId: ITEM,
    entitlementTrace: [],
    openedAt: "2026-10-23T21:00:00Z",
    slaResponseDue: "2026-10-23T22:00:00Z",
    slaResponseWarnAt: "2026-10-23T21:45:00Z",
    slaResolutionDue: "2026-10-26T19:00:00Z",
    slaResolutionWarnAt: "2026-10-26T17:00:00Z",
    slaPausedMinutes: 0,
    slaState: "RUNNING",
    slaAlerts: [],
    status: "NEW",
    version: 1,
  };

  it("suggests articles for the description and the unit's item, and lists the case's activities (AC-45.12, AC-45.15)", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/search")) return { status: 200, body: { items: [{ article: article({ status: "PUBLISHED" }), score: 0.85 }] } };
      if (url.pathname.endsWith(`/service-objects/${OBJECT}`)) return { status: 200, body: unit(), etag: '"1"' };
      if (url.pathname.endsWith("/activities")) return { status: 200, body: { items: [activity()] } };
      if (url.pathname.endsWith("/work-orders")) return empty;
      return { status: 200, body: serviceCase, etag: '"1"' };
    };
    renderAt(`/service/cases/${CASE}`);
    const suggested = await findTable({ name: "Suggested articles" });
    expect(within(suggested).getByText("Pump noise at start-up")).toBeInTheDocument();
    const search = calls.find((c) => c.url.pathname.endsWith("/search"));
    expect(search?.url.searchParams.get("q")).toBe("The pump makes a grinding noise");
    expect(search?.url.searchParams.get("itemId")).toBe(ITEM);
    const activities = await findTable({ name: "Case activities" });
    expect(within(activities).getByText("Call the customer back")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/activities"))?.url.searchParams.get("entityId")).toBe(CASE);
    expect(screen.getByRole("link", { name: "44444444" })).toHaveAttribute("href", `/service/objects/${OBJECT}`);
  });

  it("prefills a new case from the unit it was opened from", () => {
    handler = () => empty;
    renderAt(`/service/cases/new?customerPartyId=${CUSTOMER}&legalEntityId=${LE}&serviceObjectId=${OBJECT}`);
    expect(screen.getByLabelText(/Service object/)).toHaveValue(OBJECT);
    expect(screen.getByLabelText("Customer")).toHaveValue(CUSTOMER);
  });
});
