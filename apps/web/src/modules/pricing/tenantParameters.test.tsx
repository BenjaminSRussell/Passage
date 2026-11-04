import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findAlert } from "../../test-utils";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ADMIN = ["pricing.view", "pricing.admin"];
const URL_END = "/tenant-parameters";

describe("Tenant pricing parameters", () => {
  it("shows a loading state, then the defaults", async () => {
    server.handler = (url) => (url.pathname.endsWith(URL_END) ? json(200, { cacheTtlSeconds: 0, campaignKillSwitchEffect: "NEXT_RESOLVE", version: 0 }) : undefined);
    const { container } = renderAt("/pricing/tenant-parameters", ADMIN);
    expect(screen.queryByLabelText("Snapshot lifetime (seconds)")).toBeNull(); // still loading: no form yet
    expect(await screen.findByLabelText("Snapshot lifetime (seconds)")).toHaveValue("");
    expect(screen.getByLabelText("Campaign kill switch effect")).toHaveValue("NEXT_RESOLVE");
    expect(screen.getByText("Defaults, nothing saved yet.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves the lifetime and the kill switch effect at the version read", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith(URL_END)) return undefined;
      if (init.method === "PUT") return json(200, { ...JSON.parse(String(init.body)), version: 4, updatedBy: "ana" });
      return json(200, { cacheTtlSeconds: 20, campaignKillSwitchEffect: "NEXT_RESOLVE", version: 3 });
    };
    renderAt("/pricing/tenant-parameters", ADMIN);
    const ttl = await screen.findByLabelText("Snapshot lifetime (seconds)");
    expect(ttl).toHaveValue("20");
    fireEvent.change(ttl, { target: { value: "45" } });
    fireEvent.change(screen.getByLabelText("Campaign kill switch effect"), { target: { value: "IMMEDIATE_CACHE_FLUSH" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tenant parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ cacheTtlSeconds: 45, campaignKillSwitchEffect: "IMMEDIATE_CACHE_FLUSH", version: 3 });
    expect(idempotencyKey(put)).toBeTruthy();
    expect(screen.getByText("Version 4, saved by ana")).toBeInTheDocument();
  });

  it("sends 0 for an empty lifetime and refuses a non-number before calling", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith(URL_END)) return undefined;
      if (init.method === "PUT") return json(200, { ...JSON.parse(String(init.body)), version: 2 });
      return json(200, { cacheTtlSeconds: 30, campaignKillSwitchEffect: "NEXT_RESOLVE", version: 1 });
    };
    renderAt("/pricing/tenant-parameters", ADMIN);
    const ttl = await screen.findByLabelText("Snapshot lifetime (seconds)");
    fireEvent.change(ttl, { target: { value: "soon" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tenant parameters" }));
    expect(await screen.findByText("Enter whole seconds from 10 to 600, or leave it empty.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(0);
    fireEvent.change(ttl, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tenant parameters" }));
    expect(await screen.findByText("Parameters saved")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "PUT")!.body).toMatchObject({ cacheTtlSeconds: 0 });
  });

  it("shows a refused lifetime on its field", async () => {
    server.handler = (url, init) => {
      if (!url.pathname.endsWith(URL_END)) return undefined;
      if (init.method === "PUT") return json(422, { code: "COM-0001", detail: "request is invalid", errors: { cacheTtlSeconds: "must be 0 or from 10 to 600" } });
      return json(200, { cacheTtlSeconds: 0, campaignKillSwitchEffect: "NEXT_RESOLVE", version: 0 });
    };
    renderAt("/pricing/tenant-parameters", ADMIN);
    fireEvent.change(await screen.findByLabelText("Snapshot lifetime (seconds)"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save tenant parameters" }));
    expect(await screen.findByText("must be 0 or from 10 to 600")).toBeInTheDocument();
    expect(screen.getByLabelText("Snapshot lifetime (seconds)")).toHaveAttribute("aria-invalid", "true");
  });

  it("reads without pricing.admin but offers no save", async () => {
    server.handler = () => json(200, { cacheTtlSeconds: 60, campaignKillSwitchEffect: "IMMEDIATE_CACHE_FLUSH", version: 2, updatedBy: "ana" });
    renderAt("/pricing/tenant-parameters", ["pricing.view"]);
    expect(await screen.findByLabelText("Snapshot lifetime (seconds)")).toBeDisabled();
    expect(screen.getByLabelText("Campaign kill switch effect")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save tenant parameters" })).toBeNull();
    expect(screen.getByText("Changing them needs the pricing.admin duty.")).toBeInTheDocument();
  });

  it("reports a failed load and retries it", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { cacheTtlSeconds: 0, campaignKillSwitchEffect: "NEXT_RESOLVE", version: 0 }));
    renderAt("/pricing/tenant-parameters", ADMIN);
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByLabelText("Snapshot lifetime (seconds)")).toBeInTheDocument());
  });
});
