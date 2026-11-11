// The people of Cedar Ridge Pumps, shared by the HR and shop floor time demo handlers. The six shop floor workers of
// company.ts keep their ids and personnel numbers; the managers, office staff and newer hires are added here.
import { USER, WORKERS } from "../../company";
import { day, uid } from "../../store";

export const KIND_HR_WORKER = 8;

export type Department = "OPERATIONS" | "PRODUCTION" | "QUALITY" | "MAINTENANCE" | "HUMAN-RESOURCES";

export interface Person {
  id: string;
  number: string;
  name: string;
  preferredName?: string;
  title: string;
  department: Department;
  jobCode: string;
  /** Shop floor resource code for hourly floor staff; they clock in and book jobs. */
  resource?: string;
  /** Hourly rate (hourly staff) or annual salary (salaried), USD. */
  pay: number;
  hourly: boolean;
  managerId: string | null;
  /** Hire date, days from today (negative). */
  hired: number;
  employmentType: "FULL_TIME" | "PART_TIME" | "TEMP" | "CONTRACTOR";
  city: string;
}

const worker = (number: string) => {
  const w = WORKERS.find((x) => x.number === number);
  if (!w) throw new Error(`demo worker ${number} not in company.ts`);
  return w;
};
const hr = (n: number) => uid(KIND_HR_WORKER, 100 + n);

export const JORDAN = hr(1);
export const RACHEL = hr(2);
export const TOM = hr(3);
export const ELENA = hr(4);

const floor = (number: string, title: string, jobCode: string, pay: number, managerId: string, hired: number, department: Department = "PRODUCTION", city = "Cedar Rapids"): Person => {
  const w = worker(number);
  return { id: w.id, number: w.number, name: w.name, title, department, jobCode, resource: w.resource, pay, hourly: true, managerId, hired, employmentType: "FULL_TIME", city };
};

export const PEOPLE: Person[] = [
  { id: JORDAN, number: "W-0101", name: USER.name, title: "Plant operations manager", department: "OPERATIONS", jobCode: "OPS-MGR", pay: 128000, hourly: false, managerId: null, hired: -2920, employmentType: "FULL_TIME", city: "Marion" },
  { id: RACHEL, number: "W-0105", name: "Rachel Osei", title: "HR manager", department: "HUMAN-RESOURCES", jobCode: "HR-MGR", pay: 104000, hourly: false, managerId: null, hired: -2110, employmentType: "FULL_TIME", city: "Cedar Rapids" },
  { id: TOM, number: "W-0109", name: "Thomas Brennan", preferredName: "Tom Brennan", title: "Production supervisor", department: "PRODUCTION", jobCode: "PROD-SUP", pay: 86000, hourly: false, managerId: JORDAN, hired: -3310, employmentType: "FULL_TIME", city: "Hiawatha" },
  { id: ELENA, number: "W-0118", name: "Elena Ruiz", title: "Quality engineer", department: "QUALITY", jobCode: "QA-ENG", pay: 92000, hourly: false, managerId: JORDAN, hired: -1490, employmentType: "FULL_TIME", city: "Cedar Rapids" },
  { id: hr(5), number: "W-0122", name: "Kevin Marsh", title: "Maintenance technician, lead", department: "MAINTENANCE", jobCode: "MAINT-TECH", pay: 36.4, hourly: true, managerId: JORDAN, hired: -2630, employmentType: "FULL_TIME", city: "Marion" },
  { id: hr(6), number: "W-0146", name: "Aisha Grant", title: "Production planner", department: "OPERATIONS", jobCode: "PLANNER", pay: 78000, hourly: false, managerId: JORDAN, hired: -910, employmentType: "FULL_TIME", city: "Cedar Rapids" },
  { id: hr(7), number: "W-0150", name: "Nadia Petrova", preferredName: "Nadia Petrov", title: "EHS coordinator", department: "OPERATIONS", jobCode: "EHS-COORD", pay: 74000, hourly: false, managerId: JORDAN, hired: -705, employmentType: "FULL_TIME", city: "Iowa City" },
  floor("W-0112", "CNC machinist II", "MACH-2", 31.5, TOM, -2410),
  floor("W-0127", "Lead assembler", "ASM-LEAD", 30.9, TOM, -3120, "PRODUCTION", "Marion"),
  floor("W-0133", "Test technician", "TEST-TECH", 30.25, ELENA, -1820, "QUALITY"),
  floor("W-0141", "Assembler II", "ASM-2", 27.4, TOM, -1290, "PRODUCTION", "Hiawatha"),
  floor("W-0158", "Paint and coating technician", "PAINT-TECH", 26.8, TOM, -1010),
  floor("W-0164", "Balancing technician", "BAL-TECH", 29.6, TOM, -615, "PRODUCTION", "Marion"),
  { id: hr(8), number: "W-0171", name: "Grace Liu", title: "Packer", department: "PRODUCTION", jobCode: "PACK-1", resource: "PCK-01", pay: 22.8, hourly: true, managerId: TOM, hired: -212, employmentType: "PART_TIME", city: "Cedar Rapids" },
  { id: hr(9), number: "W-0175", name: "Owen Fischer", title: "Assembler I", department: "PRODUCTION", jobCode: "ASM-1", resource: "ASM-01", pay: 24.1, hourly: true, managerId: TOM, hired: -12, employmentType: "FULL_TIME", city: "Cedar Rapids" },
];

export const personById = (id: string): Person | undefined => PEOPLE.find((p) => p.id === id);
export const person = (number: string): Person => {
  const p = PEOPLE.find((x) => x.number === number);
  if (!p) throw new Error(`demo person ${number} not in hr/people.ts`);
  return p;
};
export const displayName = (p: Person): string => p.preferredName ?? p.name;
export const hireDate = (p: Person): string => day(p.hired);
/** Party ids read like the person's party number, so a page that cannot resolve them still shows something sensible. */
export const partyIdOf = (p: Person): string => `PER-${p.number.slice(2)}`;
/** Everyone who clocks in on the shop floor: the hourly staff. */
export const FLOOR = PEOPLE.filter((p) => p.hourly);
export const reportsOf = (managerId: string): Person[] => PEOPLE.filter((p) => p.managerId === managerId);
