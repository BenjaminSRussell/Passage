import { ProblemMessage } from "../../shell/errors";
import { Alert, Button } from "../../shell/ui";
import { ProductApiError } from "./api/client";

const UNREACHABLE = "We couldn't reach the product service. Check your connection and try again.";

export function ProductProblem({ error, title, retry }: { error: unknown; title: string; retry?: () => void }) {
  return (
    <Alert
      tone="danger"
      title={title}
      live="alert"
      action={
        retry ? (
          <Button size="sm" onClick={retry}>
            Try again
          </Button>
        ) : undefined
      }
    >
      <ProblemMessage problem={error} message={error instanceof ProductApiError ? undefined : UNREACHABLE} />
    </Alert>
  );
}
