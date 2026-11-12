import type { KillSwitchLog } from "../api/killswitch.js";
import type { KillSwitchRegistry } from "../domain/killswitch.js";

/** Poll interval: well inside the 10 s propagation window (AC-47.6), so a missed poll or two does not fail closed. */
export const DEFAULT_KILL_POLL_MS = 1000;

/**
 * Keeps one pod's registry in step with the shared kill switch (`kill_switch_change`, AC-47.6). Each
 * poll applies the toggles after the registry's version in order, then heartbeats. A poll that fails
 * does not heartbeat, so a pod that cannot read the table for 10 s fails closed and halts every run.
 */
export class KillSwitchPoller {
  private timer: ReturnType<typeof setTimeout> | undefined;
  private stopped = false;

  constructor(
    private readonly log: Pick<KillSwitchLog, "changesSince">,
    private readonly registry: KillSwitchRegistry,
    private readonly onError: (err: unknown) => void = () => undefined,
  ) {}

  /** One poll. Returns the number of toggles applied; throws when the log cannot be read. */
  async poll(): Promise<number> {
    const changes = await this.log.changesSince(this.registry.version);
    let applied = 0;
    for (const change of changes) if (this.registry.apply(change)) applied += 1;
    this.registry.heartbeat();
    return applied;
  }

  /** Polls now, then every `intervalMs` until `stop`. Resolves after the first poll (or its failure). */
  async start(intervalMs: number = DEFAULT_KILL_POLL_MS): Promise<void> {
    const tick = async (): Promise<void> => {
      try {
        await this.poll();
      } catch (err) {
        this.onError(err);
      }
    };
    const loop = (): void => {
      if (this.stopped) return;
      this.timer = setTimeout(() => {
        void tick().finally(loop);
      }, intervalMs);
      this.timer.unref?.();
    };
    await tick();
    loop();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
  }
}
