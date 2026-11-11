import type { KeyValueStore, SealCipher } from "../../mobile-flows/domain/offlineStore";
import type { DayVisit, TechnicianDay } from "./types";

/** An in-memory store and a cipher that only reverses the text, enough to see that nothing is written in clear. */
export function memoryStore(): KeyValueStore<string> & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    get: async (k) => data.get(k) ?? null,
    put: async (k, v) => void data.set(k, v),
    delete: async (k) => void data.delete(k),
  };
}

export const reverseCipher: SealCipher = {
  seal: async (s) => [...s].reverse().join(""),
  open: async (s) => [...s].reverse().join(""),
};

export function visit(over: Partial<DayVisit> = {}): DayVisit {
  return {
    id: "00000000-0000-4000-8000-0000000000a1",
    number: "SWO-000041",
    status: "DISPATCHED",
    version: 3,
    sequence: 1,
    carryOver: false,
    customerPartyId: "00000000-0000-4000-8000-0000000000c1",
    caseDescription: "Seal leaking",
    address: "1200 Harbor Drive, Duluth, MN",
    latitude: 46.78,
    longitude: -92.1,
    assignedStart: "2026-10-26T14:00:00Z",
    assignedEnd: "2026-10-26T15:30:00Z",
    estDurationMinutes: 90,
    requiredSkills: [],
    lines: [{ id: "00000000-0000-4000-8000-0000000000b1", kind: "PART", description: "Seal kit", productId: "00000000-0000-4000-8000-0000000000d1", unit: "EA", qty: "2", consumedQty: "0" }],
    checklist: [
      { id: "00000000-0000-4000-8000-0000000000e1", text: "Lock out", required: true, done: false },
      { id: "00000000-0000-4000-8000-0000000000e2", text: "Photo of nameplate", required: false, done: false },
    ],
    photos: [],
    signature: { signed: false, exempt: false },
    technicianNote: "",
    ...over,
  };
}

export function day(visits: DayVisit[] = [visit()]): TechnicianDay {
  return {
    technician: { id: "00000000-0000-4000-8000-0000000000f1", code: "AMY", name: "Amy Rivera", timeZone: "America/Chicago", legalEntityId: "00000000-0000-4000-8000-0000000000e0" },
    date: "2026-10-26",
    generatedAt: "2026-10-26T12:00:00Z",
    visits,
    totals: { visits: visits.length, completed: 0, open: visits.length, bookedMinutes: 90 * visits.length },
  };
}
