import { Component, type ErrorInfo, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { copy } from "./copy";
import { recordError } from "./telemetry";
import { Alert, Button, buttonClassName } from "./ui";

interface State {
  failed: boolean;
}

export class RouteErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    recordError(error, info.componentStack);
  }

  render() {
    if (!this.state.failed) {
      return this.props.children;
    }
    return (
      <Alert
        tone="danger"
        live="alert"
        title={copy.errorBoundary.title}
        action={
          <>
            <Button size="sm" onClick={() => this.setState({ failed: false })}>
              {copy.buttons.tryAgain}
            </Button>
            <Link to="/" className={buttonClassName("ghost", "sm")}>
              {copy.buttons.goHome}
            </Link>
          </>
        }
      >
        {copy.errorBoundary.body}
      </Alert>
    );
  }
}
