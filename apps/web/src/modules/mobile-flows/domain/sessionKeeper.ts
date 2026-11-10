// Keeps the device's session with the mobile service alive (FR-32.1, FR-32.6, BR-32.6). Pure: the
// calls and the clock are injected. The device must keep working with the service out of reach
// (FR-32.5), so a failed call never stops the keeper: it reports "offline" and tries again on the
// next beat. Only a refusal the worker has to act on, another user holding the device, is
// "blocked".

export interface KeeperSession {
  sessionId: string;
}

/** Failure of a session call: an HTTP status and, when the body had one, the problem code. */
export interface KeeperError {
  status?: number;
  code?: string;
  message?: string;
}

export interface KeeperClient {
  start(): Promise<{ session: KeeperSession; notice?: { message: string } }>;
  heartbeat(sessionId: string, deviceTime: Date): Promise<{ relocked: boolean; reason?: string }>;
  end(sessionId: string): Promise<void>;
}

export type KeeperStatus =
  /** Not started yet. */
  | "idle"
  /** A session is open and the last call worked. */
  | "active"
  /** The service could not be reached, or is not deployed; the device keeps working from its queue. */
  | "offline"
  /** Another user holds this device (MOB-SESSION-ACTIVE). They must sign out or the session must go idle. */
  | "blocked";

export interface KeeperState {
  status: KeeperStatus;
  sessionId?: string;
  /** A line for the worker: a resumed run, a lost work lock or why the device is blocked. */
  message?: string;
}

const asError = (e: unknown): KeeperError => (typeof e === "object" && e !== null ? (e as KeeperError) : { message: String(e) });

export class SessionKeeper {
  private current: KeeperState = { status: "idle" };

  constructor(
    private readonly client: KeeperClient,
    private readonly onChange: (state: KeeperState) => void = () => undefined,
    private readonly now: () => Date = () => new Date(),
  ) {}

  get state(): KeeperState {
    return this.current;
  }

  /** The id replay sends with each transaction; absent while no session is open. */
  get sessionId(): string | undefined {
    return this.current.sessionId;
  }

  private set(next: KeeperState): KeeperState {
    this.current = next;
    this.onChange(next);
    return next;
  }

  /** Opens the session, or picks the user's own up again. */
  async start(): Promise<KeeperState> {
    try {
      const out = await this.client.start();
      return this.set({ status: "active", sessionId: out.session.sessionId, ...(out.notice ? { message: out.notice.message } : {}) });
    } catch (e) {
      const err = asError(e);
      if (err.status === 409 && err.code === "MOB-SESSION-ACTIVE") {
        return this.set({ status: "blocked", message: "Another worker is signed in on this device. Ask them to sign out." });
      }
      return this.set({ status: "offline" });
    }
  }

  /**
   * One beat: heartbeat the open session, or start one. A session the server ended (a takeover or
   * a supervisor's sign-out) is started again. A transport failure keeps the session id for the
   * next beat.
   */
  async beat(): Promise<KeeperState> {
    const id = this.current.sessionId;
    if (!id) return this.start();
    try {
      const out = await this.client.heartbeat(id, this.now());
      return this.set({ status: "active", sessionId: id, ...(out.reason ? { message: out.reason } : {}) });
    } catch (e) {
      const err = asError(e);
      if (err.status === 409 && err.code === "MOB-SESSION-ENDED") {
        this.current = { status: "idle" };
        return this.start();
      }
      return this.set({ status: "offline", sessionId: id });
    }
  }

  /** Signs out. A failure is ignored: the idle timeout ends the session anyway. */
  async stop(): Promise<void> {
    const id = this.current.sessionId;
    this.set({ status: "idle" });
    if (!id) return;
    try {
      await this.client.end(id);
    } catch {
      // The session ends on its own after the idle timeout.
    }
  }
}
