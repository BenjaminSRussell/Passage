import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LibraryEntry, ShareLink } from "./api/client";
import { validateShareDays } from "./ShareLinks";
import { header, json, pageViolations, problem, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LIB = "0190f0a0-0000-7000-8000-000000000b01";
const DUTY = "reporting.library.share";
const TOKEN = "0190f0a000007000800000000000000a." + "A".repeat(43);

const entry: LibraryEntry = {
  id: LIB,
  runId: "0190f0a0-0000-7000-8000-000000000a01",
  scheduleCode: "NIGHTLY",
  definitionCode: "BS-STD",
  definitionVersion: 2,
  ledgerId: "0190f0a0-0000-7000-8000-000000000201",
  legalEntityId: "0190f0a0-0000-7000-8000-0000000000e1",
  periodYear: 2026,
  periodNo: 3,
  currencyType: "acct",
  format: "csv",
  fileName: "BS-STD-2026-03.csv",
  contentType: "text/csv; charset=utf-8",
  sizeBytes: 2048,
  sha256: "ab".repeat(32),
  retainUntil: "2033-04-01T02:00:00Z",
  createdAt: "2026-04-01T02:00:04Z",
  onHold: false,
};
const active: ShareLink = {
  id: "0190f0a0-0000-7000-8000-000000000f01",
  libraryId: LIB,
  status: "active",
  createdBy: "u-fin",
  createdAt: "2026-05-01T09:00:00Z",
  expiresAt: "2026-05-15T09:00:00Z",
  downloadCount: 3,
  revision: 1,
};
const expired: ShareLink = { ...active, id: "0190f0a0-0000-7000-8000-000000000f02", status: "expired", expiresAt: "2026-04-10T09:00:00Z", downloadCount: 0 };
const revoked: ShareLink = { ...active, id: "0190f0a0-0000-7000-8000-000000000f03", status: "revoked", revokedBy: "u-fin", revokedAt: "2026-05-02T09:00:00Z", revision: 2, downloadCount: 7 };

const links = (items: ShareLink[]) => (url: URL, init: RequestInit) => {
  if (url.pathname === `/api/v1/reporting/library/${LIB}/share-links`) {
    if (init.method === "POST") return json(201, { ...active, id: "0190f0a0-0000-7000-8000-000000000f09", token: TOKEN, path: `/v1/reporting/shared/${TOKEN}` }, { ETag: '"1"' });
    return json(200, { items });
  }
  if (url.pathname === `/api/v1/reporting/library/${LIB}`) return json(200, entry);
  return undefined;
};

describe("share lifetime rules", () => {
  it("accepts whole days from 1 to 90", () => {
    expect(validateShareDays(" 14 ")).toEqual({ days: 14 });
    expect(validateShareDays("1")).toEqual({ days: 1 });
    expect(validateShareDays("90")).toEqual({ days: 90 });
  });
  it("names what the service would refuse", () => {
    for (const bad of ["", "0", "91", "2.5", "abc", "-3"]) expect(validateShareDays(bad).error).toMatch(/1 to 90/);
  });
});

describe("ShareLinks on the library file page", () => {
  it("is not offered without the share duty", async () => {
    server.handler = links([active]);
    renderAt(`/finreport/library/${LIB}`);
    expect(await screen.findByText("BS-STD-2026-03.csv")).toBeInTheDocument();
    expect(screen.queryByText("Share this file")).toBeNull();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/share-links"))).toBe(false);
  });

  it("lists the links with their status and downloads", async () => {
    server.handler = links([active, expired, revoked]);
    const { container } = renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const table = await findTable({ name: "Share links" });
    expect(within(table).getByText("Active")).toBeInTheDocument();
    expect(within(table).getByText("Expired")).toBeInTheDocument();
    expect(within(table).getByText("Revoked")).toBeInTheDocument();
    expect(within(table).getByText("3")).toBeInTheDocument();
    expect(within(table).getByText("7")).toBeInTheDocument();
    // Only links still working or waiting to expire can be revoked.
    expect(within(table).getAllByRole("button", { name: /^Revoke the link/ })).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty state and a retry on error", async () => {
    server.handler = links([]);
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    expect(await screen.findByText("This file has not been shared")).toBeInTheDocument();
  });

  it("offers a retry when the links cannot be loaded", async () => {
    server.handler = (url, init) => (url.pathname.endsWith("/share-links") ? problem(500, "COM-0500", "links are down") : links([])(url, init));
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    expect(await findAlert()).toHaveTextContent("links are down");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("creates a link with an idempotency key and shows the secret once", async () => {
    server.handler = links([]);
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const form = await screen.findByRole("form", { name: "Create a share link" });
    fireEvent.change(within(form).getByLabelText(/^Link lifetime/), { target: { value: "0" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create link" }));
    expect(await within(form).findByText("Enter a whole number of days from 1 to 90.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText(/^Link lifetime/), { target: { value: "30" } });
    fireEvent.click(within(form).getByRole("button", { name: "Create link" }));
    const url = (await screen.findByLabelText("Share link")) as HTMLInputElement;
    expect(url.value).toBe(`${window.location.origin}/api/v1/reporting/shared/${TOKEN}`);
    expect(url.readOnly).toBe(true);
    expect(screen.getByText("Copy the link now")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ expiresInDays: 30 });
    expect(header(post!, "Idempotency-Key")).toMatch(/\S+/);
    // The list is read again after the link is made.
    await waitFor(() => expect(server.calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith("/share-links")).length).toBeGreaterThan(1));
  });

  it("says the secret cannot be shown again when the service replays a link already made", async () => {
    server.handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/share-links") ? json(200, { ...active, id: "0190f0a0-0000-7000-8000-000000000f09" }) : links([active])(url, init);
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const form = await screen.findByRole("form", { name: "Create a share link" });
    fireEvent.click(within(form).getByRole("button", { name: "Create link" }));
    expect(await screen.findByText("This link was created before")).toBeInTheDocument();
    expect(screen.queryByText("Copy the link now")).toBeNull();
    expect(screen.queryByLabelText("Share link")).toBeNull();
  });

  it("shows the service's refusal", async () => {
    server.handler = (url, init) => (init.method === "POST" ? problem(409, "REPORT-0053", "past its retention date") : links([])(url, init));
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const form = await screen.findByRole("form", { name: "Create a share link" });
    fireEvent.click(within(form).getByRole("button", { name: "Create link" }));
    expect(await findAlert()).toHaveTextContent("past its retention date");
    expect(screen.queryByText("Copy the link now")).toBeNull();
  });

  it("revokes a link after confirmation, with the revision it read", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/reporting/share-links/${active.id}:revoke`) return init.method === "POST" ? json(200, { ...active, status: "revoked", revision: 2 }, { ETag: '"2"' }) : undefined;
      return links([active])(url, init);
    };
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const table = await findTable({ name: "Share links" });
    fireEvent.click(within(table).getByRole("button", { name: /^Revoke the link/ }));
    const dialog = await screen.findByRole("alertdialog", { name: "Revoke this share link?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep link" }));
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(within(table).getByRole("button", { name: /^Revoke the link/ }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke link" }));
    await waitFor(() => expect(server.calls.some((c) => c.method === "POST")).toBe(true));
    const post = server.calls.find((c) => c.method === "POST");
    expect(header(post!, "If-Match")).toBe('"1"');
  });

  it("shows the conflict when the link changed meanwhile", async () => {
    server.handler = (url, init) => (init.method === "POST" ? problem(412, "conflict_version", "share link revision is 2, not 1") : links([active])(url, init));
    renderAt(`/finreport/library/${LIB}`, [DUTY]);
    const table = await findTable({ name: "Share links" });
    fireEvent.click(within(table).getByRole("button", { name: /^Revoke the link/ }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke link" }));
    expect(await findAlert()).toHaveTextContent("conflict_version");
  });
});
