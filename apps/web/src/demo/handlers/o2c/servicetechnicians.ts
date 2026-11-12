// Field technicians (/api/v1/service/technicians) for the demo: the people Cedar Ridge Pumps' dispatch board schedules, with
// their skills, territories, shift and daily maximum. Jordan Avery is the technician of the PWA demo (technician.ts), so the
// two screens agree. Reads honour the status and territory filters the screen sends; create and replace work on the table.
import type { Technician, TechnicianInput } from "../../../modules/service/api/client";
import { LEGAL_ENTITY, PLANT, WAREHOUSES } from "../../company";
import { reply, route, type DemoRequest } from "../../backend";
import { table, uid } from "../../store";

const BASE = "/api/v1/service";

const weekdays = [1, 2, 3, 4, 5];
function technician(n: number, code: string, name: string, extra: Partial<Technician>): Technician {
  return {
    id: uid(450, n),
    code,
    name,
    version: 1,
    workerId: null,
    legalEntityId: LEGAL_ENTITY.id,
    territoryIds: ["MIDWEST1"],
    skills: ["PUMP-REPAIR"],
    timeZone: "America/Chicago",
    shiftDays: weekdays,
    shiftStartMinute: 7 * 60,
    shiftEndMinute: 16 * 60,
    homeLatitude: null,
    homeLongitude: null,
    truckLocationId: null,
    truckSiteId: null,
    truckWarehouseId: null,
    maxDailyMinutes: 480,
    overtimeAllowed: false,
    active: true,
    ...extra,
  };
}

const technicians = table<Technician>([
  technician(1, "JAVERY", "Jordan Avery", { skills: ["PUMP-REPAIR", "SEAL-REPLACE", "VIBRATION"], homeLatitude: 46.7867, homeLongitude: -92.1005, truckSiteId: PLANT.id, truckWarehouseId: WAREHOUSES[0]!.id, overtimeAllowed: true, maxDailyMinutes: 540 }),
  technician(2, "TREYES", "Tomas Reyes", { territoryIds: ["GULFCST1"], skills: ["PUMP-REPAIR", "CONTROLS", "SKID-COMMISSIONING"], timeZone: "America/Chicago", shiftStartMinute: 6 * 60 + 30, shiftEndMinute: 15 * 60 + 30 }),
  technician(3, "ARAHMAN", "Aisha Rahman", { territoryIds: ["MIDWEST1", "GULFCST1"], skills: ["PUMP-REPAIR", "CONTROLS", "ALIGNMENT"], maxDailyMinutes: 450 }),
  technician(4, "GHOLLOWAY", "Greg Holloway", { territoryIds: ["MIDWEST1"], skills: ["SEAL-REPLACE", "MOTOR-REWIND"], shiftDays: [1, 2, 3, 4], shiftStartMinute: 8 * 60, shiftEndMinute: 18 * 60 }),
  technician(5, "IVARGA", "Ines Varga", { territoryIds: ["GULFCST1"], skills: ["PUMP-REPAIR"], active: false }),
]);

route("GET", `${BASE}/technicians`, ({ query }) => {
  const active = query.get("active");
  const territory = query.get("territoryId");
  return {
    items: technicians
      .all()
      .filter((t) => (active === "" || active === null || String(t.active) === active) && (!territory || t.territoryIds.includes(territory)))
      .sort((a, b) => a.name.localeCompare(b.name)),
  };
});

route("GET", `${BASE}/technicians/{id}`, ({ params }) => {
  const t = technicians.get(params.id);
  return t ? reply.versioned(t, t.version) : reply.problem(404, "SVC-0404", "Technician not found");
});

route("POST", `${BASE}/technicians`, ({ body }: DemoRequest) => {
  const input = (body ?? {}) as TechnicianInput;
  const code = (input.code ?? "").trim().toUpperCase();
  if (technicians.all().some((t) => t.code === code)) return reply.problem(409, "SVC-DSP-001", "A technician with this code exists", { code: "A technician with this code already exists." });
  const id = uid(450, technicians.all().length + 1);
  const created = technicians.insert({ ...(input as Omit<Technician, "id" | "version" | "code">), id, code, version: 1 });
  return reply.versioned(created, 1, 201);
});

route("PUT", `${BASE}/technicians/{id}`, ({ params, body }: DemoRequest) => {
  const current = technicians.get(params.id);
  if (!current) return reply.problem(404, "SVC-0404", "Technician not found");
  const next = technicians.update(params.id, { ...(body as Omit<TechnicianInput, "code">), version: current.version + 1 });
  return reply.versioned(next, current.version + 1);
});
