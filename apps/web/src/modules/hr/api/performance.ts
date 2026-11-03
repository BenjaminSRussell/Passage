import { id as seg, newKey, page, post, send } from "./client";

export type GoalStatus = "OPEN" | "ACHIEVED" | "CANCELLED";
export type ReviewStatus = "draft" | "self_submitted" | "signed_off";

export const GOAL_TITLE_MAX = 200;
export const GOAL_DESCRIPTION_MAX = 2000;
export const REVIEW_TEXT_MAX = 4000;
export const MIN_RATING = 1;
export const MAX_RATING = 5;

export interface Goal {
  id: string;
  workerId: string;
  legalEntityId?: string;
  title: string;
  description?: string;
  dueOn?: string;
  status: GoalStatus;
  progressPercent: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewGoal {
  title: string;
  description?: string;
  dueOn?: string;
}

export interface GoalProgress {
  status?: GoalStatus;
  progressPercent?: number;
}

export interface Review {
  id: string;
  workerId: string;
  legalEntityId?: string;
  periodStart: string;
  periodEnd: string;
  status: ReviewStatus;
  selfAchievements?: string;
  selfDevelopment?: string;
  selfSubmittedAt?: string;
  managerComments?: string;
  overallRating?: number;
  signedOffBy?: string;
  signedOffAt?: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewReview {
  periodStart: string;
  periodEnd: string;
}

export interface SelfAssessment {
  achievements: string;
  development?: string;
}

export interface SignOff {
  managerComments?: string;
  overallRating?: number;
}

const command = <T,>(path: string, body: unknown) => send<T>(path, { method: "POST", body: JSON.stringify(body) });

export async function listWorkerGoals(workerId: string, signal?: AbortSignal): Promise<Goal[]> {
  return (await page<Goal>(`/workers/${seg(workerId)}/goals`, signal)).items;
}

export function createGoal(workerId: string, input: NewGoal, key: string = newKey()): Promise<Goal> {
  return post<Goal>(`/workers/${seg(workerId)}/goals`, input, key);
}

/** Setting the same values again changes nothing on the service, so no key is needed. */
export function updateGoalProgress(goalId: string, input: GoalProgress): Promise<Goal> {
  return command<Goal>(`/goals/${seg(goalId)}/progress`, input);
}

export async function listMyGoals(signal?: AbortSignal): Promise<Goal[]> {
  return (await page<Goal>("/me/goals", signal)).items;
}

/** The signed-in worker reports progress on their own goal; the status can be OPEN or ACHIEVED. */
export function updateMyGoalProgress(goalId: string, input: GoalProgress): Promise<Goal> {
  return command<Goal>(`/me/goals/${seg(goalId)}/progress`, input);
}

export async function listWorkerReviews(workerId: string, signal?: AbortSignal): Promise<Review[]> {
  return (await page<Review>(`/workers/${seg(workerId)}/reviews`, signal)).items;
}

export function openReview(workerId: string, input: NewReview, key: string = newKey()): Promise<Review> {
  return post<Review>(`/workers/${seg(workerId)}/reviews`, input, key);
}

export function getReview(reviewId: string, signal?: AbortSignal): Promise<Review> {
  return send<Review>(`/reviews/${seg(reviewId)}`, {}, signal);
}

export function signOffReview(reviewId: string, input: SignOff): Promise<Review> {
  return command<Review>(`/reviews/${seg(reviewId)}/sign-off`, input);
}

export async function listMyReviews(signal?: AbortSignal): Promise<Review[]> {
  return (await page<Review>("/me/reviews", signal)).items;
}

/** Step one of the two-step review. Sending the same text again changes nothing. */
export function submitSelfAssessment(reviewId: string, input: SelfAssessment): Promise<Review> {
  return command<Review>(`/me/reviews/${seg(reviewId)}/self-assessment`, input);
}
