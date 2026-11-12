import { UUID } from "../../shell/api/http";
import { EntityName } from "../../shell/lookup/EntityName";
import type { RepShare } from "./api/client";

/**
 * A sales rep by name. Pricing keeps only the rep's worker id; the name is looked up in HR. A rep key that is not a
 * worker id (loaded from an older system) is shown as it is.
 */
export function RepName({ id }: { id: string }) {
  return UUID.test(id) ? <EntityName entity="worker" id={id} fallback="Unknown rep" /> : <>{id}</>;
}

/** "Ana Ortiz 60% · Ben Cole 40%" */
export function RepShares({ reps }: { reps: RepShare[] }) {
  return (
    <>
      {reps.map((r, i) => (
        <span key={r.repId}>
          {i > 0 ? " · " : ""}
          <RepName id={r.repId} /> {`${r.sharePct}%`}
        </span>
      ))}
    </>
  );
}
