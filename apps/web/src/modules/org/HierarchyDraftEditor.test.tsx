import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { bodyOf, callsTo, draftNodes, json, openType, published, renderPage, versionList, type Reply } from "./hierarchyTestKit";
import { stubLocalStorage } from "./testStorage";

beforeEach(() => void stubLocalStorage());
afterEach(() => vi.unstubAllGlobals());

const units = {
  "GET /operating-units": () =>
    json({
      items: [
        { id: "ou-a", code: "ALPHA", name: "Alpha", status: "active" },
        { id: "ou-b", code: "BETA", name: "Beta", status: "active" },
        { id: "ou-c", code: "GAMMA", name: "Gamma", status: "active" },
        { id: "ou-d", code: "DELTA", name: "Delta", status: "active" },
        { id: "ou-e", code: "OLD", name: "Retired", status: "inactive" },
      ],
    }),
};

const saved: Record<string, Reply> = {
  "PUT /hierarchy-versions/v3/nodes": () => json({ ...versionList[0], version: 8, nodes: draftNodes }),
};

async function openDraft(overrides: Record<string, Reply> = {}, ready: () => Promise<unknown> = () => screen.findByRole("treeitem", { name: "Gamma" })) {
  const context = renderPage({ ...units, ...saved, ...overrides });
  await openType(context.user);
  await screen.findByRole("tree");
  await context.user.click(screen.getByRole("checkbox", { name: "Show draft" }));
  await screen.findByRole("group", { name: "Move selected node" });
  await ready();
  return context;
}

const order = () =>
  screen
    .getAllByRole("treeitem")
    .map((item) => `${item.getAttribute("aria-level")}${document.getElementById(`tree-label-${item.dataset.id}`)?.textContent}`);

const rowOf = (name: string) => screen.getByText(name).closest("div") as HTMLElement;
const announcement = () => document.querySelector('[aria-live="polite"] .visually-hidden')?.textContent;
const saveBody = (fetchMock: ReturnType<typeof renderPage>["fetchMock"]) => {
  const calls = callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT");
  expect(calls).toHaveLength(1);
  return bodyOf(calls[0]) as { version: number; nodes: { orgUnitId: string; parentOrgUnitId: string | null; sortOrder: number }[] };
};

