import { Link } from "react-router-dom";
import { Card } from "../../shell/ui";
import { OrderNumber } from "../mfg-shared/orders";
import { ResourceName } from "../mfg-shared/resources";
import { listControlRecipes, type ControlRecipe } from "./api/client";
import { RecipeStatusBadge } from "./ControlRecipesPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const ACTIVE = new Set(["sent", "received", "in-execution"]);

/** The PI sheets waiting at this kiosk: the kiosk recipes of the worker's resource that are not completed (FR-25.16). */
export function KioskPiList({ resourceId }: { resourceId: string | null }) {
  const state = useLoad((signal) => listControlRecipes({ destination: "kiosk" }, signal), "kiosk-pi");
  const rows: ControlRecipe[] = (state.data ?? []).filter((r) => ACTIVE.has(r.status) && (resourceId === null || r.resourceId === resourceId));
  if (state.error && !state.data) {
    // A quiet note, not an alert: the punches above are what the kiosk is for, and this list is retried on the next sign-in.
    return (
      <p className={styles.hint}>
        The PI sheets could not be loaded. <button type="button" onClick={state.reload}>Reload PI sheets</button>
      </p>
    );
  }
  if (rows.length === 0) return null;
  return (
    <Card title="PI sheets to work">
      <ul className={styles.plainList} aria-label="PI sheets to work">
        {rows.map((r) => (
          <li key={r.id}>
            <Link to={`/shopfloor/kiosk/pi/${r.id}`}>
              <OrderNumber id={r.orderId} />
              {" on "}
              <ResourceName id={r.resourceId} />
            </Link>{" "}
            <RecipeStatusBadge status={r.status} />
          </li>
        ))}
      </ul>
    </Card>
  );
}
