import type { ReactNode } from "react";
import type { Auth } from "./auth";
import { AuthGate } from "./AuthGate";

export function AuthRoot({ auth, requireAuth, children }: { auth: Auth | null; requireAuth: boolean; children: (duties?: readonly string[]) => ReactNode }) {
  if (auth !== null) return <AuthGate auth={auth}>{(session) => children(session.duties)}</AuthGate>;
  if (!requireAuth) return <>{children()}</>;
  return (
    <main>
      <h1>Sign-in is not configured</h1>
      <p role="alert">This build has no identity provider settings, so the application is not available. Ask an administrator to configure sign-in.</p>
    </main>
  );
}
