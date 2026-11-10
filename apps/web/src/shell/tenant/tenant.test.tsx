import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it } from "vitest";
import type { PassageModule } from "../../modules/registry";
import { Shell } from "../Shell";
import { json, server, useFakeServer } from "../testing/fakeServer";
import { useWorkspaceNav } from "../workspaces/WorkspaceNav";
import { featureForPath, filterModulesByFeatures, isFeatureOn, isPathEnabled } from "./featureRules";
import { FeatureGate } from "./TenantSettings";

useFakeServer();

beforeEach(() => window.localStorage.clear());

const modules: PassageModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [],
    nav: [{ label: "Journal batches", to: "/ledger/journal-batches" }],
  },
  {
    id: "processmfg",
    title: "Process manufacturing",
    routes: [{ path: "/processmfg/formulas", element: <p>Formulas page</p> }],
    nav: [{ label: "Formulas", to: "/processmfg/formulas" }],
  },
  {
    id: "integration",
    title: "Integration",
    routes: [],
    nav: [
      { label: "Trading partners", to: "/integration/partners" },
      { label: "Peppol invoice", to: "/integration/peppol" },
    ],
  },
];

describe("feature rules", () => {
  it("maps a path to the longest feature prefix", () => {
    expect(featureForPath("/processmfg/formulas/1")).toBe("process-manufacturing");
    expect(featureForPath("/integration/interchanges/outbound")).toBe("edi");
    expect(featureForPath("/integration/peppol")).toBeUndefined();
    expect(featureForPath("/salesforce")).toBeUndefined();
  });

  it("treats a feature as on unless the tenant switched it off", () => {
    expect(isFeatureOn(undefined, "edi")).toBe(true);
    expect(isFeatureOn({ edi: true }, "edi")).toBe(true);
    expect(isFeatureOn({ edi: false }, "edi")).toBe(false);
    expect(isFeatureOn({ edi: false }, "payroll")).toBe(true);
    expect(isPathEnabled({ "process-manufacturing": false }, "/processmfg")).toBe(false);
  });

  it("drops entries of switched-off features and the modules they empty", () => {
    const kept = filterModulesByFeatures(modules, { "process-manufacturing": false, edi: false });
    expect(kept.map((m) => m.id)).toEqual(["ledger", "integration"]);
    expect(kept[1].nav.map((n) => n.to)).toEqual(["/integration/peppol"]);
    expect(filterModulesByFeatures(modules, undefined)).toBe(modules);
  });
});

function Entries() {
  const { entries } = useWorkspaceNav();
  return <ul aria-label="entries">{entries.map((e) => <li key={e.to}>{e.to}</li>)}</ul>;
}

function renderShell(props: { duties: string[]; features?: Record<string, boolean>; path?: string }) {
  return render(
    <MemoryRouter initialEntries={[props.path ?? "/"]}>
      <Routes>
        <Route element={<Shell modules={modules} duties={props.duties} features={props.features} />}>
          <Route path="/" element={<Entries />} />
          <Route path="/processmfg/formulas" element={<p>Formulas page</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const featureReply = (off: string[]) =>
  json(200, {
    features: ["finance", "process-manufacturing", "edi"].map((key) => ({ key, name: key, description: "", module: key, dependsOn: null, enabled: !off.includes(key) })),
    templates: [],
  });

describe("shell feature gating", () => {
  it("hides the navigation of a switched-off feature and says so on its pages", async () => {
    renderShell({ duties: [], features: { "process-manufacturing": false }, path: "/processmfg/formulas" });
    expect(await screen.findByText("This isn't turned on for your company")).toBeInTheDocument();
    expect(screen.queryByText("Formulas page")).not.toBeInTheDocument();
  });

  it("shows the page when its feature is on", async () => {
    renderShell({ duties: [], features: { edi: false }, path: "/processmfg/formulas" });
    expect(await screen.findByText("Formulas page")).toBeInTheDocument();
  });

  it("reads the tenant's features, applies them without a reload and fails open on error", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tenant/features" ? featureReply(["process-manufacturing", "edi"]) : undefined);
    renderShell({ duties: ["identity.feature.view"] });
    const list = screen.getByRole("list", { name: "entries" });
    await waitFor(() => expect(within(list).queryByText("/processmfg/formulas")).not.toBeInTheDocument());
    expect(within(list).getByText("/ledger/journal-batches")).toBeInTheDocument();
    expect(within(list).queryByText("/integration/partners")).not.toBeInTheDocument();
    expect(within(list).getByText("/integration/peppol")).toBeInTheDocument();
  });

  it("keeps everything on when the features cannot be read", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderShell({ duties: ["identity.feature.view"] });
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/tenant/features")).toBe(true));
    expect(within(screen.getByRole("list", { name: "entries" })).getByText("/processmfg/formulas")).toBeInTheDocument();
  });

  it("asks the server for nothing when the person holds none of the tenant duties", () => {
    renderShell({ duties: [] });
    expect(server.calls).toHaveLength(0);
  });

  it("merges the tenant's workspace override into the shipped workspaces", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/tenant/workspace-config" ? json(200, { config: { workspaces: [{ id: "ops", label: "Operations", persona: "Ops", description: "Daily ops", entries: ["/ledger/journal-batches"] }] } }) : undefined);
    function Names() {
      const { available } = useWorkspaceNav();
      return <ul aria-label="workspaces">{available.map((w) => <li key={w.id}>{w.label}</li>)}</ul>;
    }
    render(
      <MemoryRouter>
        <Routes>
          <Route element={<Shell modules={modules} duties={["identity.setup.view"]} />}>
            <Route path="/" element={<Names />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(await within(await screen.findByRole("list", { name: "workspaces" })).findByText("Operations")).toBeInTheDocument();
  });
});

describe("FeatureGate", () => {
  it("renders its fields only while the feature is on, and the fallback otherwise", () => {
    const view = (features: Record<string, boolean>) => (
      <MemoryRouter>
        <Routes>
          <Route element={<Shell modules={modules} duties={[]} features={features} />}>
            <Route path="/" element={<FeatureGate feature="catch-weight" fallback={<p>Not used</p>}><label>Catch weight<input /></label></FeatureGate>} />
          </Route>
        </Routes>
      </MemoryRouter>
    );
    const on = render(view({}));
    expect(screen.getByLabelText("Catch weight")).toBeInTheDocument();
    on.unmount();
    render(view({ "catch-weight": false }));
    expect(screen.queryByLabelText("Catch weight")).not.toBeInTheDocument();
    expect(screen.getByText("Not used")).toBeInTheDocument();
  });
});
