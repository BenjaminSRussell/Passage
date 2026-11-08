import { useEffect, useState } from "react";
import { describeProblem, type Problem } from "./api/client";
import { getHierarchyVersion, listHierarchyVersions } from "./api/hierarchy";
import { todayIso, versionAsOf } from "./hierarchyTree";

// Parents of org units live in hierarchy versions (PRD 1.6.4), never on the unit itself. The operating-unit page
// reads the version of one hierarchy type that is in force today and shows each unit's parent from it.

export type UnitParentsState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; error: Problem }
  // versionNo is null when the hierarchy type has no version in force today.
  | { status: "ready"; versionNo: number | null; parentOf: ReadonlyMap<string, string | null> };

export async function loadUnitParents(typeId: string, date: string, signal?: AbortSignal): Promise<{ versionNo: number | null; parentOf: Map<string, string | null> }> {
  const versions = await listHierarchyVersions(typeId, undefined, { signal });
  const inForce = versionAsOf(versions, date);
  if (inForce === null) {
    return { versionNo: null, parentOf: new Map() };
  }
  const version = await getHierarchyVersion(inForce.id, { signal });
  return { versionNo: version.versionNo, parentOf: new Map(version.nodes.map((node) => [node.orgUnitId, node.parentOrgUnitId])) };
}

export function useUnitParents(typeId: string): UnitParentsState {
  const [state, setState] = useState<UnitParentsState>({ status: "idle" });
  useEffect(() => {
    if (typeId === "") {
      setState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    setState({ status: "loading" });
    loadUnitParents(typeId, todayIso(), controller.signal).then(
      (result) => setState({ status: "ready", ...result }),
      (failure) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", error: describeProblem(failure) });
        }
      },
    );
    return () => controller.abort();
  }, [typeId]);
  return state;
}

// parentCell is what the Parent column shows for one unit: its parent's code, "Top level" for a root, and a dash for
// a unit that is not in the version or while no hierarchy is chosen or loaded.
export function parentCell(state: UnitParentsState, unitId: string, codeOf: (id: string) => string | undefined): string {
  if (state.status !== "ready" || !state.parentOf.has(unitId)) {
    return "—";
  }
  const parent = state.parentOf.get(unitId);
  return parent === null || parent === undefined ? "Top level" : (codeOf(parent) ?? "—");
}
