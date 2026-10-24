const json = (r: Response) => r.json();

export const listWorkItems = (assignee = "me") =>
  fetch(`/api/v1/workitems?assignee=${assignee}&status=pending`).then(json);

export const listRuns = () => fetch("/api/v1/batch/runs").then(json);

export const listTemplates = () => fetch("/api/v1/notify/templates").then(json);