describe("draft editor shell", () => {
  it("shows edit controls only for the draft, with no unsaved changes and no passing axe violations", async () => {
    await openDraft();
    expect(screen.getByText("No unsaved changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Publish…" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Move up" })).toBeDisabled();
    expect(screen.getByText(/Select a node to move or replace it/)).toBeInTheDocument();
    const pageLevel = ["page-has-heading-one", "landmark-unique", "region", "bypass"];
    for (const root of [screen.getByRole("region", { name: "Edit draft" }), screen.getByRole("tree")]) {
      expect(checkAxeAA(root).filter((violation) => !pageLevel.includes(violation.id))).toEqual([]);
    }
  });

  it("marks the selected node and enables only the moves that apply", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Alpha"));
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Selected: Alpha")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move up" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Move down" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Indent" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Outdent" })).toBeDisabled();
    await user.click(screen.getByText("Passage Inc"));
    expect(screen.getByRole("button", { name: "Move down" })).toBeDisabled();
    expect(screen.getByLabelText("Move under")).toBeDisabled();
  });
});

describe("reparenting without a pointer", () => {
  it("moves a sibling up and down with buttons, announces the new position and saves the order", async () => {
    const { fetchMock, user } = await openDraft();
    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    expect(announcement()).toBe("Alpha is now under Passage Inc, position 2 of 2");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish…" })).toBeDisabled();
    expect(screen.getByText("Save your changes before publishing.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move down" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Move up" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta", "3Gamma"]);

    await user.click(screen.getByRole("button", { name: "Move down" }));
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Draft version 3 saved")).toBeInTheDocument();
    const body = saveBody(fetchMock);
    expect(body.version).toBe(7);
    expect(body.nodes.find((entry) => entry.orgUnitId === "ou-a")).toEqual({ orgUnitId: "ou-a", parentOrgUnitId: "le1", sortOrder: 1 });
    expect(body.nodes.find((entry) => entry.orgUnitId === "ou-b")).toEqual({ orgUnitId: "ou-b", parentOrgUnitId: "le1", sortOrder: 0 });
    await waitFor(() => expect(callsTo(fetchMock, "/hierarchy-versions/v3", "GET")).toHaveLength(2));
  });

  it("indents under the previous sibling and outdents after the parent", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Beta"));
    await user.click(screen.getByRole("button", { name: "Indent" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Beta", "4Gamma"]);
    await user.click(screen.getByText("Gamma"));
    await user.click(screen.getByRole("button", { name: "Outdent" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Beta", "3Gamma"]);
    await user.click(screen.getByText("Beta"));
    await user.click(screen.getByRole("button", { name: "Outdent" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Gamma", "2Beta"]);
  });

  it("moves under a parent chosen from the list, which never offers the node, its parent or its descendants", async () => {
    const { fetchMock, user } = await openDraft();
    await user.click(screen.getByText("Beta"));
    const choices = within(screen.getByLabelText("Move under"));
    expect(choices.queryByRole("option", { name: "Beta (BETA)" })).toBeNull();
    expect(choices.queryByRole("option", { name: "Passage Inc (PI)" })).toBeNull();
    expect(choices.queryByRole("option", { name: "Gamma (GAMMA)" })).toBeNull();
    expect(screen.getByRole("button", { name: "Move" })).toBeDisabled();
    await user.selectOptions(screen.getByLabelText("Move under"), "ou-a");
    await user.click(screen.getByRole("button", { name: "Move" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Beta", "4Gamma"]);
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft version 3 saved");
    expect(saveBody(fetchMock).nodes.find((entry) => entry.orgUnitId === "ou-b")?.parentOrgUnitId).toBe("ou-a");
  });

  it("moves with Alt and the arrow keys and keeps focus on the moved node", async () => {
    const { user } = await openDraft();
    act(() => screen.getByRole("treeitem", { name: "Beta" }).focus());
    await user.keyboard("{Alt>}{ArrowUp}{/Alt}");
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveFocus();
    await user.keyboard("{Alt>}{ArrowRight}{/Alt}");
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveFocus();
    expect(screen.getByText("Selected: Beta")).toBeInTheDocument();
  });
});

describe("drag and drop", () => {
  it("reparents a node dropped on a valid target and refuses its own descendants", async () => {
    await openDraft();
    expect(rowOf("Passage Inc")).toHaveAttribute("draggable", "false");
    expect(rowOf("Gamma")).toHaveAttribute("draggable", "true");

    fireEvent.dragStart(rowOf("Beta"));
    expect(fireEvent.dragOver(rowOf("Gamma"))).toBe(true);
    expect(fireEvent.dragOver(rowOf("Beta"))).toBe(true);
    fireEvent.drop(rowOf("Gamma"));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta", "3Gamma"]);
    fireEvent.dragEnd(rowOf("Beta"));

    fireEvent.dragStart(rowOf("Gamma"), { dataTransfer: { setData: vi.fn(), effectAllowed: "" } });
    expect(fireEvent.dragOver(rowOf("Alpha"))).toBe(false);
    fireEvent.drop(rowOf("Alpha"));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Gamma", "2Beta"]);
    expect(screen.getByRole("treeitem", { name: "Gamma" })).toHaveAttribute("aria-selected", "true");
    expect(announcement()).toBe("Gamma is now under Alpha, position 1 of 1");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
  });
});

describe("adding and replacing from a picker", () => {
  it("adds an active org unit that is not yet in the tree under the selected node", async () => {
    const { fetchMock, user } = await openDraft();
    const picker = within(screen.getByLabelText("Add org unit"));
    expect(picker.queryByRole("option", { name: "Alpha (ALPHA)" })).toBeNull();
    expect(picker.queryByRole("option", { name: "Retired (OLD)" })).toBeNull();
    expect(screen.getByText("Select a parent node first.")).toBeInTheDocument();
    expect(screen.getByLabelText("Add org unit")).toBeDisabled();

    await user.click(screen.getByText("Alpha"));
    expect(screen.getByText("It is added under Alpha.")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Add org unit"), "ou-d");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "3Delta", "2Beta", "3Gamma"]);
    expect(announcement()).toBe("Delta added under Alpha");
    expect(within(screen.getByLabelText("Add org unit")).queryByRole("option", { name: "Delta (DELTA)" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft version 3 saved");
    const added = saveBody(fetchMock).nodes.find((entry) => entry.orgUnitId === "ou-d");
    expect(added).toEqual({ orgUnitId: "ou-d", parentOrgUnitId: "ou-a", sortOrder: 0 });
  });

  it("replaces the selected node in place and keeps its children", async () => {
    const { fetchMock, user } = await openDraft();
    expect(screen.getByLabelText("Replace selected with")).toBeDisabled();
    await user.click(screen.getByText("Beta"));
    await user.selectOptions(screen.getByLabelText("Replace selected with"), "ou-d");
    await user.click(screen.getByRole("button", { name: "Replace" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Delta", "3Gamma"]);
    expect(screen.getByRole("treeitem", { name: "Delta" })).toHaveAttribute("aria-selected", "true");
    expect(announcement()).toBe("Beta replaced by Delta");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft version 3 saved");
    expect(saveBody(fetchMock).nodes.find((entry) => entry.orgUnitId === "ou-c")?.parentOrgUnitId).toBe("ou-d");
  });

  it("starts an empty draft with a root and keeps publish off until there are nodes saved", async () => {
    const { user } = await openDraft({ "GET /hierarchy-versions/v3": () => json({ ...versionList[0], version: 7, nodes: [] }) }, () =>
      screen.findByText("This draft has no nodes yet"),
    );
    expect(screen.getByText("This draft has no nodes yet")).toBeInTheDocument();
    expect(screen.queryByRole("tree")).toBeNull();
    expect(screen.getByRole("button", { name: "Publish…" })).toBeDisabled();
    expect(screen.getByText("It becomes the root of this hierarchy.")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Add org unit"), "le1");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(order()).toEqual(["1Passage Inc"]);
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
  });
});

describe("saving", () => {
  const dirtyDraft = async (overrides: Record<string, Reply>) => {
    const context = await openDraft(overrides);
    await context.user.click(screen.getByText("Alpha"));
    await context.user.click(screen.getByRole("button", { name: "Move down" }));
    return context;
  };

  it("keeps the edits and offers a reload when the draft changed elsewhere", async () => {
    const { fetchMock, user } = await dirtyDraft({ "PUT /hierarchy-versions/v3/nodes": () => json({ code: "ORG-VERSION-CONFLICT", detail: "Stale version" }, 409) });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("This draft changed elsewhere")).toBeInTheDocument();
    expect(screen.getByText(/Stale version \(ORG-VERSION-CONFLICT\)/)).toBeInTheDocument();
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    await user.click(screen.getByRole("button", { name: "Reload draft" }));
    await waitFor(() => expect(callsTo(fetchMock, "/hierarchy-versions/v3", "GET")).toHaveLength(2));
    expect(await screen.findByText("No unsaved changes")).toBeInTheDocument();
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta", "3Gamma"]);
  });

  it("explains a missing permission", async () => {
    const { user } = await dirtyDraft({ "PUT /hierarchy-versions/v3/nodes": () => json({ code: "AUTH-0003", detail: "Missing duty org.hierarchy.write" }, 403) });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("You don't have permission to do this")).toBeInTheDocument();
    expect(screen.getByText(/Missing duty org.hierarchy.write \(AUTH-0003\)/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeEnabled();
  });

  it("shows a server validation failure and discards local edits on request", async () => {
    const { user } = await dirtyDraft({ "PUT /hierarchy-versions/v3/nodes": () => json({ code: "ORG-HIER-CYCLE", detail: "Cycle detected" }, 422) });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Could not save the draft")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta", "3Gamma"]);
    expect(screen.queryByText("Could not save the draft")).toBeNull();
    expect(screen.getByText("No unsaved changes")).toBeInTheDocument();
    expect(announcement()).toBe("Changes discarded");
  });
});

describe("publishing", () => {
  const publishedReply = (effectiveFrom: string): Reply => () => json({ ...versionList[0], status: "published", effectiveFrom, version: 8, nodes: draftNodes });
  const openPublish = async (overrides: Record<string, Reply> = {}) => {
    const context = await openDraft(overrides);
    await context.user.click(screen.getByRole("button", { name: "Publish…" }));
    return context;
  };
  const setDate = (value: string) => fireEvent.change(screen.getByLabelText("Effective from"), { target: { value } });
  const publishCalls = (fetchMock: ReturnType<typeof renderPage>["fetchMock"]) => callsTo(fetchMock, "/hierarchy-versions/v3:publish", "POST");

  it("publishes a future date without asking for a reason", async () => {
    const { fetchMock, user } = await openPublish({ "POST /hierarchy-versions/v3:publish": publishedReply("2099-01-01") });
    setDate("2099-01-01");
    expect(screen.queryByLabelText("Backdate reason")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Version 3 published, effective from 2099-01-01")).toBeInTheDocument();
    expect(publishCalls(fetchMock)).toHaveLength(1);
    expect(bodyOf(publishCalls(fetchMock)[0])).toEqual({ version: 7, effectiveFrom: "2099-01-01", reason: "" });
    expect(new Headers(publishCalls(fetchMock)[0][1]?.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("requires a reason of 10 to 500 characters for a past date before it sends anything", async () => {
    const { fetchMock, user } = await openPublish({ "POST /hierarchy-versions/v3:publish": publishedReply("2020-01-01") });
    setDate("2020-01-01");
    expect(screen.getByText(/Required because this date is in the past/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Give a reason of 10 to 500 characters.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Backdate reason"), "too short");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(screen.getByText("Give a reason of 10 to 500 characters.")).toBeInTheDocument();
    expect(publishCalls(fetchMock)).toHaveLength(0);

    await user.clear(screen.getByLabelText("Backdate reason"));
    await user.type(screen.getByLabelText("Backdate reason"), "  Correcting the 2020 reorganisation  ");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Version 3 published, effective from 2020-01-01")).toBeInTheDocument();
    expect(bodyOf(publishCalls(fetchMock)[0])).toEqual({ version: 7, effectiveFrom: "2020-01-01", reason: "Correcting the 2020 reorganisation" });
  });

  it("refuses a past date when the type forbids backdating", async () => {
    const { fetchMock, user } = await openPublish({
      "GET /hierarchy-types?": () => json({ items: [{ id: "t1", code: "REP", name: "Reporting", purposes: ["reporting"], backdateAllowed: false, status: "active" }] }),
    });
    setDate("2020-01-01");
    await user.type(screen.getByLabelText("Backdate reason"), "A perfectly good reason");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText(/does not allow backdating/)).toBeInTheDocument();
    expect(publishCalls(fetchMock)).toHaveLength(0);
  });

  it("rejects an invalid date", async () => {
    const { fetchMock, user } = await openPublish();
    setDate("");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Enter a valid date.")).toBeInTheDocument();
    expect(publishCalls(fetchMock)).toHaveLength(0);
  });

  it.each([
    ["ORG-HIER-BACKDATE-DUTY", 403, "Backdated publish requires the Backdate duty and a reason.", "Could not publish the draft"],
    ["ORG-HIER-MAKER-CHECKER", 403, "You edited this draft last", "Could not publish the draft"],
    ["AUTH-0003", 403, "Missing duty org.hierarchy.publish", "You don't have permission to do this"],
    ["ORG-HIER-OVERLAP", 409, "Effective date is not after version 2", "Could not publish the draft"],
  ])("shows the %s refusal and stays on the draft", async (code, status, detail, title) => {
    const { user } = await openPublish({ "POST /hierarchy-versions/v3:publish": () => json({ code, detail }, status) });
    setDate("2099-01-01");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(new RegExp(`${detail.replace(/\./g, "\\.")} \\(${code}\\)`))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish version 3" })).toBeEnabled();
    expect(screen.getByRole("tree")).toBeInTheDocument();
  });
});

describe("creating a draft", () => {
  const withoutDraft = { "GET /hierarchy-types/t1/versions": () => json({ items: [versionList[1], versionList[2]] }) };

  it("creates a draft from the toolbar and confirms it", async () => {
    const { fetchMock, user } = renderPage({
      ...withoutDraft,
      "POST /hierarchy-types/t1/versions": () => json({ ...versionList[0], version: 1, nodes: published }, 201),
    });
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("button", { name: "Create draft" }));
    expect(await screen.findByText("Draft version 3 created")).toBeInTheDocument();
    const calls = callsTo(fetchMock, "/hierarchy-types/t1/versions", "POST");
    expect(calls).toHaveLength(1);
    expect(calls[0][1]?.body).toBeUndefined();
    expect(new Headers(calls[0][1]?.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it.each([
    ["ORG-HIER-DRAFT-EXISTS", 409, "An open draft already exists", "Could not create the draft"],
    ["AUTH-0003", 403, "Missing duty org.hierarchy.write", "You don't have permission to do this"],
  ])("shows the %s refusal", async (code, status, detail, title) => {
    const { user } = renderPage({ ...withoutDraft, "POST /hierarchy-types/t1/versions": () => json({ code, detail }, status) });
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("button", { name: "Create draft" }));
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(new RegExp(`${detail} \\(${code}\\)`))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create draft" })).toBeEnabled();
  });

  it("creates the first draft from the empty state", async () => {
    const { fetchMock, user } = renderPage({
      "GET /hierarchy-types/t1/versions": () => json({ items: [] }),
      "POST /hierarchy-types/t1/versions": () => json({ ...versionList[0], versionNo: 1, version: 1, nodes: [] }, 201),
    });
    await openType(user);
    await user.click(await screen.findByRole("button", { name: "Create draft" }));
    expect(await screen.findByText("Draft version 1 created")).toBeInTheDocument();
    expect(callsTo(fetchMock, "/hierarchy-types/t1/versions", "POST")).toHaveLength(1);
  });
});

describe("removed since the previous version", () => {
  const removedRoutes = {
    "GET /hierarchy-types/t1/compare": () =>
      json({
        hierarchyTypeId: "t1",
        fromVersionNo: 2,
        toVersionNo: 3,
        added: [],
        moved: [],
        removed: [{ orgUnitId: "ou-b", kind: "operating_unit", parentOrgUnitId: "le1" }, { orgUnitId: "ou-x", kind: "operating_unit", parentOrgUnitId: null }],
      }),
  };

  it("lists nodes present in the previous version and gone from the displayed one", async () => {
    await openDraft(removedRoutes);
    const section = screen.getByRole("region", { name: "Removed since v2" });
    expect(within(section).getByText("Beta")).toBeInTheDocument();
    expect(within(section).getByText("was under Passage Inc")).toBeInTheDocument();
    expect(within(section).getByText("ou-x")).toBeInTheDocument();
    expect(within(section).getByText("was under no parent (root)")).toBeInTheDocument();
  });

  it("leaves the section out when nothing was removed", async () => {
    await openDraft();
    expect(screen.queryByText(/Removed since/)).toBeNull();
  });
});

describe("export", () => {
  it("downloads the displayed version as CSV with a safe file name", async () => {
    const blobs: Blob[] = [];
    const names: string[] = [];
    const created = vi.fn((blob: Blob) => (blobs.push(blob), "blob:hierarchy"));
    const revoked = vi.fn();
    Object.assign(URL, { createObjectURL: created, revokeObjectURL: revoked });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
    try {
      const { user } = renderPage();
      await openType(user);
      await screen.findByRole("tree");
      await user.click(screen.getByRole("button", { name: "Export CSV" }));
      expect(names).toEqual(["hierarchy-REP-v2.csv"]);
      expect(revoked).toHaveBeenCalledWith("blob:hierarchy");
      expect(blobs[0].type).toBe("text/csv;charset=utf-8");
      expect(await blobs[0].text()).toBe(
        ["Level,Name,Code,Kind,Parent code", "1,Passage Inc,PI,legal_entity,", "2,Alpha,ALPHA,operating_unit,PI", "3,ou-c,ou-c,operating_unit,ALPHA", "2,Beta,BETA,operating_unit,PI"].join("\r\n"),
      );
    } finally {
      click.mockRestore();
    }
  });

  it("offers no export while nothing is displayed", async () => {
    const { user } = renderPage();
    await openType(user);
    await screen.findByRole("tree");
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2025-12-31" } });
    expect(screen.queryByRole("button", { name: "Export CSV" })).toBeNull();
  });
});

describe("operating units fail to load while editing", () => {
  it("warns that names fall back to ids", async () => {
    await openDraft({ "GET /operating-units": () => json({ code: "COM-0002", detail: "Down" }, 500) }, () => screen.findByRole("treeitem", { name: "ou-c" }));
    expect(await screen.findByText("Operating units did not load")).toBeInTheDocument();
    expect(screen.getByText(/Down \(COM-0002\)/)).toBeInTheDocument();
  });
});
