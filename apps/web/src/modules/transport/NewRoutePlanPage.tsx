import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { describeProblem, optimizeRoutes, type Problem, type RouteRequest } from "./api/client";
import styles from "./pages.module.css";
import { RowsEditor, type RowField } from "./RowsEditor";
import { ProblemMessage } from "../../shell/errors";

export interface RouteValues {
  depotX: string;
  depotY: string;
  timeLimitS: string;
  speedKmh: string;
  latenessPerMin: string;
  stops: Record<string, string>[];
  vehicles: Record<string, string>[];
}

const stopFields: RowField[] = [
  { key: "id", label: "Stop reference", hint: "Your own name for the stop, such as a customer or dock (up to 64 characters)." },
  { key: "x", label: "X km", decimal: true, hint: "Position east of the origin." },
  { key: "y", label: "Y km", decimal: true, hint: "Position north of the origin." },
  { key: "earliest", label: "Earliest min", hint: "Window start, minutes after route start." },
  { key: "latest", label: "Latest min" },
  { key: "serviceMin", label: "Service min" },
  { key: "weightKg", label: "Weight kg" },
  { key: "volumeL", label: "Volume L" },
  { key: "pallets", label: "Pallets" },
];

const vehicleFields: RowField[] = [
  { key: "id", label: "Vehicle reference", hint: "Your own name for the vehicle, such as its plate or fleet number." },
  { key: "capacityKg", label: "Capacity kg" },
  { key: "capacityL", label: "Capacity L" },
  { key: "pallets", label: "Pallets" },
  { key: "fixedCost", label: "Fixed cost", hint: "Cost of using the vehicle, whole money units." },
  { key: "maxRouteMin", label: "Max route min", hint: "Default 660 (11 h driving)." },
];

const blankStop = () => ({ id: "", x: "", y: "", earliest: "0", latest: "480", serviceMin: "10", weightKg: "0", volumeL: "0", pallets: "0" });
const blankVehicle = () => ({ id: "", capacityKg: "", capacityL: "0", pallets: "0", fixedCost: "0", maxRouteMin: "660" });

const WEEK_MIN = 7 * 24 * 60;
const whole = /^\d{1,7}$/;
const coordinate = /^-?\d{1,5}(\.\d{1,6})?$/;

/** Checks that mirror RouteRequest.Validate in the service, and the request the form sends. */
export function validateRoute(v: RouteValues): { errors: Record<string, string>; input?: RouteRequest } {
  const errors: Record<string, string> = {};
  const num = (s: string) => Number(s.trim());
  if (!coordinate.test(v.depotX.trim())) errors.depotX = "Enter the depot X position in km, such as 0.";
  if (!coordinate.test(v.depotY.trim())) errors.depotY = "Enter the depot Y position in km, such as 0.";
  if (!whole.test(v.timeLimitS.trim()) || num(v.timeLimitS) < 5 || num(v.timeLimitS) > 25) errors.timeLimitS = "Enter 5 to 25 seconds.";
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(v.speedKmh.trim()) || num(v.speedKmh) <= 0 || num(v.speedKmh) > 200) errors.speedKmh = "Enter a speed above 0 and at most 200 km/h.";
  if (!whole.test(v.latenessPerMin.trim()) || num(v.latenessPerMin) > 10000) errors.latenessPerMin = "Enter 0 to 10,000 per minute late.";

  const seenStops = new Set<string>();
  const stops = v.stops.map((s, i) => {
    const row = `stops[${i}]`;
    const id = s.id.trim();
    if (id === "" || id.length > 64) errors[row] = "Enter a stop id of 1 to 64 characters.";
    else if (seenStops.has(id)) errors[row] = "Give each stop its own id.";
    else if (!coordinate.test(s.x.trim()) || !coordinate.test(s.y.trim())) errors[row] = "Enter the position as X and Y in km.";
    else if (!whole.test(s.earliest.trim()) || !whole.test(s.latest.trim()) || num(s.latest) < num(s.earliest) || num(s.latest) > WEEK_MIN)
      errors[row] = "Enter a window with earliest at or before latest, within a week.";
    else if (![s.serviceMin, s.weightKg, s.volumeL, s.pallets].every((q) => whole.test(q.trim()))) errors[row] = "Enter service time and quantities as whole numbers.";
    seenStops.add(id);
    return {
      id,
      point: { x: num(s.x), y: num(s.y) },
      earliest: num(s.earliest),
      latest: num(s.latest),
      serviceMin: num(s.serviceMin),
      weightKg: num(s.weightKg),
      volumeL: num(s.volumeL),
      pallets: num(s.pallets),
    };
  });
  if (stops.length === 0 || stops.length > 500) errors.stops = "Add between 1 and 500 stops.";

  const seenVehicles = new Set<string>();
  const vehicles = v.vehicles.map((x, i) => {
    const row = `vehicles[${i}]`;
    const id = x.id.trim();
    if (id === "" || id.length > 64) errors[row] = "Enter a vehicle id of 1 to 64 characters.";
    else if (seenVehicles.has(id)) errors[row] = "Give each vehicle its own id.";
    else if (![x.capacityKg, x.capacityL, x.pallets, x.fixedCost].every((q) => whole.test(q.trim()))) errors[row] = "Enter capacities and fixed cost as whole numbers.";
    else if (!whole.test(x.maxRouteMin.trim()) || num(x.maxRouteMin) < 1 || num(x.maxRouteMin) > WEEK_MIN) errors[row] = "Enter the maximum route time in minutes, at least 1.";
    seenVehicles.add(id);
    return { id, capacityKg: num(x.capacityKg), capacityL: num(x.capacityL), pallets: num(x.pallets), fixedCost: num(x.fixedCost), maxRouteMin: num(x.maxRouteMin) };
  });
  if (vehicles.length === 0 || vehicles.length > 50) errors.vehicles = "Add between 1 and 50 vehicles.";

  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      depot: { x: num(v.depotX), y: num(v.depotY) },
      stops,
      vehicles,
      speedKmh: num(v.speedKmh),
      latenessPerMin: num(v.latenessPerMin),
      timeLimitS: num(v.timeLimitS),
    },
  };
}

