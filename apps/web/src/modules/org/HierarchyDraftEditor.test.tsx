import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

  it("does not let Ctrl+S start a save from the discard confirmation", async () => {
    const { fetchMock, user } = await dirtyDraft({});
    await user.keyboard("{Escape}");
    expect(dialog()).toBeInTheDocument();
    await user.keyboard("{Control>}s{/Control}");
    expect(callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT")).toHaveLength(0);
    expect(dialog()).toBeInTheDocument();
    await user.click(within(dialog()).getByRole("button", { name: "Keep editing" }));
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
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

  it("lists server-reported removals when the draft is only viewed", async () => {
    const { user } = renderPage({ ...units, ...removedRoutes }, ["org.read"]);
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    const section = await screen.findByRole("region", { name: "Removed since v2" });
    expect(within(section).getByText("Beta")).toBeInTheDocument();
    expect(within(section).getByText("was under Passage Inc")).toBeInTheDocument();
    expect(within(section).getByText("ou-x")).toBeInTheDocument();
    expect(within(section).getByText("was under no parent (root)")).toBeInTheDocument();
  });

  it("reflects unsaved edits: removals, moves and additions track the draft as edited", async () => {
    const { user } = await openDraft();
    await waitFor(() => expect(screen.getByTitle("Previously under Alpha (ALPHA)")).toBeInTheDocument());
    expect(screen.queryByText(/Removed since/)).toBeNull();

    await user.click(screen.getByText("Gamma"));
    await user.selectOptions(screen.getByLabelText("Move under"), "ou-a");
    await user.click(screen.getByRole("button", { name: "Move" }));
    expect(screen.queryByTitle(/Previously under/)).toBeNull();
    expect(screen.queryAllByText("changed")).toHaveLength(0);

    await user.click(screen.getByText("Beta"));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    const section = screen.getByRole("region", { name: "Removed since v2" });
    expect(within(section).getByText("Beta")).toBeInTheDocument();
    expect(within(section).getByText("was under Passage Inc")).toBeInTheDocument();

    await user.click(screen.getByText("Alpha"));
    await user.selectOptions(screen.getByLabelText("Add org unit"), "ou-d");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByRole("treeitem", { name: "Delta" })).toHaveAccessibleDescription("Added in this version");
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
    const attached: boolean[] = [];
    const created = vi.fn((blob: Blob) => (blobs.push(blob), "blob:hierarchy"));
    const revoked = vi.fn();
    Object.assign(URL, { createObjectURL: created, revokeObjectURL: revoked });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
      attached.push(this.isConnected);
    });
    try {
      const { user } = renderPage();
      await openType(user);
      await screen.findByRole("tree");
      await user.click(screen.getByRole("button", { name: "Export CSV" }));
      expect(names).toEqual(["hierarchy-REP-v2.csv"]);
      expect(attached).toEqual([true]);
      expect(document.querySelector("a[download]")).toBeNull();
      await waitFor(() => expect(revoked).toHaveBeenCalledWith("blob:hierarchy"));
      expect(blobs[0].type).toBe("text/csv;charset=utf-8");
      expect(Array.from(new Uint8Array(await blobs[0].arrayBuffer()).slice(0, 3))).toEqual([0xef, 0xbb, 0xbf]);
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

const dialog = () => screen.getByRole("alertdialog");

async function dirtyDraft(overrides: Record<string, Reply> = {}) {
  const context = await openDraft(overrides);
  await context.user.click(screen.getByText("Alpha"));
  await context.user.click(screen.getByRole("button", { name: "Move down" }));
  return context;
}

describe("unsaved changes guard", () => {
  it("asks before a type change throws edits away, and keeps them on Keep editing", async () => {
    const { user } = await dirtyDraft();
    await user.selectOptions(screen.getByLabelText("Hierarchy type"), "");
    expect(dialog()).toHaveAccessibleName("Discard your unsaved changes?");
    expect(dialog()).toHaveAccessibleDescription(/Leaving now loses them/);
    expect(within(dialog()).getByRole("button", { name: "Keep editing" })).toHaveFocus();
    await user.click(within(dialog()).getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    expect(screen.getByLabelText("Hierarchy type")).toHaveValue("t1");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
  });

  it("discards and proceeds on confirm", async () => {
    const { user } = await dirtyDraft();
    await user.selectOptions(screen.getByLabelText("Hierarchy type"), "");
    await user.click(within(dialog()).getByRole("button", { name: "Discard changes" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.queryByRole("tree")).toBeNull();
    expect(screen.getByLabelText("Hierarchy type")).toHaveValue("");
  });

  it("closes the dialog with Escape and guards the date input, the slider, the version buttons and Show draft", async () => {
    const { user } = await dirtyDraft();
    fireEvent.change(screen.getByLabelText("As of date"), { target: { value: "2026-01-15" } });
    expect(dialog()).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    fireEvent.change(screen.getByRole("slider", { name: "Effective date slider" }), { target: { value: "10" } });
    await user.click(within(dialog()).getByRole("button", { name: "Keep editing" }));
    await user.click(screen.getByRole("button", { name: "View version 1" }));
    await user.click(within(dialog()).getByRole("button", { name: "Keep editing" }));
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    expect(dialog()).toBeInTheDocument();
    await user.click(within(dialog()).getByRole("button", { name: "Discard changes" }));
    expect(screen.getByRole("status")).toHaveTextContent("Version 2");
    expect(screen.queryByText("Unsaved changes")).toBeNull();
  });

  it("does not ask when there is nothing to lose, including after a move is undone by hand", async () => {
    const { user } = await dirtyDraft();
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Move up" }));
    expect(screen.getByText("No unsaved changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save draft" })).toBeDisabled();
    await user.selectOptions(screen.getByLabelText("Hierarchy type"), "");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.queryByRole("tree")).toBeNull();
  });

  it("warns on page unload only while there are unsaved edits", async () => {
    const { user } = await dirtyDraft();
    const dirtyEvent = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(dirtyEvent);
    expect(dirtyEvent.defaultPrevented).toBe(true);
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    const cleanEvent = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(cleanEvent);
    expect(cleanEvent.defaultPrevented).toBe(false);
  });

  it("blocks in-app navigation while dirty and stays put on Keep editing", async () => {
    const { router } = await dirtyDraft();
    await act(async () => void router.navigate("/org"));
    expect(router.state.location.pathname).toBe("/org/hierarchy");
    await clickInDialog("Keep editing");
    expect(router.state.location.pathname).toBe("/org/hierarchy");
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

const clickInDialog = (name: string) => userEvent.setup().click(within(dialog()).getByRole("button", { name }));

describe("permissions", () => {
  const withoutDraft = { "GET /hierarchy-types/t1/versions": () => json({ items: [versionList[1], versionList[2]] }) };

  it("shows no write controls to a read-only user, only a read-only notice for the draft", async () => {
    const { user } = renderPage({ ...units, ...withoutDraft }, ["org.read"]);
    await openType(user);
    await screen.findByRole("tree");
    expect(screen.queryByRole("button", { name: "Create draft" })).toBeNull();
    expect(screen.getByRole("button", { name: "Export CSV" })).toBeInTheDocument();
  });

  it("shows the draft read-only, with the reason, when write is missing", async () => {
    const { user } = renderPage(units, ["org.read", "org.hierarchy.publish"]);
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    expect(await screen.findByText("This draft is read-only for you")).toBeInTheDocument();
    expect(await screen.findByRole("treeitem", { name: "Gamma" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Move selected node" })).toBeNull();
    expect(screen.queryByRole("button", { name: /save|publish|remove/i })).toBeNull();
    expect(document.querySelector("[draggable='true']")).toBeNull();
  });

  it("offers Create draft only with the write duty, also from the empty state", async () => {
    const { user, unmount } = renderPage({ "GET /hierarchy-types/t1/versions": () => json({ items: [] }) }, ["org.read"]);
    await openType(user);
    expect(await screen.findByText("No versions yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create draft" })).toBeNull();
    expect(screen.getByText(/needs the hierarchy write duty/)).toBeInTheDocument();
    unmount();
    const writer = renderPage(withoutDraft, ["org.read", "org.hierarchy.write"]);
    await openType(writer.user);
    expect(await screen.findByRole("button", { name: "Create draft" })).toBeInTheDocument();
  });

  it("hides Publish without the publish duty but keeps editing", async () => {
    const { user } = renderPage(units, ["org.read", "org.hierarchy.write"]);
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    await screen.findByRole("group", { name: "Move selected node" });
    expect(screen.getByRole("button", { name: "Save draft" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Publish…" })).toBeNull();
  });

  it("refuses a past date without the backdate duty before sending", async () => {
    const { fetchMock, user } = renderPage({ ...units, ...saved }, ["org.read", "org.hierarchy.write", "org.hierarchy.publish"]);
    await openType(user);
    await screen.findByRole("tree");
    await user.click(screen.getByRole("checkbox", { name: "Show draft" }));
    await screen.findByRole("group", { name: "Move selected node" });
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2020-01-01" } });
    await user.type(screen.getByLabelText("Backdate reason"), "Correcting an old record");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText(/needs the backdate duty/)).toBeInTheDocument();
    expect(callsTo(fetchMock, ":publish", "POST")).toHaveLength(0);
  });

  it("states that publish dates are UTC", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    expect(screen.getByText(/Dates are in UTC\. Today is \d{4}-\d{2}-\d{2}\./)).toBeInTheDocument();
  });
});

describe("removing a node", () => {
  it("removes a leaf, announces it, selects the parent and frees it for the Add picker", async () => {
    const { fetchMock, user } = await openDraft();
    await user.click(screen.getByText("Gamma"));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta"]);
    expect(announcement()).toBe("Gamma removed");
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveFocus();
    expect(within(screen.getByLabelText("Add org unit")).getByRole("option", { name: "Gamma (GAMMA)" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft version 3 saved");
    expect(saveBody(fetchMock).nodes.map((entry) => entry.orgUnitId)).toEqual(expect.not.arrayContaining(["ou-c"]));
  });

  it("moves the children of a removed node up to its parent, in its place", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Beta"));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Gamma"]);
    expect(announcement()).toBe("Beta removed, its child moved up under Passage Inc");
  });

  it("never removes the root", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Passage Inc"));
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();
  });
});

describe("keeping your place across a refresh", () => {
  const reloads = () => {
    let reads = 0;
    return {
      "GET /hierarchy-versions/v3": () => (reads++ === 0 ? json({ ...versionList[0], version: 7, nodes: draftNodes }) : (new Promise<Response>(() => undefined) as never)),
    };
  };

  it("keeps the tree, selection and collapse state, and puts focus on the confirmation after Save", async () => {
    const { user } = await openDraft(reloads());
    await user.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.queryByRole("treeitem", { name: "Gamma" })).toBeNull();
    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    const message = await screen.findByText("Draft version 3 saved");
    expect(message.closest("[aria-live]")).toHaveFocus();
    expect(screen.getByRole("tree")).toBeInTheDocument();
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("treeitem", { name: "Gamma" })).toBeNull();
    expect(screen.getByText("No unsaved changes")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish…" })).toBeEnabled();
  });

  it("uses the new lock version for the next save without waiting for the refetch", async () => {
    const { fetchMock, user } = await openDraft(reloads());
    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("Draft version 3 saved");
    await user.click(screen.getByRole("button", { name: "Move down" }));
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT")).toHaveLength(2));
    expect(callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT").map((call) => bodyOf(call).version)).toEqual([7, 8]);
  });
});

describe("keyboard shortcuts and focus", () => {
  it("saves with Ctrl+S only when there is something to save", async () => {
    const { fetchMock, user } = await openDraft();
    act(() => screen.getByRole("treeitem", { name: "Alpha" }).focus());
    await user.keyboard("{Control>}s{/Control}");
    expect(callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT")).toHaveLength(0);
    await user.keyboard("{Alt>}{ArrowDown}{/Alt}");
    await user.keyboard("{Control>}s{/Control}");
    expect(await screen.findByText("Draft version 3 saved")).toBeInTheDocument();
    expect(callsTo(fetchMock, "/hierarchy-versions/v3/nodes", "PUT")).toHaveLength(1);
  });

  it("cancels with Esc after asking, and closes the publish panel first", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    expect(screen.getByRole("region", { name: "Publish draft" })).toBeInTheDocument();
    act(() => screen.getByRole("treeitem", { name: "Alpha" }).focus());
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("region", { name: "Publish draft" })).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();

    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    await user.keyboard("{Escape}");
    expect(dialog()).toHaveAccessibleName("Discard your changes?");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(order()).toEqual(["1Passage Inc", "2Beta", "3Gamma", "2Alpha"]);
    await user.keyboard("{Escape}");
    await user.click(within(dialog()).getByRole("button", { name: "Discard changes" }));
    expect(order()).toEqual(["1Passage Inc", "2Alpha", "2Beta", "3Gamma"]);
    expect(announcement()).toBe("Changes discarded");
  });

  it("moves focus to the moved node when the button that was pressed becomes disabled", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    expect(screen.getByRole("button", { name: "Move down" })).toBeDisabled();
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveFocus();
  });

  it("expands and collapses with the disclosure control while editing, without changing the selection", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Beta"));
    await user.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.getByRole("treeitem", { name: "Gamma" })).toBeInTheDocument();
  });
});

describe("accessibility of the open states", () => {
  const pageLevel = ["page-has-heading-one", "landmark-unique", "region", "bypass"];
  const clean = (root: ParentNode) => checkAxeAA(root).filter((violation) => !pageLevel.includes(violation.id));

  it("has no violations with the publish panel open, a backdate reason field and a failure alert", async () => {
    const { user } = await openDraft({ "POST /hierarchy-versions/v3:publish": () => json({ code: "ORG-HIER-OVERLAP", detail: "Overlaps" }, 409) });
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2020-01-01" } });
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Give a reason of 10 to 500 characters.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Backdate reason"), "Correcting an old record");
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    await screen.findByText("Could not publish the draft");
    expect(clean(screen.getByRole("region", { name: "Edit draft" }))).toEqual([]);
  });

  it("has no violations in the empty draft state or with the discard dialog open", async () => {
    const { user } = await openDraft({ "GET /hierarchy-versions/v3": () => json({ ...versionList[0], version: 7, nodes: [] }) }, () =>
      screen.findByText("This draft has no nodes yet"),
    );
    expect(clean(document.body)).toEqual(expect.not.arrayContaining([expect.objectContaining({ id: "button-name" })]));
    await user.selectOptions(screen.getByLabelText("Add org unit"), "le1");
    await user.click(screen.getByRole("button", { name: "Add" }));
    act(() => screen.getByRole("treeitem", { name: "Passage Inc" }).focus());
    await user.keyboard("{Escape}");
    expect(clean(dialog())).toEqual([]);
    expect(dialog()).toHaveAttribute("aria-modal", "true");
  });

  it("has no violations in the conflict alert", async () => {
    const { user } = await dirtyDraft({ "PUT /hierarchy-versions/v3/nodes": () => json({ code: "ORG-VERSION-CONFLICT", detail: "Stale" }, 409) });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await screen.findByText("This draft changed elsewhere");
    expect(clean(screen.getByRole("region", { name: "Edit draft" }))).toEqual([]);
  });
});

describe("operating units fail to load while editing", () => {
  it("warns that names fall back to ids", async () => {
    await openDraft({ "GET /operating-units": () => json({ code: "COM-0002", detail: "Down" }, 500) }, () => screen.findByRole("treeitem", { name: "ou-c" }));
    expect(await screen.findByText("Operating units did not load")).toBeInTheDocument();
    expect(screen.getByText(/Down \(COM-0002\)/)).toBeInTheDocument();
  });
});

describe("focus after an action disables the pressed button", () => {
  it("returns focus to the tree after Add, Move under and Replace", async () => {
    const { user } = await openDraft({
      "GET /operating-units": () =>
        json({
          items: [
            { id: "ou-a", code: "ALPHA", name: "Alpha", status: "active" },
            { id: "ou-b", code: "BETA", name: "Beta", status: "active" },
            { id: "ou-c", code: "GAMMA", name: "Gamma", status: "active" },
            { id: "ou-d", code: "DELTA", name: "Delta", status: "active" },
            { id: "ou-f", code: "FOX", name: "Fox", status: "active" },
          ],
        }),
    });
    await user.click(screen.getByText("Alpha"));
    await user.selectOptions(screen.getByLabelText("Add org unit"), "ou-d");
    await user.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveFocus();

    await user.click(screen.getByText("Beta"));
    await user.selectOptions(screen.getByLabelText("Move under"), "ou-a");
    await user.click(screen.getByRole("button", { name: "Move" }));
    expect(screen.getByRole("button", { name: "Move" })).toBeDisabled();
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveFocus();

    await user.click(screen.getByText("Delta"));
    await user.selectOptions(screen.getByLabelText("Replace selected with"), "ou-f");
    await user.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.getByRole("button", { name: "Replace" })).toBeDisabled();
    expect(screen.getByRole("treeitem", { name: "Fox" })).toHaveFocus();
  });

  it("focuses the replacement and the surviving selection after Discard", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByText("Beta"));
    await user.selectOptions(screen.getByLabelText("Replace selected with"), "ou-d");
    await user.click(screen.getByRole("button", { name: "Replace" }));
    expect(screen.getByRole("treeitem", { name: "Delta" })).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(screen.getByRole("button", { name: "Discard changes" })).toBeDisabled();
    expect(screen.getByRole("treeitem", { name: "Passage Inc" })).toHaveFocus();
  });
});

describe("editing while a save is pending", () => {
  it("locks the edit controls, Alt+arrow moves and dragging until the save returns", async () => {
    const { user } = await dirtyDraft({ "PUT /hierarchy-versions/v3/nodes": () => new Promise<Response>(() => undefined) as never });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Discard changes" })).toBeDisabled());
    const before = order();
    for (const name of ["Move up", "Move down", "Indent", "Outdent", "Remove", "Add", "Replace", "Move"]) {
      expect(screen.getByRole("button", { name })).toBeDisabled();
    }
    expect(screen.getByLabelText("Add org unit")).toBeDisabled();
    expect(rowOf("Gamma")).toHaveAttribute("draggable", "false");
    act(() => screen.getByRole("treeitem", { name: "Alpha" }).focus());
    await user.keyboard("{Alt>}{ArrowUp}{/Alt}");
    fireEvent.dragStart(rowOf("Beta"));
    fireEvent.drop(rowOf("Gamma"));
    expect(order()).toEqual(before);
  });
});

describe("tree reveal and disclosure control", () => {
  it("expands a collapsed parent so the moved node stays visible and focused", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.queryByRole("treeitem", { name: "Gamma" })).toBeNull();
    await user.click(screen.getByText("Alpha"));
    await user.selectOptions(screen.getByLabelText("Move under"), "ou-b");
    await user.click(screen.getByRole("button", { name: "Move" }));
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("treeitem", { name: "Alpha" })).toHaveFocus();
  });

  it("renders the disclosure control as a non-focusable, unnamed decoration", async () => {
    await openDraft();
    const toggle = screen.getByTestId("toggle-ou-b");
    expect(toggle.tagName).toBe("SPAN");
    expect(toggle).toHaveAttribute("aria-hidden", "true");
    expect(toggle).not.toHaveAttribute("aria-label");
    expect(screen.queryByRole("button", { name: /^(Expand|Collapse)$/ })).toBeNull();
  });
});

