import { Link } from "react-router-dom";
import { copy } from "../copy";
import { Card, EmptyState, buttonClassName } from "../ui";

export function NotFoundPage() {
  return (
    <Card>
      <EmptyState
        icon="search"
        headingLevel={2}
        title={copy.notFound.title}
        description={copy.notFound.description}
        action={
          <Link to="/" className={buttonClassName("secondary")}>
            {copy.buttons.goHome}
          </Link>
        }
      />
    </Card>
  );
}
