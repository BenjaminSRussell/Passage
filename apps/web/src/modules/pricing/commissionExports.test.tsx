import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SettlementExportRecord } from "./api/client";
import { validateVoidReason } from "./CommissionExportDetailPage";
import { idempotencyKey, json, pageViolations, renderAt, server, text, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const ID = "0190f0a0-0000-7000-8000-000000000077";
const MAINTAIN = ["pricing.commission.view", "pricing.commission.maintain"];

const issued: SettlementExportRecord = {
  id: ID,
  legalEntityId: LE,
  target: "PAYROLL",
  from: "2027-03-01",
  to: "2027-03-31",
  status: "ISSUED",
  lineCount: 2,
  totals: [{ currency: "USD", amount: "400" }],
  csvSha256: "a".repeat(64),
  exportedBy: "u1",
  voidReason: "",
  voidedBy: "",
  version: 1,
  createdAt: "2027-04-01 09:00:00+00",
  lines: [
    { repId: "R1", currency: "USD", amount: "240", transactions: 3 },
    { repId: "R2", currency: "USD", amount: "160", transactions: 3 },
  ],
};

const CSV = "target,rep_id,currency,amount,transactions,from,to\r\nPAYROLL,R1,USD,240.00,3,2027-03-01,2027-03-31\r\n";

describe("Commission exports list", () => {
  it("lists the recorded exports, filters them and opens one", async () => {
    server.handler = (url) => (url.pathname.endsWith("/commission/settlement-exports") ? json(200, { items: [{ ...issued, lines: undefined }] }) : undefined);
    const { container } = renderAt("/pricing/commission/exports");
    const table = await findTable({ name: "Commission exports" });
    expect(table).toHaveTextContent("2027-03-01 to 2027-03-31");
    expect(table).toHaveTextContent("400 USD");
    expect(table).toHaveTextContent("Issued");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "VOIDED" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("VOIDED"));
  });

  it("shows the empty state and a failure with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/pricing/commission/exports");
    expect(await screen.findByText("No exports recorded")).toBeInTheDocument();
  });

  it("shows the service failure and reloads on retry", async () => {
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.commission.view" });
    renderAt("/pricing/commission/exports");
    expect(await findAlert()).toHaveTextContent("missing duty pricing.commission.view (AUTH-0003)");
    server.handler = () => json(200, { items: [issued] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Commission exports" })).toHaveTextContent("Payroll");
  });
});

describe("Commission export detail", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows the frozen rows and downloads the service's file", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(`/${ID}/file`)) return text(200, CSV, { "Content-Type": "text/csv", "Content-Disposition": 'attachment; filename="commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv"' });
      if (url.pathname.endsWith(`/${ID}`)) return json(200, issued);
      return undefined;
    };
    const { container } = renderAt(`/pricing/commission/exports/${ID}`);
    const table = await findTable({ name: "Rows as handed over" });
    expect(table).toHaveTextContent("240 USD");
    expect(within(table).getByText("R2")).toBeInTheDocument();
    expect(screen.getByText("a".repeat(64))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Void export" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    const blobs: Blob[] = [];
    Object.assign(URL, { createObjectURL: vi.fn((b: Blob) => (blobs.push(b), "blob:export")), revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.download).toBe("commission-settlement-PAYROLL-2027-03-01-2027-03-31.csv");
    });
    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1));
    expect(await blobs[0].text()).toBe(CSV);
  });

  it("voids with a reason after confirmation and shows the voided state", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/${ID}:void`)) return json(200, { ...issued, status: "VOIDED", version: 2, voidReason: "sent the wrong month", voidedBy: "u2" });
      if (url.pathname.endsWith(`/${ID}`)) return json(200, issued);
      return undefined;
    };
    renderAt(`/pricing/commission/exports/${ID}`, MAINTAIN);
    await findTable({ name: "Rows as handed over" });
    fireEvent.click(screen.getByRole("button", { name: "Void export" }));
    expect(screen.getByText("Say why the export is voided.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "sent the wrong month" } });
    fireEvent.click(screen.getByRole("button", { name: "Void export" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Void export" }));
    expect(await screen.findByText("This export was voided")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ version: 1, reason: "sent the wrong month" });
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Void export" })).toBeNull();
  });

  it("shows a refused void and a missing export", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "PRC-0031", detail: "settlement export is already voided" });
      return url.pathname.endsWith(`/${ID}`) ? json(200, issued) : undefined;
    };
    renderAt(`/pricing/commission/exports/${ID}`, MAINTAIN);
    await findTable({ name: "Rows as handed over" });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "again" } });
    fireEvent.click(screen.getByRole("button", { name: "Void export" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Void export" }));
    expect(await findAlert()).toHaveTextContent("settlement export is already voided (PRC-0031)");
  });

  it("reports an export that does not exist", async () => {
    server.handler = () => json(404, { code: "PRC-0067", detail: "no settlement export x" });
    renderAt(`/pricing/commission/exports/${ID}`);
    expect(await findAlert()).toHaveTextContent("no settlement export x (PRC-0067)");
  });

  it("checks the reason", () => {
    expect(validateVoidReason("  ")).toBe("Say why the export is voided.");
    expect(validateVoidReason("x".repeat(501))).toBe("The reason is at most 500 characters.");
    expect(validateVoidReason("wrong month")).toBe("");
  });
});

describe("Recording an export from the settlement page", () => {
  const rowsCsv = "target,rep_id,currency,amount,transactions,from,to\r\nAP,R1,USD,240.00,3,2027-03-01,2027-03-31\r\n";

  function preview() {
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("First invoice date"), { target: { value: "2027-03-01" } });
    fireEvent.change(screen.getByLabelText("Last invoice date"), { target: { value: "2027-03-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview settlement" }));
  }

  it("offers Record export only with the maintain duty, records it and opens the record", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/commission/settlement-exports")) return json(201, { ...issued, target: "AP" });
      if (url.pathname.endsWith("/commission/settlement-export")) return text(200, rowsCsv, { "Content-Type": "text/csv" });
      if (url.pathname.endsWith(`/${ID}`)) return json(200, { ...issued, target: "AP" });
      return undefined;
    };
    const first = renderAt("/pricing/commission/settlement");
    preview();
    await findTable({ name: "Commission settlement" });
    expect(screen.queryByRole("button", { name: "Record export" })).toBeNull();
    first.unmount();

    renderAt("/pricing/commission/settlement", MAINTAIN);
    preview();
    await findTable({ name: "Commission settlement" });
    fireEvent.click(screen.getByRole("button", { name: "Record export" }));
    expect(await screen.findByRole("heading", { name: /Accounts payable: 2027-03-01 to 2027-03-31/ })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ legalEntityId: LE, from: "2027-03-01", to: "2027-03-31", target: "AP" });
    expect(idempotencyKey(post!)).toBeTruthy();
  });

  it("shows the overlap refusal and keeps the preview", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "PRC-0065", detail: "settlement export x already covers part of this period for AP; void it before exporting again" });
      if (url.pathname.endsWith("/commission/settlement-export")) return text(200, rowsCsv, { "Content-Type": "text/csv" });
      return undefined;
    };
    renderAt("/pricing/commission/settlement", MAINTAIN);
    preview();
    await findTable({ name: "Commission settlement" });
    fireEvent.click(screen.getByRole("button", { name: "Record export" }));
    expect(await findAlert()).toHaveTextContent("already covers part of this period for AP; void it before exporting again (PRC-0065)");
    expect(screen.getByRole("button", { name: "Record export" })).toBeInTheDocument();
  });
});