/** Plan multi-stop routes with the VRP optimizer (FR-34.2, BR-34.1). The same request returns the plan already solved. */
export function NewRoutePlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RouteValues>({
    depotX: "0",
    depotY: "0",
    timeLimitS: "20",
    speedKmh: "60",
    latenessPerMin: "50",
    stops: [blankStop()],
    vehicles: [blankVehicle()],
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const update = (patch: Partial<RouteValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRoute(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const plan = await optimizeRoutes(input);
      navigate(`/transport/routes/${plan.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      setBusy(false);
    }
  }

  const field = (key: "depotX" | "depotY" | "timeLimitS" | "speedKmh" | "latenessPerMin", id: string, label: string, hint?: string) => {
    const serviceKey = { depotX: "depot", depotY: "depot", timeLimitS: "timeLimitS", speedKmh: "speedKmh", latenessPerMin: "latenessPerMin" }[key];
    const error = errors[key] ?? errors[serviceKey];
    return (
      <FormField id={id} label={label} hint={hint} error={error}>
        {(control) => <Input {...control} inputMode="decimal" value={values[key]} onChange={(e) => update({ [key]: e.target.value })} invalid={Boolean(error)} />}
      </FormField>
    );
  };

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Optimize routes"
        description="Positions are kilometres on a local map grid and times are minutes from the route start. Lateness is a soft penalty up to 60 minutes past a window; beyond that, or over capacity, a stop is dropped and reported (TMS-0104)."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("depotX", "route-depot-x", "Depot X (km)")}
          {field("depotY", "route-depot-y", "Depot Y (km)")}
          {field("timeLimitS", "route-time-limit", "Solver time (s)", "5 to 25 seconds.")}
          {field("speedKmh", "route-speed", "Average speed (km/h)")}
          {field("latenessPerMin", "route-lateness", "Lateness penalty per minute", "0 turns the penalty off.")}
          <RowsEditor field="stops" legend="Stops" noun="stop" fields={stopFields} rows={values.stops} errors={errors} blank={blankStop} onChange={(stops) => update({ stops })} />
          <RowsEditor field="vehicles" legend="Vehicles" noun="vehicle" fields={vehicleFields} rows={values.vehicles} errors={errors} blank={blankVehicle} onChange={(vehicles) => update({ vehicles })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "TMS-0123" ? "The route optimizer is unavailable" : "Could not optimize the routes"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Optimize routes
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/routes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
