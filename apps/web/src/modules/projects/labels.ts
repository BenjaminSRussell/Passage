import type { BadgeTone } from "../../shell/ui";
import type { ProjectType, Stage } from "./api/client";

export const dash = "—";

export const stageLabel: Record<Stage, string> = {
  CREATED: "Created",
  IN_PROCESS: "In process",
  ON_HOLD: "On hold",
  FINISHED: "Finished",
};

export const stageTone: Record<Stage, BadgeTone> = {
  CREATED: "neutral",
  IN_PROCESS: "info",
  ON_HOLD: "warning",
  FINISHED: "success",
};

export const typeLabel: Record<ProjectType, string> = {
  TIME_MATERIAL: "Time and material",
  FIXED_PRICE: "Fixed price",
  INTERNAL: "Internal",
  COST: "Cost",
  INVESTMENT: "Investment",
  TIME: "Time",
};

