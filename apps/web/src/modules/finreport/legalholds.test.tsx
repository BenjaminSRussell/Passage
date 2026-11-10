import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LegalHold, LibraryEntry, LibrarySweep, RunSweep } from "./api/client";
import { validateHold } from "./LegalHoldsPage";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ENTITY = "0190f0a0-0000-7000-8000-0000000000e1";
const HOLD_ID = "0190f0a0-0000-7000-8000-000000000c01";
const DUTY = "reporting.library.hold";

const active: LegalHold = {
  id: HOLD_ID,
  legalEntityId: ENTITY,
  reason: "Audit 2026-17",
  placedBy: "u-legal",
  placedAt: "2026-05-01T09:00:00Z",
  active: true,
  revision: 1,
};
const released: LegalHold = { ...active, id: "0190f0a0-0000-7000-8000-000000000c02", active: false, releasedBy: "u-legal", releasedAt: "2026-06-01T09:00:00Z", releaseNote: "Matter closed", revision: 2 };
const sweep: LibrarySweep = {
  id: "0190f0a0-0000-7000-8000-000000000d01",
  legalEntityId: ENTITY,
  sweptAt: "2026-11-08T04:00:00Z",
  cutoff: "2026-11-08T04:00:00Z",
  deletedCount: 12,
  deletedBytes: 2048,
  heldCount: 3,
  actor: "system:finreport-library-sweep",
};

const runSweep: RunSweep = {
  id: "0190f0a0-0000-7000-8000-000000000d02",
  legalEntityId: ENTITY,
  sweptAt: "2026-11-08T04:00:01Z",
  cutoff: "2026-08-10T04:00:00Z",
  retentionDays: 90,
  deletedRuns: 7,
  deletedCells: 1234,
  heldRuns: 2,
  actor: "system:finreport-library-sweep",
};

describe("hold rules", () => {
  it("accepts an entity id and a reason", () => {
    expect(validateHold({ legalEntityId: ` ${ENTITY} `, reason: "Audit" })).toEqual({});
  });
  it("names the fields the service would refuse", () => {
    expect(Object.keys(validateHold({ legalEntityId: "nope", reason: "  " })).sort()).toEqual(["legalEntityId", "reason"]);
    expect(validateHold({ legalEntityId: ENTITY, reason: "x".repeat(501) }).reason).toMatch(/500/);
  });
});

describe("LegalHoldsPage", () => {
  it("lists the holds and the sweep passes", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/legal-holds") return json(200, { items: [active, released] });
      if (url.pathname === "/api/v1/reporting/library-sweeps") return json(200, { items: [sweep] });
      if (url.pathname === "/api/v1/reporting/run-sweeps") return json(200, { items: [runSweep] });
      return undefined;
    };
    const { container } = renderAt("/finreport/legal-holds");
    const holds = await findTable({ name: "Legal holds" });
    expect(await within(holds).findAllByText("Audit 2026-17")).toHaveLength(2);
    expect(within(holds).getByText("In force")).toBeInTheDocument();
    expect(within(holds).getByText("Released")).toBeInTheDocument();
    const sweeps = await findTable({ name: "Retention sweeps" });
    expect(within(sweeps).getByText("12")).toBeInTheDocument();
    expect(within(sweeps).getByText("2.0 KB")).toBeInTheDocument();
    const runs = await findTable({ name: "Ad-hoc run sweeps" });
    expect(within(runs).getByText("90")).toBeInTheDocument();
    expect(within(runs).getByText("7")).toBeInTheDocument();
    expect(within(runs).getByText("1234")).toBeInTheDocument();
    // Without the hold duty there is no form to put a library on hold.
    expect(screen.queryByRole("form", { name: "Put a library on hold" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state and a retry on error", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/legal-holds") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/library-sweeps") return problem(500, "COM-0500", "sweeps are down");
      if (url.pathname === "/api/v1/reporting/run-sweeps") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport/legal-holds");
    expect(await screen.findByText("No legal hold has been placed")).toBeInTheDocument();
    const alert = await findAlert();
    expect(alert).toHaveTextContent("sweeps are down (COM-0500)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(await screen.findByText("No ad-hoc run has passed its retention")).toBeInTheDocument();
  });

  it("shows a retry when the ad-hoc run sweeps cannot be loaded", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/legal-holds") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/library-sweeps") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/run-sweeps") return problem(500, "COM-0500", "run sweeps are down");
      return undefined;
    };
    renderAt("/finreport/legal-holds");
    expect(await findAlert()).toHaveTextContent("run sweeps are down (COM-0500)");
    expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("places a hold with an idempotency key and opens it", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/legal-holds" && init.method === "POST") return json(201, active, { ETag: '"1"' });
      if (url.pathname === "/api/v1/reporting/legal-holds") return json(200, { items: [] });
      if (url.pathname === `/api/v1/reporting/legal-holds/${HOLD_ID}`) return json(200, active, { ETag: '"1"' });
      if (url.pathname === "/api/v1/reporting/library-sweeps") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/run-sweeps") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport/legal-holds", [DUTY]);
    const form = await screen.findByRole("form", { name: "Put a library on hold" });
    fireEvent.click(within(form).getByRole("button", { name: "Place hold" }));
    expect(await within(form).findByText("Enter the legal entity id as a UUID.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText(/^Legal entity/), { target: { value: ENTITY.toUpperCase() } });
    fireEvent.change(within(form).getByLabelText(/^Reason/), { target: { value: " Audit 2026-17 " } });
    fireEvent.click(within(form).getByRole("button", { name: "Place hold" }));
    expect(await screen.findByText("Details")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ legalEntityId: ENTITY, reason: "Audit 2026-17" });
    expect(header(post!, "Idempotency-Key")).toMatch(/\S+/);
  });

  it("shows the service's refusal when the entity is already held", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/reporting/legal-holds" && init.method === "POST") return problem(409, "REPORT-0045", "already under legal hold");
      if (url.pathname === "/api/v1/reporting/legal-holds") return json(200, { items: [active] });
      if (url.pathname === "/api/v1/reporting/library-sweeps") return json(200, { items: [] });
      if (url.pathname === "/api/v1/reporting/run-sweeps") return json(200, { items: [] });
      return undefined;
    };
    renderAt("/finreport/legal-holds", [DUTY]);
    const form = await screen.findByRole("form", { name: "Put a library on hold" });
    fireEvent.change(within(form).getByLabelText(/^Legal entity/), { target: { value: ENTITY } });
    fireEvent.change(within(form).getByLabelText(/^Reason/), { target: { value: "Again" } });
    fireEvent.click(within(form).getByRole("button", { name: "Place hold" }));
    expect(await findAlert()).toHaveTextContent("already under legal hold (REPORT-0045)");
  });
});

