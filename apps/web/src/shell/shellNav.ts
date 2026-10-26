import type { NavItem } from "../modules/registry";
import { copy } from "./copy";
import type { IconName } from "./ui";

export interface ShellNavItem extends NavItem {
  icon: IconName;
}

export const shellNav: ShellNavItem[] = [
  { label: copy.nav.home, to: "/", icon: "home" },
  { label: copy.nav.notes, to: "/notes", icon: "note" },
];
