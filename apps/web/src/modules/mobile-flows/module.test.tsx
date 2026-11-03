import { findAlert } from "../../test-utils";
import { fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import mobileFlows from "./module";

describe("mobile-flows module", () => {
  it("keeps every route under /mobile-flows and gates nav by the PRD duties", () => {
    expect(mobileFlows.id).toBe("mobile-flows");
    expect(mobileFlows.routes[0].path).toBe("/mobile-flows");
    expect(mobileFlows.routes.every((r) => r.element != null && r.path?.startsWith("/mobile-flows"))).toBe(true);
    expect(mobileFlows.nav).toEqual([
      { label: "Flow designer", to: "/mobile-flows", duty: "WMS_MOBILE_ADMIN" },
      { label: "Printers", to: "/mobile-flows/printers", duty: "WMS_PRINT" },
      { label: "Printer status", to: "/mobile-flows/printer-status", duty: "WMS_PRINT" },
      { label: "Routing rules", to: "/mobile-flows/routing-rules", duty: "WMS_PRINT" },
      { label: "Print jobs", to: "/mobile-flows/print-jobs", duty: "WMS_PRINT" },
      { label: "Offline conflicts", to: "/mobile-flows/conflicts", duty: "WMS_WORK_SUPERVISE" },
    ]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("mobile-flows");
  });

  it("validates the sample flow and reports an unreachable step", () => {
    render(createElement(MemoryRouter, null, mobileFlows.routes[0].element));
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("status")).toHaveTextContent("Flow graph is valid");

    const area = screen.getByLabelText("Flow definition (JSON)") as HTMLTextAreaElement;
    const broken = JSON.parse(area.value);
    broken.steps.push({ id: "orphan", type: "display", prompt: "x", next: {} });
    fireEvent.change(area, { target: { value: JSON.stringify(broken) } });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("list", { name: "Validation issues" })).toHaveTextContent(
      "MOB-0113: step orphan is unreachable from the entry step",
    );
  });

  it("simulates a GS1 scan against the expected class", () => {
    render(createElement(MemoryRouter, null, mobileFlows.routes[0].element));
    fireEvent.change(screen.getByLabelText("Scan value"), { target: { value: "]C1" + "0109501234567891" + "17261231" } });
    fireEvent.change(screen.getByLabelText("Expected class"), { target: { value: "LP" } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate scan" }));
    const result = screen.getByRole("status", { name: "Simulation result" });
    expect(result).toHaveTextContent("MOB-0101 Expected LP, scanned GTIN.");
    expect(result).toHaveTextContent("AI 01: 09501234567891");
  });

  it("reports a bad check digit before the class mismatch", () => {
    render(createElement(MemoryRouter, null, mobileFlows.routes[0].element));
    fireEvent.change(screen.getByLabelText("Scan value"), { target: { value: "]C1" + "0109501234567892" } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate scan" }));
    expect(screen.getByRole("status", { name: "Simulation result" })).toHaveTextContent("MOB-0102 Check digit invalid for AI 01.");
  });

  it("explains when the flow service is not reachable instead of claiming a save", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not found", { status: 404 })));
    try {
      render(createElement(MemoryRouter, null, mobileFlows.routes[0].element));
      fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
      expect(await findAlert()).toHaveTextContent("The mobile flow service is not available yet");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("publishes a valid graph so devices pick up the version without an app release", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/flows") && init?.method === "POST") {
        return new Response(JSON.stringify({ ...JSON.parse(String(init.body)), flowId: "flow-1", version: 2, status: "DRAFT" }), { status: 200 });
      }
      if (url.includes(":publish")) {
        return new Response(JSON.stringify({ code: "RECEIVE_PUTAWAY", flowId: "flow-1", version: 2, status: "PUBLISHED", entryStepId: "scan_lp", steps: [] }), { status: 200 });
      }
      return new Response("not found", { status: 404 });
    });
    vi.stubGlobal("fetch", fetchMock);
    try {
      render(createElement(MemoryRouter, null, mobileFlows.routes[0].element));
      fireEvent.click(screen.getByRole("button", { name: "Publish" }));
      expect(await screen.findByRole("status")).toHaveTextContent("Published RECEIVE_PUTAWAY version 2");
      const urls = fetchMock.mock.calls.map((c) => String(c[0]));
      expect(urls.some((u) => u.endsWith("/flows"))).toBe(true);
      expect(urls.some((u) => u.includes("/flows/flow-1:publish"))).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
