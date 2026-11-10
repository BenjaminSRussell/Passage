import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { recordError } from "../telemetry";
import {
  fetchFeatures,
  fetchSetup,
  fetchWorkspaceOverride,
  FEATURE_VIEW_DUTY,
  SETUP_MAINTAIN_DUTY,
  SETUP_VIEW_DUTY,
  type TenantFeatures,
  type TenantSetup,
} from "./api";
import { featureStateOf, isFeatureOn, type FeatureState } from "./featureRules";

export interface TenantSettings {
  /** On/off by feature key; undefined until it is known, or when the person may not read it. */
  features: FeatureState;
  catalog: TenantFeatures | undefined;
  /** Onboarding progress, read only for a person who can maintain it. */
  setup: TenantSetup | undefined;
  /** The tenant's workspace override as stored; undefined when there is none. */
  workspaceOverride: unknown;
  /** Reads everything again, after a change on the features or setup pages. */
  refresh(): void;
}

const unknown: TenantSettings = { features: undefined, catalog: undefined, setup: undefined, workspaceOverride: undefined, refresh: () => undefined };

const TenantSettingsContext = createContext<TenantSettings>(unknown);

export function useTenantSettings(): TenantSettings {
  return useContext(TenantSettingsContext);
}

/** True unless the tenant switched the feature off. Everything is on while the answer is not known. */
export function useFeature(key: string): boolean {
  return isFeatureOn(useTenantSettings().features, key);
}

export interface FeatureGateProps {
  feature: string;
  /** Shown instead of the children when the feature is off. */
  fallback?: ReactNode;
  children: ReactNode;
}

/** Hides a field or a block of a form that belongs to a feature the tenant does not use. Not a security boundary. */
export function FeatureGate({ feature, fallback = null, children }: FeatureGateProps) {
  return useFeature(feature) ? <>{children}</> : <>{fallback}</>;
}

export interface TenantSettingsLoad {
  /** Fixed answers for tests and for a shell hosted without the identity service. They replace the first read. */
  features?: Readonly<Record<string, boolean>>;
}

function failOpen(error: unknown): undefined {
  recordError(error instanceof Error ? error : new Error(String(error)));
  return undefined;
}

/** Reads the tenant's features, onboarding progress and workspace override, each only if the duties allow it. */
export function useTenantSettingsLoader(duties: readonly string[], fixed?: TenantSettingsLoad["features"]): TenantSettings {
  const [catalog, setCatalog] = useState<TenantFeatures>();
  const [setup, setSetup] = useState<TenantSetup>();
  const [override, setOverride] = useState<unknown>();
  const [attempt, setAttempt] = useState(0);
  const canFeatures = fixed === undefined && duties.includes(FEATURE_VIEW_DUTY);
  const canWorkspaces = duties.includes(SETUP_VIEW_DUTY);
  const canSetup = duties.includes(SETUP_MAINTAIN_DUTY);

  useEffect(() => {
    const controller = new AbortController();
    const { signal } = controller;
    if (canFeatures) void fetchFeatures(signal).then(setCatalog, (e: unknown) => (signal.aborted ? undefined : failOpen(e)));
    if (canWorkspaces) void fetchWorkspaceOverride(signal).then(setOverride, (e: unknown) => (signal.aborted ? undefined : failOpen(e)));
    if (canSetup) void fetchSetup(signal).then(setSetup, (e: unknown) => (signal.aborted ? undefined : failOpen(e)));
    return () => controller.abort();
  }, [canFeatures, canWorkspaces, canSetup, attempt]);

  const refresh = useCallback(() => setAttempt((n) => n + 1), []);
  const features = useMemo<FeatureState>(() => fixed ?? (catalog === undefined ? undefined : featureStateOf(catalog.features)), [fixed, catalog]);
  return useMemo(() => ({ features, catalog, setup, workspaceOverride: override, refresh }), [features, catalog, setup, override, refresh]);
}

export function TenantSettingsProvider({ value, children }: { value: TenantSettings; children: ReactNode }) {
  return <TenantSettingsContext.Provider value={value}>{children}</TenantSettingsContext.Provider>;
}
