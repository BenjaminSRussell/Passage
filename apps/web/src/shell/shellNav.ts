import type { NavItem } from "../modules/registry";
import { copy } from "./copy";
import type { IconName } from "./ui";
import { ALL_FEATURES_PATH } from "./workspaces/model";

export interface ShellNavItem extends NavItem {
  icon: IconName;
}

export const shellNav: ShellNavItem[] = [
  { label: copy.nav.home, to: "/", icon: "home" },
  { label: copy.nav.allFeatures, to: ALL_FEATURES_PATH, icon: "grid" },
];