describe("LegalHoldPage", () => {
  const detail = (hold: LegalHold) => (url: URL, init: RequestInit) => {
    if (url.pathname === `/api/v1/reporting/legal-holds/${HOLD_ID}:release`) {
      return init.method === "POST" ? json(200, { ...hold, active: false, releasedBy: "u-legal", releasedAt: "2026-07-01T09:00:00Z", releaseNote: "Matter closed", revision: 2 }, { ETag: '"2"' }) : undefined;
    }
    if (url.pathname === `/api/v1/reporting/legal-holds/${HOLD_ID}`) return json(200, hold, { ETag: `"${hold.revision}"` });
    return undefined;
  };

  it("releases the hold with the revision it read", async () => {
    server.handler = detail(active);
    const { container } = renderAt(`/finreport/legal-holds/${HOLD_ID}`, [DUTY]);
    expect(await screen.findByText("Audit 2026-17")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const form = screen.getByRole("form", { name: "Release the hold" });
    fireEvent.change(within(form).getByLabelText(/^Release note/), { target: { value: "Matter closed" } });
    fireEvent.click(within(form).getByRole("button", { name: "Release hold" }));
    expect(await screen.findByText("Matter closed")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("form", { name: "Release the hold" })).toBeNull());
    const post = server.calls.find((c) => c.method === "POST");
    expect(header(post!, "If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ note: "Matter closed" });
  });

  it("shows the conflict when the hold changed meanwhile", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return problem(412, "conflict_version", "legal hold revision is 2, not 1");
      return detail(active)(url, init);
    };
    renderAt(`/finreport/legal-holds/${HOLD_ID}`, [DUTY]);
    const form = await screen.findByRole("form", { name: "Release the hold" });
    fireEvent.click(within(form).getByRole("button", { name: "Release hold" }));
    expect(await findAlert()).toHaveTextContent("conflict_version");
  });

  it("offers no release without the duty, and none for a released hold", async () => {
    server.handler = detail(active);
    renderAt(`/finreport/legal-holds/${HOLD_ID}`);
    expect(await screen.findByText("Audit 2026-17")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Release the hold" })).toBeNull();
  });

  it("shows a released hold with its note", async () => {
    server.handler = (url) => (url.pathname.endsWith(released.id) ? json(200, released) : undefined);
    renderAt(`/finreport/legal-holds/${released.id}`, [DUTY]);
    expect(await screen.findByText("Matter closed")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Release the hold" })).toBeNull();
  });

  it("says when the hold does not exist and when it cannot be loaded", async () => {
    server.handler = () => problem(404, "REPORT-0047", "legal hold does not exist");
    renderAt(`/finreport/legal-holds/${HOLD_ID}`, [DUTY]);
    expect(await findAlert()).toHaveTextContent("REPORT-0047");
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });

  it("offers a retry when the load fails", async () => {
    server.handler = () => problem(500, "COM-0500", "down");
    renderAt(`/finreport/legal-holds/${HOLD_ID}`, [DUTY]);
    expect(await findAlert()).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("library file under hold", () => {
  it("says the file is kept past its retention date", async () => {
    const entry: LibraryEntry = {
      id: "0190f0a0-0000-7000-8000-000000000b01", runId: "0190f0a0-0000-7000-8000-000000000a01", scheduleCode: "NIGHTLY", definitionCode: "BS-STD",
      definitionVersion: 2, ledgerId: "0190f0a0-0000-7000-8000-000000000201", legalEntityId: ENTITY, periodYear: 2026, periodNo: 3, currencyType: "acct",
      format: "csv", fileName: "BS-STD.csv", contentType: "text/csv", sizeBytes: 10, sha256: "ab".repeat(32), retainUntil: "2026-04-01T02:00:00Z",
      createdAt: "2026-03-01T02:00:00Z", onHold: true,
    };
    server.handler = (url) => (url.pathname === `/api/v1/reporting/library/${entry.id}` ? json(200, entry) : undefined);
    renderAt(`/finreport/library/${entry.id}`);
    expect(await screen.findByText(/kept past this date: legal hold/)).toBeInTheDocument();
  });
});
