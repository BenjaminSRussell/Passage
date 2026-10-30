import { Link } from "react-router-dom";
import { copy } from "../copy";
import { Card, EmptyState, buttonClassName } from "../ui";

export function NotAvailablePage() {
  return (
    <Card>
      <EmptyState
        icon="lock"
        headingLevel={2}
        title={copy.notAvailable.title}
        description={copy.notAvailable.description}
        action={
          <Link to="/" className={buttonClassName("secondary")}>
            {copy.buttons.goHome}
          </Link>
        }
      />
    </Card>
  );
}
