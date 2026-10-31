import {
  page,
  send,
  type AsOfView,
  type Page,
  type PIIView,
  type Worker,
} from "./client";

export interface MyProfile extends Worker {
  pii: PIIView;
  asOf: AsOfView;
}

export interface TeamMember extends Worker {
  asOf: AsOfView;
}

export function getMyProfile(signal?: AbortSignal): Promise<MyProfile> {
  return send<MyProfile>("/me", {}, signal);
}

export function listMyTeam(
  cursor?: string,
  signal?: AbortSignal,
): Promise<Page<Worker>> {
  return page<Worker>(
    `/me/team${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
    signal,
  );
}

export function getTeamMember(
  workerId: string,
  signal?: AbortSignal,
): Promise<TeamMember> {
  return send<TeamMember>(
    `/me/team/${encodeURIComponent(workerId)}`,
    {},
    signal,
  );
}
