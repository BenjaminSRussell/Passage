import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LibraryEntry } from "./api/client";
import { formatBytes } from "./format";
import { blankLibraryValues, toLibraryQuery, validateLibrary } from "./LibraryPage";
import { json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190f0a0-0000-7000-8000-000000000201";
const SHA = "ab".repeat(32);

const csv: LibraryEntry = {
  id: "0190f0a0-0000-7000-8000-000000000b01",
  runId: "0190f0a0-0000-7000-8000-000000000a01",
  scheduleCode: "NIGHTLY",
  definitionCode: "BS-STD",
  definitionVersion: 2,
  ledgerId: LEDGER,
  legalEntityId: "0190f0a0-0000-7000-8000-0000000000e1",
  periodYear: 2026,
  periodNo: 3,
  currencyType: "acct",
  format: "csv",
  fileName: "BS-STD-0190f0a0-2026-03.csv",
  contentType: "text/csv; charset=utf-8",
  sizeBytes: 2048,
  sha256: SHA,
  retainUntil: "2033-04-01T02:00:00Z",
  createdAt: "2026-04-01T02:00:04Z",
  onHold: false,
};
const xlsx: LibraryEntry = { ...csv, id: "0190f0a0-0000-7000-8000-000000000b02", format: "xlsx", fileName: "BS-STD-0190f0a0-2026-03.xlsx", sizeBytes: 5 * 1024 * 1024 };

describe("library filter rules", () => {
  it("accepts an empty filter and turns the fields into the service's query", () => {
    expect(validateLibrary(blankLibraryValues())).toEqual({});
    expect(toLibraryQuery({ definition: " bs-std ", ledger: ` ${LEDGER} `, schedule: "nightly", format: "csv", period: "2026-03" })).toEqual({
      definition: "BS-STD",
      ledger: LEDGER,
      schedule: "NIGHTLY",
      format: "csv",
      period: "2026-03",
    });
    expect(toLibraryQuery(blankLibraryValues())).toEqual({});
  });

  it("names the fields the service would refuse", () => {
    const errors = validateLibrary({ ...blankLibraryValues(), ledger: "nope", period: "2026-3" });
    expect(Object.keys(errors).sort()).toEqual(["ledger", "period"]);
  });

  it("writes file sizes", () => {
    expect([formatBytes(12), formatBytes(2048), formatBytes(5 * 1024 * 1024)]).toEqual(["12 B", "2.0 KB", "5.0 MB"]);
  });
});

describe("LibraryPage", () => {
  it("lists the files, filters them and loads more", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/reporting/library") return undefined;
      if (url.searchParams.get("cursor") === "c1") return json(200, { items: [xlsx], limit: 50, nextCursor: null });
      if (url.searchParams.get("format") === "xlsx") return json(200, { items: [xlsx], limit: 50, nextCursor: null });
      return json(200, { items: [csv], limit: 50, nextCursor: "c1" });
    };
    const { container } = renderAt("/finreport/library");
    const table = await findTable({ name: "Library files" });
    expect(await within(table).findByText("BS-STD v2")).toBeInTheDocument();
    expect(within(table).getByText("2026-03")).toBeInTheDocument();
    expect(within(table).getByText("2.0 KB")).toBeInTheDocument();
    expect(within(table).getByText("abababababab")).toBeInTheDocument();
    expect(within(table).getByText("2033-04-01")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await within(table).findByText("5.0 MB")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();

    fireEvent.change(screen.getByLabelText(/^Format/), { target: { value: "xlsx" } });
    fireEvent.click(screen.getByRole("button", { name: "Show files" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("format") === "xlsx")).toBe(true));
    await waitFor(() => expect(within(table).queryByText("2.0 KB")).toBeNull());
  });

  it("does not send a filter with field errors", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/reporting/library" ? json(200, { items: [], limit: 50, nextCursor: null }) : undefined);
    renderAt("/finreport/library");
    await screen.findByText("No files in the library");
    fireEvent.change(screen.getByLabelText(/^Period/), { target: { value: "2026-3" } });
    fireEvent.click(screen.getByRole("button", { name: "Show files" }));
    expect(await screen.findByText("Enter the fiscal year and period number, such as 2026-03.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname === "/api/v1/reporting/library")).toHaveLength(1);
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/reporting/library") return undefined;
      return fail ? problem(403, "AUTH-0003", "You need the view duty.") : json(200, { items: [csv], limit: 50, nextCursor: null });
    };
    renderAt("/finreport/library");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("You need the view duty. (AUTH-0003)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("BS-STD v2")).toBeInTheDocument();
  });

  it("opens a file from the list", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/reporting/library") return json(200, { items: [csv], limit: 50, nextCursor: null });
      if (url.pathname === `/api/v1/reporting/library/${csv.id}`) return json(200, csv);
      return undefined;
    };
    renderAt("/finreport/library");
    const table = await findTable({ name: "Library files" });
    fireEvent.click(await within(table).findByRole("button", { name: "BS-STD v2" }));
    expect(await screen.findByRole("heading", { name: csv.fileName })).toBeInTheDocument();
  });
});

describe("LibraryEntryPage", () => {
  it("shows the digest, retention and a download of exactly the stored file", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/reporting/library/${csv.id}` ? json(200, csv) : undefined);
    const { container } = renderAt(`/finreport/library/${csv.id}`);
    expect(await screen.findByRole("heading", { name: csv.fileName })).toBeInTheDocument();
    expect(screen.getByText(SHA)).toBeInTheDocument();
    expect(screen.getByText("2033-04-01T02:00:00Z")).toBeInTheDocument();
    const download = screen.getByRole("link", { name: "Download CSV" });
    expect(download).toHaveAttribute("href", `/api/v1/reporting/library/${csv.id}/content`);
    expect(screen.getByRole("link", { name: "Open the statement" })).toHaveAttribute("href", `/finreport/runs/${csv.runId}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the file is not in the library", async () => {
    server.handler = (url) => (url.pathname.startsWith("/api/v1/reporting/library/") ? problem(404, "REPORT-0043", "report library entry x does not exist") : undefined);
    renderAt(`/finreport/library/${csv.id}`);
    expect(await findAlert()).toHaveTextContent("This file is not in the library");
    expect(screen.getByRole("link", { name: "Back to the library" })).toBeInTheDocument();
  });

  it("offers a retry when loading fails for another reason", async () => {
    server.handler = (url) => (url.pathname.startsWith("/api/v1/reporting/library/") ? problem(500, "COM-0500", "down") : undefined);
    renderAt(`/finreport/library/${csv.id}`);
    expect(within(await findAlert()).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
