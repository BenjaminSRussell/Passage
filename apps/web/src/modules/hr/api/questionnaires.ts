import { id as seg, newKey, post, query, send, type Page } from "./client";

export type QuestionType = "SINGLE" | "MULTI" | "SCALE" | "NUMERIC" | "TEXT" | "DATE";
export const QUESTION_TYPES: QuestionType[] = ["SINGLE", "MULTI", "SCALE", "NUMERIC", "TEXT", "DATE"];

export interface QuestionOption {
  value: string;
  label: string;
  score?: string;
}

export interface Question {
  code: string;
  text: string;
  type: QuestionType;
  options?: QuestionOption[];
  scaleMax?: number;
  weight: string;
  mandatory: boolean;
  resultGroup?: string;
}

export interface Questionnaire {
  id: string;
  code: string;
  name: string;
  anonymous: boolean;
  questions: Question[];
}

export interface NewQuestionnaire {
  code: string;
  name: string;
  anonymous: boolean;
  questions: Array<Omit<Question, "weight"> & { weight?: string }>;
}

export type ScheduleStatus = "PLANNED" | "OPEN" | "CLOSED";

export interface Schedule {
  id: string;
  questionnaireId: string;
  population: string;
  opensAt: string;
  closesAt: string;
  invitedCount: number;
  status: ScheduleStatus;
}

export interface ScheduleView extends Schedule {
  questionnaire: Questionnaire;
}

export interface NewSchedule {
  questionnaireId: string;
  population?: string;
  opensAt: string;
  closesAt: string;
}

export type AnswerValue = string | string[] | number;

export interface QuestionStat {
  code: string;
  type: string;
  answered: number;
  mean?: string;
  distribution?: Record<string, number>;
}

export interface GroupStat {
  code: string;
  responses: number;
  meanScore?: string;
}

export interface Stats {
  respondents: number;
  questions: QuestionStat[];
  resultGroups: GroupStat[];
}

export interface QuestionnaireResults {
  scheduleId: string;
  anonymous: boolean;
  invited: number;
  responded: number;
  responseRate: string;
  overall: Stats;
  suppressed: boolean;
  groups?: Array<{ departmentId: string | null; stats: Stats }>;
}

export async function listQuestionnaires(signal?: AbortSignal): Promise<Questionnaire[]> {
  return (await send<Page<Questionnaire>>("/questionnaires", {}, signal)).items;
}

export function getQuestionnaire(questionnaireId: string, signal?: AbortSignal): Promise<Questionnaire> {
  return send<Questionnaire>(`/questionnaires/${seg(questionnaireId)}`, {}, signal);
}

export function createQuestionnaire(input: NewQuestionnaire, key: string = newKey()): Promise<Questionnaire> {
  return post<Questionnaire>("/questionnaires", input, key);
}

export function createSchedule(input: NewSchedule, key: string = newKey()): Promise<Schedule> {
  return post<Schedule>("/questionnaires/schedules", input, key);
}

export async function openSchedules(signal?: AbortSignal): Promise<Schedule[]> {
  return (await send<Page<Schedule>>("/questionnaires/schedules/open", {}, signal)).items;
}

export function getSchedule(scheduleId: string, signal?: AbortSignal): Promise<ScheduleView> {
  return send<ScheduleView>(`/questionnaires/schedules/${seg(scheduleId)}`, {}, signal);
}

export async function submitAnswers(scheduleId: string, workerId: string, answers: Record<string, AnswerValue>): Promise<void> {
  await send<unknown>(`/questionnaires/schedules/${seg(scheduleId)}/answers`, {
    method: "POST",
    body: JSON.stringify({ workerId, answers }),
  });
}

export function scheduleResults(scheduleId: string, byDepartment: boolean, signal?: AbortSignal): Promise<QuestionnaireResults> {
  return send<QuestionnaireResults>(
    `/questionnaires/schedules/${seg(scheduleId)}/results${query({ by: byDepartment ? "department" : undefined })}`,
    {},
    signal,
  );
}
