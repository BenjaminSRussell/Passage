import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BufferProfile, DdmrpBuffer } from "./api/client";
import { EMPTY_PROFILE, EMPTY_RECALC, priorityPercent, profileDraftOf, shiftDecimal, validateBufferProfile, validateRecalc } from "./BufferPages";
import planning from "./module";

const ITEM = "00000000-0000-0000-0000-0000000000b1";
const SITE = "00000000-0000-0000-0000-0000000000a1";
const WH = "00000000-0000-0000-0000-0000000000d1";

const profile: BufferProfile = {
  id: "00000000-0000-0000-0000-0000000000e1",
  version: 1,
  code: "BUF-MFG",
  itemType: "manufactured",
  leadTimeFactor: "0.500",
  variabilityFactor: "0.500",
  orderCycleDays: 7,
  minOrderQty: "0.000000",
  spikeThresholdPct: "50.00",
  spikeHorizonDays: null,
  aduWindowPastDays: 90,
  aduWindowFutureDays: 0,
};

/** The BR-22.11 worked example as the service stores it. */
const yellow: DdmrpBuffer = {
  id: "00000000-0000-0000-0000-0000000000f1",
  itemId: ITEM,
  siteId: SITE,
  warehouseId: null,
  profileId: profile.id,
  profileCode: "BUF-MFG",
  adu: "10.000000",
  dltDays: "10.00",
  daf: "1.000",
  adjustedAdu: "10",
  zones: { redBase: "50", redSafety: "25", yellow: "100", green: "70", topOfRed: "75", topOfYellow: "175", topOfGreen: "245" },
  qualifiedDemand: "20",
  netFlow: "130.000000",
  plannedQty: "115.000000",
  priority: "0.530600",
  zone: "yellow",
  calculatedAt: "2026-11-02T00:15:00Z",
};
const red: DdmrpBuffer = { ...yellow, id: "00000000-0000-0000-0000-0000000000f2", warehouseId: WH, netFlow: "50", priority: "0.2041", zone: "red", plannedQty: "195" };

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

const bodyOf = (init?: RequestInit) => JSON.parse(String(init?.body)) as Record<string, unknown>;

afterEach(() => vi.unstubAllGlobals());

describe("buffer profile rules", () => {
  const draft = { ...EMPTY_PROFILE, code: "buf-mfg", leadTimeFactor: "0.5", variabilityFactor: "0.5", orderCycleDays: "7" };

  it("turns a valid draft into the request body with the 4.22.13 defaults", () => {
    expect(validateBufferProfile(draft)).toEqual({
      errors: {},
      input: {
        code: "BUF-MFG",
        itemType: "manufactured",
        leadTimeFactor: "0.5",
        variabilityFactor: "0.5",
        orderCycleDays: 7,
        minOrderQty: "0",
        spikeThresholdPct: "50",
        spikeHorizonDays: null,
        aduWindowPastDays: 90,
        aduWindowFutureDays: 0,
      },
    });
  });

  it("names every rule the service enforces", () => {
    const { errors, input } = validateBufferProfile({
      ...draft,
      code: "x",
      leadTimeFactor: "0.1234",
      variabilityFactor: "100",
      minOrderQty: "-1",
      spikeThresholdPct: "0",
      spikeHorizonDays: "1.5",
      aduWindowPastDays: "3",
      aduWindowFutureDays: "3",
      orderCycleDays: "3651",
    });
    expect(input).toBeNull();
    expect(Object.keys(errors).sort()).toEqual([
      "aduWindowPastDays",
      "code",
      "leadTimeFactor",
      "minOrderQty",
      "orderCycleDays",
      "spikeHorizonDays",
      "spikeThresholdPct",
      "variabilityFactor",
    ]);
  });

  it("round-trips a stored profile through the draft", () => {
    expect(validateBufferProfile(profileDraftOf(profile)).input).toMatchObject({ code: "BUF-MFG", leadTimeFactor: "0.5", spikeThresholdPct: "50" });
  });

  it("moves decimal points without floats", () => {
    expect(priorityPercent("0.530600")).toBe("53.06%");
    expect(priorityPercent("-0.1")).toBe("-10%");
    expect(priorityPercent("1.3878")).toBe("138.78%");
    expect(shiftDecimal("60", -2)).toBe("0.6");
    expect(shiftDecimal("5", -2)).toBe("0.05");
    expect(shiftDecimal("0", 2)).toBe("0");
  });

  it("checks a recalculation and sends qualified demand as day 0", () => {
    const ok = validateRecalc({ ...EMPTY_RECALC, itemId: ITEM, siteId: SITE, profileId: profile.id, adu: "10", dltDays: "10", onHand: "90", openSupply: "60", qualifiedDemand: "20" });
    expect(ok.body).toEqual({ itemId: ITEM, siteId: SITE, profileId: profile.id, adu: "10", dltDays: "10", daf: "1", onHand: "90", openSupply: "60", demand: [{ day: 0, qty: "20" }] });
    const bad = validateRecalc({ ...EMPTY_RECALC, itemId: "FG", warehouseId: "WH-2", dltDays: "10.125", daf: "1000" });
    expect(Object.keys(bad.errors).sort()).toEqual(["adu", "daf", "dltDays", "itemId", "profileId", "siteId", "warehouseId"]);
  });
});

