import { apiFetch } from "../../../shell/api/http";
const json = (r: Response) => r.json();

export const listWorkItems = (assignee = "me") =>
  apiFetch(`/api/v1/workitems?assignee=${assignee}&status=pending`).then(json);

export const listRuns = () => apiFetch("/api/v1/batch/runs").then(json);

export const listTemplates = () => apiFetch("/api/v1/notify/templates").then(json);
