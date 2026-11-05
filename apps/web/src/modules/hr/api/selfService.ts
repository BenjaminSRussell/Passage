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

export type ProfileChangeKind = "PREFERRED_NAME" | "HOME_ADDRESS" | "BANK_ACCOUNT";
export type ProfileChangeStatus =
  | "APPLIED"
  | "PENDING"
  | "APPROVED"
  | "REJECTED"
  | "CANCELED";

export interface Address {
  line1: string;
  line2?: string;
  city: string;
  region?: string;
  postalCode: string;
  country: string;
}

/** One self-service change; summary is the masked new value. */
export interface ProfileChange {
  id: string;
  workerId: string;
  workerName: string;
  personnelNumber: string;
  kind: ProfileChangeKind;
  status: ProfileChangeStatus;
  summary: string;
  requestedBy: string;
  decidedBy?: string;
  decidedAt?: string;
  decisionNote?: string;
  createdAt: string;
}

export type NewProfileChange =
  | { kind: "PREFERRED_NAME"; preferredName: string }
  | { kind: "HOME_ADDRESS"; address: Address }
  | { kind: "BANK_ACCOUNT"; bankAccount: string };

/** Which approval queue: a manager's (direct reports' addresses) or HR's (every change). */
export type ChangeQueue = "team" | "hr";

const queuePath = (queue: ChangeQueue) =>
  queue === "team" ? "/me/team/profile-changes" : "/profile-changes";

function statusQuery(status?: string, cursor?: string): string {
  const params = new URLSearchParams();
  if (status) params.set("status", status);
  if (cursor) params.set("cursor", cursor);
  const text = params.toString();
  return text ? `?${text}` : "";
}

export function requestProfileChange(
  input: NewProfileChange,
): Promise<ProfileChange> {
  return send<ProfileChange>("/me/profile-changes", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export function listMyProfileChanges(
  signal?: AbortSignal,
): Promise<Page<ProfileChange>> {
  return page<ProfileChange>("/me/profile-changes", signal);
}

export function cancelProfileChange(changeId: string): Promise<ProfileChange> {
  return send<ProfileChange>(
    `/me/profile-changes/${encodeURIComponent(changeId)}/cancel`,
    { method: "POST" },
  );
}

export function listProfileChanges(
  queue: ChangeQueue,
  filter: { status?: ProfileChangeStatus | ""; cursor?: string } = {},
  signal?: AbortSignal,
): Promise<Page<ProfileChange>> {
  return page<ProfileChange>(
    queuePath(queue) + statusQuery(filter.status, filter.cursor),
    signal,
  );
}

export function decideProfileChange(
  queue: ChangeQueue,
  changeId: string,
  decision: "APPROVE" | "REJECT",
  note?: string,
): Promise<ProfileChange> {
  return send<ProfileChange>(
    `${queuePath(queue)}/${encodeURIComponent(changeId)}/decide`,
    {
      method: "POST",
      body: JSON.stringify(note ? { decision, note } : { decision }),
    },
  );
}
