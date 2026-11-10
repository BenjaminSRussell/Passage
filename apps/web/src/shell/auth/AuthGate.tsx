import { useEffect, useRef, useState, type ReactNode } from "react";
import type { Auth } from "./auth";
import type { Session } from "./oidc";

type State = { status: "loading" } | { status: "ready"; session: Session } | { status: "failed"; message: string };

export function AuthGate({ auth, children }: { auth: Auth; children: (session: Session) => ReactNode }) {
  const [state, setState] = useState<State>({ status: "loading" });
  const loginStarted = useRef(false);

  useEffect(() => {
    let live = true;
    auth
      .ensureSession()
      .then(async (session) => {
        if (session === null) {
          if (loginStarted.current) return;
          loginStarted.current = true;
          await auth.login();
        } else if (live) {
          setState({ status: "ready", session });
        }
      })
      .catch((error: unknown) => {
        if (live) setState({ status: "failed", message: error instanceof Error ? error.message : "sign-in failed" });
      });
    return () => {
      live = false;
    };
  }, [auth]);

  if (state.status === "ready") return <>{children(state.session)}</>;
  if (state.status === "failed") {
    return (
      <main>
        <h1>Sign-in failed</h1>
        <p role="alert">{state.message}</p>
        <button
          type="button"
          onClick={() => {
            setState({ status: "loading" });
            void auth.login();
          }}
        >
          Try again
        </button>
      </main>
    );
  }
  return (
    <main>
      <p role="status">Signing in</p>
    </main>
  );
}
