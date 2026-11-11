import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CalendarDetailPage, CalendarsPage } from "./detail";
import { CapabilitiesPage, ResourceGroupsPage, ResourcesPage } from "./resources";
import { CalendarCreatePage, CapabilityCreatePage, parseDay, parseWeek, ResourceCreatePage, ResourceGroupCreatePage } from "./setup";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, detail: string, extra: object = {}) => ({ ok: false, status, json: async () => ({ code, detail, title: "x", ...extra }) });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const type = (label: RegExp | string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const sent = (fetchMock: ReturnType<typeof stub>) => {
  const [url, init] = fetchMock.mock.calls[0];
  return { url: String(url), init: init as RequestInit, body: JSON.parse(String(init?.body)) };
};

describe("parseDay", () => {
  it("reads comma separated ranges, an efficiency after @, and treats empty text as a closed day", () => {
    expect(parseDay("08:00-12:00, 13:00 - 17:00 @ 92.5")).toEqual({
      shifts: [
        { start: "08:00", end: "12:00" },
        { start: "13:00", end: "17:00", efficiencyPct: "92.5" },
      ],
    });
    expect(parseDay("  ")).toEqual({ shifts: [] });
  });

  it("accepts 24:00 as an end and an end before the start as a shift past midnight", () => {
    expect(parseDay("16:00-24:00").error).toBeUndefined();
    expect(parseDay("22:00-06:00").error).toBeUndefined();
  });

  it.each([
    ["morning", /is not a range/],
    ["8:00-12:00", /is not a range/],
    ["06:00-25:00", /does not exist/],
    ["06:60-10:00", /does not exist/],
    ["24:00-02:00", /starts at 24:00/],
    ["06:00-06:00", /no length/],
    ["06:00-10:00, 09:00-12:00", /overlap at 09:00/],
    ["22:00-06:00, 23:00-23:30", /overlap at 23:00/],
    ["00:00-01:00, 02:00-03:00, 04:00-05:00, 06:00-07:00, 08:00-09:00, 10:00-11:00", /at most 5 shifts/],
    ["06:00-10:00 @ 0", /efficiency/],
    ["06:00-10:00 @ 99.999", /efficiency/],
  ])("rejects %s as the service would (ENG-CAL-SHIFT)", (text, message) => {
    expect(parseDay(text).error).toMatch(message);
  });
});

describe("parseWeek", () => {
  const closed = ["", "", "", "", "", "", ""];
  const week = (patch: Record<number, string>) => closed.map((d, i) => patch[i] ?? d);

  it("rejects a night shift that runs into the next day's first shift, Sunday into Monday too", () => {
    expect(parseWeek(week({ 0: "22:00-06:00", 1: "05:00-13:00" })).errors).toEqual({ "day-0": "A shift running to 06:00 the next morning overlaps the Tuesday shift 05:00-13:00." });
    expect(parseWeek(week({ 6: "23:00-01:00", 0: "00:30-08:00" })).errors).toHaveProperty("day-6");
  });

  it("accepts a night shift that ends as the next shift starts", () => {
    const { template, errors } = parseWeek(week({ 0: "22:00-06:00", 1: "06:00-14:00" }));
    expect(errors).toEqual({});
    expect(template[1]).toEqual([{ start: "06:00", end: "14:00" }]);
  });
});

describe("CapabilityCreatePage", () => {
  it("posts the capability with an idempotency key and reports the new id", async () => {
    const fetchMock = stub(() => ok({ id: "c1", code: "WELD", name: "Welding" }, 201));
    const created: string[] = [];
    render(<CapabilityCreatePage onCreated={(id) => created.push(id)} />);
    type("Code", "WELD");
    type("Name", "Welding");
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    await waitFor(() => expect(created).toEqual(["c1"]));
    const { url, init, body } = sent(fetchMock);
    expect(url).toBe("/api/v1/engineering/capabilities");
    expect(init.method).toBe("POST");
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(body).toEqual({ code: "WELD", name: "Welding" });
  });

  it("shows field messages from a 400 and keeps the key for an unchanged retry", async () => {
    const fetchMock = stub(() => problem(400, "COM-0001", "Validation failed", { errors: { code: "must be 1 to 64 characters" } }));
    render(<CapabilityCreatePage />);
    type("Code", "x");
    type("Name", "X");
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    expect(await screen.findByText("must be 1 to 64 characters")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const keys = fetchMock.mock.calls.map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys[0]).toBe(keys[1]);
  });

  it("uses a new idempotency key once the body changes", async () => {
    const fetchMock = stub(() => problem(409, "ENG-DUPLICATE", "a record with this number or Idempotency-Key already exists or is in flight"));
    render(<CapabilityCreatePage />);
    type("Code", "WELD");
    type("Name", "Welding");
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    expect(await screen.findByText(/Another record already uses this code/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Code")).toHaveFocus());
    type("Code", "WELD2");
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const keys = fetchMock.mock.calls.map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys[0]).not.toBe(keys[1]);
  });

  it("explains a 403", async () => {
    stub(() => problem(403, "COM-0003", "no"));
    render(<CapabilityCreatePage />);
    type("Code", "A");
    type("Name", "A");
    fireEvent.click(screen.getByRole("button", { name: "Create capability" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to create capabilities.");
  });
});

describe("ResourceGroupCreatePage", () => {
  it("posts the group and leaves an empty calendar out", async () => {
    const fetchMock = stub(() => ok({ id: "g1" }, 201));
    const created: string[] = [];
    render(<ResourceGroupCreatePage onCreated={(id) => created.push(id)} />);
    type("Code", "CELL1");
    type("Name", "Cell 1");
    type(/^Site/, "S1");
    fireEvent.click(screen.getByRole("button", { name: "Create resource group" }));
    await waitFor(() => expect(created).toEqual(["g1"]));
    expect(sent(fetchMock).body).toEqual({ code: "CELL1", name: "Cell 1", siteId: "S1" });
  });
});

describe("ResourceCreatePage", () => {
  it("posts a machine with capabilities and shows the vendor field only for vendors", async () => {
    const fetchMock = stub(() => ok({ id: "r1" }, 201));
    const created: string[] = [];
    render(<ResourceCreatePage onCreated={(id) => created.push(id)} />);
    expect(screen.queryByLabelText(/Vendor party/)).toBeNull();
    type("Code", "M1");
    type("Name", "Mill");
    type(/^Site/, "S1");
    type(/^Efficiency/, "90");
    fireEvent.click(screen.getByLabelText("Exclusive"));
    fireEvent.click(screen.getByRole("button", { name: "Add capability" }));
    type(/^Capability id 1/, "cap-1");
    type(/^Effective from 1/, "2026-01-01");
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    await waitFor(() => expect(created).toEqual(["r1"]));
    expect(sent(fetchMock).body).toEqual({
      code: "M1",
      name: "Mill",
      type: "machine",
      siteId: "S1",
      exclusive: true,
      efficiencyPct: "90",
      capabilities: [{ capabilityId: "cap-1", level: 1, effectiveFrom: "2026-01-01" }],
    });
  });

  it("sends the vendor party for a vendor and removes a capability row", async () => {
    const fetchMock = stub(() => ok({ id: "r2" }, 201));
    render(<ResourceCreatePage />);
    type("Type", "vendor");
    type("Code", "V1");
    type("Name", "Outside");
    type(/^Site/, "S1");
    type(/^Vendor party/, "party-9");
    fireEvent.click(screen.getByRole("button", { name: "Add capability" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove capability 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(sent(fetchMock).body).toMatchObject({ type: "vendor", vendorPartyId: "party-9", capabilities: [] });
  });

  it("puts ENG-RES-SITE on the group field and moves focus there", async () => {
    stub(() => problem(422, "ENG-RES-SITE", "resource site must equal the site of its resource group"));
    render(<ResourceCreatePage />);
    type("Code", "M1");
    type("Name", "Mill");
    type(/^Site/, "S1");
    type(/^Resource group/, "7f6c2b8e-0000-4000-8000-000000000001");
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    const group = screen.getByLabelText(/^Resource group/);
    await waitFor(() => expect(group).toHaveFocus());
    expect(group).toHaveAttribute("aria-invalid", "true");
    expect(group).toHaveAccessibleDescription(/resource site must equal the site of its resource group/);
  });

  it("puts ENG-RES-VENDOR on the vendor field", async () => {
    stub(() => problem(422, "ENG-RES-VENDOR", "resource of type vendor requires vendorPartyId"));
    render(<ResourceCreatePage />);
    type("Type", "vendor");
    type("Code", "V1");
    type("Name", "Outside");
    type(/^Site/, "S1");
    type(/^Vendor party/, "party-9");
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    await waitFor(() => expect(screen.getByLabelText(/^Vendor party/)).toHaveFocus());
    expect(screen.getByLabelText(/^Vendor party/)).toHaveAccessibleDescription(/requires vendorPartyId/);
  });

  it("checks decimals and levels before calling the service", async () => {
    const fetchMock = stub(() => ok({ id: "r1" }, 201));
    render(<ResourceCreatePage />);
    type("Code", "M1");
    type("Name", "Mill");
    type(/^Site/, "S1");
    type(/^Efficiency/, "100.5");
    type(/^Capacity/, "abc");
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    await waitFor(() => expect(screen.getByLabelText(/^Efficiency/)).toHaveFocus());
    expect(screen.getByLabelText(/^Efficiency/)).toHaveAccessibleDescription(/above 0 to 100/);
    expect(screen.getByLabelText(/^Capacity/)).toHaveAccessibleDescription(/at most four decimals/);
    expect(fetchMock).not.toHaveBeenCalled();
    type(/^Efficiency/, " 87.5 ");
    type(/^Capacity/, "16");
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(sent(fetchMock).body).toMatchObject({ efficiencyPct: "87.5", capacityPerDayH: "16" });
  });
});

describe("CalendarCreatePage", () => {
  it("posts the weekly template Monday first and the closed dates", async () => {
    const fetchMock = stub(() => ok({ id: "k1" }, 201));
    const created: string[] = [];
    render(<CalendarCreatePage onCreated={(id) => created.push(id)} />);
    type("Code", "DAY");
    type("Name", "Day shift");
    type("Monday", "06:00-14:00, 14:30-16:00");
    type("Friday", "");
    type(/^Closed dates/, "2026-12-25, 2026-12-26");
    fireEvent.click(screen.getByRole("button", { name: "Create calendar" }));
    await waitFor(() => expect(created).toEqual(["k1"]));
    const { body } = sent(fetchMock);
    expect(body.template).toHaveLength(7);
    expect(body.template[0]).toEqual([
      { start: "06:00", end: "14:00" },
      { start: "14:30", end: "16:00" },
    ]);
    expect(body.template[4]).toEqual([]);
    expect(body.template[5]).toEqual([]);
    expect(body.closedDates).toEqual(["2026-12-25", "2026-12-26"]);
  });

  it("blocks overlapping shifts and bad closed dates on their fields without calling the service", async () => {
    const fetchMock = stub(() => ok({}));
    render(<CalendarCreatePage />);
    type("Code", "DAY");
    type("Name", "Day");
    type("Tuesday", "06:00-14:00, 13:00-20:00");
    type(/^Closed dates/, "2026-02-30");
    fireEvent.click(screen.getByRole("button", { name: "Create calendar" }));
    await waitFor(() => expect(screen.getByLabelText("Tuesday")).toHaveFocus());
    expect(screen.getByLabelText("Tuesday")).toHaveAccessibleDescription("Shifts overlap at 13:00.");
    expect(screen.getByLabelText(/^Closed dates/)).toHaveAccessibleDescription(/2026-02-30 is not a date/);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("sends a night shift and its efficiency", async () => {
    const fetchMock = stub(() => ok({ id: "k2" }, 201));
    render(<CalendarCreatePage />);
    type("Code", "NIGHT");
    type("Name", "Nights");
    type("Monday", "22:00-06:00 @ 90");
    fireEvent.click(screen.getByRole("button", { name: "Create calendar" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(sent(fetchMock).body.template[0]).toEqual([{ start: "22:00", end: "06:00", efficiencyPct: "90" }]);
  });

  it("shows ENG-CAL-SHIFT and template messages from the service in the form alert", async () => {
    stub(() => problem(422, "ENG-CAL-SHIFT", "Wednesday: shifts overlap at 09:00"));
    render(<CalendarCreatePage />);
    type("Code", "DAY");
    type("Name", "Day");
    fireEvent.click(screen.getByRole("button", { name: "Create calendar" }));
    expect(await findAlert()).toHaveTextContent("Wednesday: shifts overlap at 09:00");
  });

  it("keeps a field message the form has no field for", async () => {
    stub(() => problem(400, "COM-0001", "validation failed", { errors: { template: "must hold 7 day patterns, Monday first" } }));
    render(<CalendarCreatePage />);
    type("Code", "DAY");
    type("Name", "Day");
    fireEvent.click(screen.getByRole("button", { name: "Create calendar" }));
    expect(await findAlert()).toHaveTextContent("validation failed; template must hold 7 day patterns, Monday first");
  });
});

describe("Generate days on the calendar screen", () => {
  const calendar = { id: "k1", code: "DAY", name: "Day", template: [[], [], [], [], [], [], []], closedDates: [], version: 1 };

  it("generates a range, reports the counts and refreshes the generated range", async () => {
    let generated = false;
    const fetchMock = stub((_url, init) => {
      if (init?.method === "POST") {
        generated = true;
        return ok({ openDays: 21, totalHours: "168" });
      }
      return ok(generated ? { ...calendar, generatedFrom: "2026-01-01", generatedTo: "2026-01-31" } : calendar);
    });
    render(
      <MemoryRouter>
        <CalendarDetailPage id="k1" canGenerate />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("From"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-01-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate days" }));
    expect(await screen.findByText("Generated 21 open days, 168 hours.")).toBeInTheDocument();
    expect(await screen.findByText("2026-01-01 to 2026-01-31")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST")!;
    expect(String(post[0])).toBe("/api/v1/engineering/calendars/k1:generate");
    expect(JSON.parse(String(post[1]?.body))).toEqual({ from: "2026-01-01", to: "2026-01-31" });
  });

  it("is hidden without the write duty and explains a 403", async () => {
    stub((_url, init) => (init?.method === "POST" ? problem(403, "AUTH-0003", "forbidden") : ok(calendar)));
    const { unmount } = render(
      <MemoryRouter>
        <CalendarDetailPage id="k1" />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("table", { name: "Weekly template" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Generate days" })).toBeNull();
    unmount();
    render(
      <MemoryRouter>
        <CalendarDetailPage id="k1" canGenerate />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("From"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-01-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate days" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to generate calendar days.");
  });

  it("shows ENG-CAL-RANGE", async () => {
    stub((_url, init) => (init?.method === "POST" ? problem(422, "ENG-CAL-RANGE", "The range is longer than 60 months") : ok(calendar)));
    render(
      <MemoryRouter>
        <CalendarDetailPage id="k1" canGenerate />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText("From"), { target: { value: "2026-01-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2036-01-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate days" }));
    expect(await findAlert()).toHaveTextContent("The range is longer than 60 months");
  });
});

describe("list pages offer the create forms", () => {
  it.each([
    ["resources", "New resource", ResourcesPage],
    ["resource groups", "New resource group", ResourceGroupsPage],
    ["capabilities", "New capability", CapabilitiesPage],
    ["calendars", "New calendar", CalendarsPage],
  ] as const)("the %s list has a %s button", async (_name, label, Page) => {
    stub(() => ok({ items: [] }));
    const onCreate = vi.fn();
    render(
      <MemoryRouter>
        <Page onCreate={onCreate} />
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole("button", { name: label }));
    expect(onCreate).toHaveBeenCalled();
    expect(await screen.findByText(/^No /)).toBeInTheDocument();
  });
});