describe("publish panel details", () => {
  it("counts the backdate reason in code points, not UTF-16 units", async () => {
    const { fetchMock, user } = await openDraft();
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2020-01-01" } });
    fireEvent.change(screen.getByLabelText("Backdate reason"), { target: { value: "😀😀😀😀😀" } });
    await user.click(screen.getByRole("button", { name: "Publish version 3" }));
    expect(await screen.findByText("Give a reason of 10 to 500 characters.")).toBeInTheDocument();
    expect(callsTo(fetchMock, ":publish", "POST")).toHaveLength(0);
  });

  it("keeps the panel and a typed backdate reason on Esc, and closes it when the reason is empty", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2020-01-01" } });
    await user.type(screen.getByLabelText("Backdate reason"), "Correcting an old record");
    await user.keyboard("{Escape}");
    expect(screen.getByLabelText("Backdate reason")).toHaveValue("Correcting an old record");
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2099-01-01" } });
    await waitFor(() => expect(screen.queryByLabelText("Backdate reason")).toBeNull());
    fireEvent.keyDown(screen.getByRole("treeitem", { name: "Alpha" }), { key: "Escape" });
    expect(screen.queryByRole("region", { name: "Publish draft" })).toBeNull();
  });

  it("keeps a deliberately collapsed branch collapsed after an unrelated draft edit", async () => {
    const { user } = await openDraft();
    await user.click(screen.getByTestId("toggle-ou-b"));
    expect(screen.queryByRole("treeitem", { name: "Gamma" })).toBeNull();
    await user.click(screen.getByText("Alpha"));
    await user.click(screen.getByRole("button", { name: "Move down" }));
    expect(screen.queryByRole("treeitem", { name: "Gamma" })).toBeNull();
    expect(screen.getByRole("treeitem", { name: "Beta" })).toHaveAttribute("aria-expanded", "false");
  });
});
