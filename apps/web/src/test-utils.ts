import { screen, waitFor, type ByRoleOptions } from "@testing-library/react";

export function findTable(options?: ByRoleOptions): Promise<HTMLElement> {
  return waitFor(() => {
    const table = screen.getByRole("table", options);
    if (table.getAttribute("aria-busy") === "true") throw new Error("table is still loading");
    return table;
  });
}

function findFilledRegion(role: "alert" | "status"): Promise<HTMLElement> {
  return waitFor(() => {
    const region = screen.getAllByRole(role).find((element) => element.textContent?.trim());
    if (!region) throw new Error(`no ${role} region has text yet`);
    return region;
  });
}

export const findAlert = () => findFilledRegion("alert");
export const findStatus = () => findFilledRegion("status");
