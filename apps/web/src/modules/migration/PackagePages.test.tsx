import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { ConfigPackage, PackageDiff } from "./api/client";
import { parsePackageInput } from "./domain";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["migration.run", "migration.package.apply"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const PKG = "0195f0a0-0000-7000-8000-0000000000c1";
const pkg: ConfigPackage = {
  id: PKG,
  version: "1.2.0",
  schemaVersion: 1,
  fileCount: 2,
  files: [
    { name: "uom.json", sha256: "a".repeat(64), dependsOn: [] },
    { name: "item.json", sha256: "b".repeat(64), dependsOn: ["uom.json"] },
  ],
  applied: [{ tenantId: "t", appliedAt: "2026-10-30T11:00:00Z", diffCounts: { create: 3, update: 1, unchanged: 0 } }],
  createdBy: "u1",
  createdAt: "2026-10-30T11:00:00Z",
};
const diff: PackageDiff = {
  version: "1.2.0",
  schemaVersion: 1,
  counts: { create: 3, update: 1, unchanged: 2 },
  files: [{ name: "uom.json", entity: "uom", create: ["EA", "KG"], update: ["M"], unchanged: ["X", "Y"] }],
};
const PACKAGE_TEXT = '{"version":"1.2.0","schemaVersion":1,"manifest":{"files":[]},"files":[]}';

function fill() {
  fireEvent.change(screen.getByLabelText("Package JSON"), { target: { value: PACKAGE_TEXT } });
  fireEvent.change(screen.getByLabelText("Signature (base64)"), { target: { value: "c2ln" } });
}

describe("configuration packages", () => {
  it("registers its routes and nav item under the module prefix", () => {
    expect(migration.routes.map((r) => r.path)).toContain("/migration/packages/new");
    expect(migration.nav.some((n) => n.to === "/migration/packages")).toBe(true);
  });

  it("parses the pasted package and signature", () => {
    expect(parsePackageInput("", "").errors).toEqual({ package: "Paste the package JSON.", signature: "Paste the base64 signature." });
    expect(parsePackageInput("[1]", "x").errors.package).toMatch(/object/);
    expect(parsePackageInput("{", "x").errors.package).toMatch(/Not valid JSON/);
    expect(parsePackageInput('{"a":1}', " sig ")).toEqual({ pkg: { a: 1 }, errors: {} });
  });

  it("lists packages, then the empty state and the error state with retry", async () => {
    handler = () => json(200, { items: [pkg] });
    const first = renderAt("/migration/packages");
    const table = await findTable({ name: "Configuration packages" });
    expect(table).toHaveTextContent("1.2.0");
    expect(table).toHaveTextContent("2026-10-30 11:00 UTC");
    expect(screen.getByRole("link", { name: "Apply package" })).toBeInTheDocument();
    first.unmount();
    handler = () => json(200, { items: [] });
    const second = renderAt("/migration/packages");
    expect(await screen.findByText("No configuration packages yet")).toBeInTheDocument();
    second.unmount();
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/migration/packages");
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
    handler = () => json(200, { items: [pkg] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Configuration packages" })).toHaveTextContent("1.2.0");
  });

  it("hides Apply package without the apply duty", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration/packages", ["migration.run"]);
    await screen.findByText("No configuration packages yet");
    expect(screen.queryByRole("link", { name: "Apply package" })).not.toBeInTheDocument();
  });

  it("validates locally, previews the diff, then applies with an idempotency key", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":diff")) return json(200, diff);
      if (url.pathname.endsWith(":apply")) return json(200, { packageId: PKG, version: "1.2.0", schemaVersion: 1, appliedAt: "2026-10-30T11:00:00Z", counts: diff.counts });
      return init.method === "POST" ? undefined : json(200, pkg);
    };
    const { container } = renderAt("/migration/packages/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    expect(await screen.findByText("Paste the package JSON.")).toBeInTheDocument();
    expect(screen.getByText("Paste the base64 signature.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    const files = await findTable({ name: "Files in apply order" });
    expect(files).toHaveTextContent("uom.json");
    expect(screen.getByText("To create")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ package: JSON.parse(PACKAGE_TEXT), signature: "c2ln" });
    fireEvent.click(screen.getByRole("button", { name: "Apply package" }));
    expect(await screen.findByRole("heading", { name: "Package 1.2.0" })).toBeInTheDocument();
    const post = calls.find((c) => c.url.pathname.endsWith(":apply"));
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByRole("table", { name: "Applied history" })).toHaveTextContent("3");
  });

  it("editing the package clears the preview so a stale diff is never applied", async () => {
    handler = () => json(200, diff);
    renderAt("/migration/packages/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await findTable({ name: "Files in apply order" });
    fireEvent.change(screen.getByLabelText("Signature (base64)"), { target: { value: "other" } });
    await waitFor(() => expect(screen.queryByRole("table", { name: "Files in apply order" })).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Apply package" })).not.toBeInTheDocument();
  });

  it("shows the service problem code for a bad signature and for a missing backend", async () => {
    handler = () => json(422, { code: "MIG-PKG-SIGNATURE", detail: "package signature does not match its manifest" });
    const first = renderAt("/migration/packages/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    expect(await screen.findByText(/MIG-PKG-SIGNATURE/)).toBeInTheDocument();
    first.unmount();
    handler = () => json(501, { code: "MIG-NOT-CONFIGURED", detail: "no package target is wired" });
    renderAt("/migration/packages/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    expect(await screen.findByText(/MIG-NOT-CONFIGURED/)).toBeInTheDocument();
  });

  it("shows an apply failure and keeps the preview", async () => {
    handler = (url) => (url.pathname.endsWith(":diff") ? json(200, diff) : json(422, { code: "MIG-PKG-SCHEMA-NEWER", detail: "package schema 2 is newer than the supported schema 1" }));
    renderAt("/migration/packages/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Preview changes" }));
    await findTable({ name: "Files in apply order" });
    fireEvent.click(screen.getByRole("button", { name: "Apply package" }));
    expect(await screen.findByText(/MIG-PKG-SCHEMA-NEWER/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply package" })).toBeInTheDocument();
  });

  it("shows a package with its files and history, an unapplied package and a load error", async () => {
    handler = () => json(200, pkg);
    const first = renderAt(`/migration/packages/${PKG}`);
    expect(await findTable({ name: "Package files" })).toHaveTextContent("uom.json");
    expect(screen.getByRole("table", { name: "Applied history" })).toHaveTextContent("2026-10-30 11:00 UTC");
    first.unmount();
    handler = () => json(200, { ...pkg, applied: [] });
    const second = renderAt(`/migration/packages/${PKG}`);
    expect(await screen.findByText("Not applied yet")).toBeInTheDocument();
    second.unmount();
    handler = () => json(404, { code: "AUTH-0404", detail: "configuration package not found" });
    renderAt(`/migration/packages/${PKG}`);
    expect(await screen.findByText(/configuration package not found/)).toBeInTheDocument();
  });
});
