import { Link } from "react-router-dom";
import { copy } from "../copy";
import { Card, EmptyState, buttonClassName } from "../ui";

export function NotAvailablePage({ featureOff = false }: { featureOff?: boolean }) {
  return (
    <Card>
      <EmptyState
        icon="lock"
        headingLevel={2}
        title={featureOff ? copy.notAvailable.featureOffTitle : copy.notAvailable.title}
        description={featureOff ? copy.notAvailable.featureOffDescription : copy.notAvailable.description}
        action={
          <Link to="/" className={buttonClassName("secondary")}>
            {copy.buttons.goHome}
          </Link>
        }
      />
    </Card>
  );
}