describe("Buffer profiles", () => {
  it("lists profiles and opens one", async () => {
    stubApi([`/buffer-profiles/${profile.id}`, () => json(profile)], ["/buffer-profiles", () => json({ items: [profile] })]);
    renderAt("/planning/buffer-profiles");
    expect(screen.getByRole("heading", { level: 2, name: "Buffer profiles" })).toBeInTheDocument();
    const table = await findTable({ name: "Buffer profiles" });
    await waitFor(() => expect(within(table).getByText("Manufactured")).toBeInTheDocument());
    expect(within(table).getByText("50%")).toBeInTheDocument();
    expect(within(table).getByText("90 past / 0 future days")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "BUF-MFG" }));
    expect(screen.getByTestId("location")).toHaveTextContent(`/planning/buffer-profiles/${profile.id}`);
  });

  it("shows the empty state and the service error", async () => {
    stubApi(["/buffer-profiles", () => json({ items: [] })]);
    renderAt("/planning/buffer-profiles");
    expect(await screen.findByText("No buffer profiles yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/buffer-profiles", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/buffer-profiles");
    expect(await screen.findByText("Could not load buffer profiles")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("creates a profile with an Idempotency-Key and opens it; 409 is explained", async () => {
    let calls = 0;
    const fetchMock = stubApi(
      [`/buffer-profiles/${profile.id}`, () => json(profile)],
      [
        "/buffer-profiles",
        () => (++calls === 1 ? problem(409, "PLAN-PROFILE-EXISTS", "a buffer profile with code BUF-MFG already exists") : json(profile, 201)),
      ],
    );
    renderAt("/planning/buffer-profiles/new");
    fireEvent.click(screen.getByRole("button", { name: "Create buffer profile" }));
    expect(await screen.findByText(/Use 2 to 20 characters/)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "BUF-MFG" } });
    fireEvent.change(screen.getByLabelText("Lead-time factor (LTF)"), { target: { value: "0.5" } });
    fireEvent.change(screen.getByLabelText("Variability factor (VF)"), { target: { value: "0.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create buffer profile" }));
    expect(await screen.findByText("A buffer profile with this code already exists")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create buffer profile" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/planning/buffer-profiles/${profile.id}`));

    const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
    expect(posts).toHaveLength(2);
    const key = (i: number) => (posts[i][1]?.headers as Record<string, string>)["Idempotency-Key"];
    expect(key(0)).toBeTruthy();
    expect(key(1)).toBe(key(0));
    expect(bodyOf(posts[0][1])).toMatchObject({ code: "BUF-MFG", leadTimeFactor: "0.5", spikeThresholdPct: "50", aduWindowPastDays: 90 });
  });

  it("edits a profile under If-Match and offers a reload on 412", async () => {
    let puts = 0;
    const fetchMock = stubApi([
      `/buffer-profiles/${profile.id}`,
      (_url, init) => {
        if (init?.method !== "PUT") return json(profile);
        return ++puts === 1 ? problem(412, "conflict_version", "buffer profile is at version 2") : json({ ...profile, version: 2, leadTimeFactor: "0.7" });
      },
    ]);
    renderAt(`/planning/buffer-profiles/${profile.id}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Buffer profile BUF-MFG" })).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("Lead-time factor (LTF)"), { target: { value: "0.7" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this buffer profile")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload profile" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
  });
});

describe("DDMRP buffer status", () => {
  it("lists buffers most urgent first with zone, priority and order, and filters", async () => {
    const fetchMock = stubApi(["/buffer-profiles", () => json({ items: [profile] })], ["/ddmrp/buffers", () => json({ items: [red, yellow] })]);
    renderAt("/planning/ddmrp-buffers");
    const table = await findTable({ name: "DDMRP buffers" });
    await waitFor(() => expect(within(table).getByText("53.06%")).toBeInTheDocument());
    expect(within(table).getByText("Yellow")).toBeInTheDocument();
    expect(within(table).getByText("Red")).toBeInTheDocument();
    expect(within(table).getByText("115")).toBeInTheDocument();
    expect(within(table).getByText(WH)).toBeInTheDocument();
    expect(within(table).getAllByText(SITE).length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Zone"), { target: { value: "red" } });
    fireEvent.change(screen.getByLabelText("Priority at most (%)"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Show buffers" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes(`siteId=${SITE}`) && String(url).includes("zone=red") && String(url).includes("maxPriority=0.6"))).toBe(true),
    );
    expect(screen.getByTestId("location")).toHaveTextContent("maxPct=60");
  });

  it("shows the empty state and refuses a bad filter", async () => {
    const fetchMock = stubApi(["/buffer-profiles", () => json({ items: [] })], ["/ddmrp/buffers", () => json({ items: [] })]);
    renderAt("/planning/ddmrp-buffers");
    expect(await screen.findByText("No buffers calculated yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Priority at most (%)"), { target: { value: "lots" } });
    fireEvent.click(screen.getByRole("button", { name: "Show buffers" }));
    expect(await screen.findByText(/Enter a percent such as 60/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/ddmrp/buffers"))).toHaveLength(1);
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/buffer-profiles", () => json({ items: [] })], ["/ddmrp/buffers", () => problem(500, "PLAN-UNKNOWN", "boom")]);
    renderAt("/planning/ddmrp-buffers");
    expect(await screen.findByText("Could not load buffers")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "DDMRP buffers" })).not.toBeInTheDocument();
  });

  it("recalculates a buffer with the stored profile, shows the result and reloads the list", async () => {
    let lists = 0;
    const fetchMock = stubApi(
      ["/buffer-profiles", () => json({ items: [profile] })],
      ["/ddmrp/buffers:recalculate", () => json(yellow)],
      ["/ddmrp/buffers", () => json({ items: ++lists === 1 ? [] : [yellow] })],
    );
    renderAt(`/planning/ddmrp-buffers?profile=${profile.id}`);
    await screen.findByText("No buffers calculated yet");
    await screen.findByRole("option", { name: "BUF-MFG" });
    for (const [label, value] of [
      ["Item", ITEM],
      ["Average daily usage (ADU)", "10"],
      ["Decoupled lead time (days)", "10"],
      ["On hand", "90"],
      ["Open supply inside the DLT", "60"],
      ["Qualified demand", "20"],
    ]) {
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    }
    fireEvent.change(screen.getByLabelText("Buffer site"), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Recalculate buffer" }));
    expect(await screen.findByText("Yellow zone, net flow 130, priority 53.06%, order 115.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([url]) => String(url).includes(":recalculate"));
    expect(bodyOf(post?.[1])).toEqual({
      itemId: ITEM,
      siteId: SITE,
      profileId: profile.id,
      adu: "10",
      dltDays: "10",
      daf: "1",
      onHand: "90",
      openSupply: "60",
      demand: [{ day: 0, qty: "20" }],
    });
    const table = await findTable({ name: "DDMRP buffers" });
    await waitFor(() => expect(within(table).getByText("53.06%")).toBeInTheDocument());
  });

  it("explains a refused recalculation", async () => {
    stubApi(
      ["/buffer-profiles", () => json({ items: [profile] })],
      ["/ddmrp/buffers:recalculate", () => problem(403, "AUTH-0003", "missing duty planning.run")],
      ["/ddmrp/buffers", () => json({ items: [] })],
    );
    renderAt(`/planning/ddmrp-buffers?profile=${profile.id}`);
    await screen.findByRole("option", { name: "BUF-MFG" });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Buffer site"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Average daily usage (ADU)"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Decoupled lead time (days)"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Recalculate buffer" }));
    expect(await screen.findByText("The buffer was not recalculated")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
  });
});
