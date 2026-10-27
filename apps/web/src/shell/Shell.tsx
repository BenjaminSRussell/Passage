import { Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";
import type { PassageModule } from "../modules/registry";
import { CommandPalette, type PaletteEntry } from "./CommandPalette";
import { copy } from "./copy";
import { useOnlineStatus } from "./hooks/useOnlineStatus";
import { demoNote } from "./metadata";
import { resolvePage } from "./pageMeta";
import { applyView, saveView, type Density, type Personalization, type SavedView } from "./personalization";
import { RouteErrorBoundary } from "./RouteErrorBoundary";
import styles from "./Shell.module.css";
import { shellNav } from "./shellNav";
import { Sidebar } from "./Sidebar";
import { clickTrace } from "./telemetry";
import { TopBar } from "./TopBar";
import { Alert, Skeleton } from "./ui";
import { workspaceIcon } from "./workspaceIcons";

const TOAST_MS = 4000;
const DRAWER_BREAKPOINT = "(min-width: 960px)";

function isShown(el: HTMLElement): boolean {
  return el.offsetParent !== null && window.getComputedStyle(el).visibility !== "hidden";
}

export interface ShellProps {
  modules: PassageModule[];
}

function PageSkeleton() {
  return (
    <div className={styles.skeleton} aria-busy="true">
      <span className="visually-hidden">Loading…</span>
      <Skeleton width="1/4" />
      <Skeleton width="1/2" />
      <Skeleton height="tile" />
    </div>
  );
}

export function Shell({ modules }: ShellProps) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const online = useOnlineStatus();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [toast, setToast] = useState("");
  const [density, setDensity] = useState<Density>("comfortable");
  const [personalization, setPersonalization] = useState<Personalization>({ views: [] });
  const searchRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLButtonElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const paletteOpenRef = useRef(false);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const lastPath = useRef(pathname);
  const wasOnline = useRef(online);
  const announced = useRef("");
  const announceFrame = useRef<number | undefined>(undefined);
  const toastTimer = useRef<number | undefined>(undefined);

  const page = useMemo(() => resolvePage(pathname, modules, shellNav), [pathname, modules]);

  const entries = useMemo<PaletteEntry[]>(
    () => [
      ...shellNav.map((n) => ({ id: `shell:${n.to}`, group: copy.nav.general, label: n.label, to: n.to, icon: n.icon })),
      ...modules.flatMap((m) =>
        m.nav.map((n) => ({ id: `${m.id}:${n.to}`, group: m.title, label: n.label, to: n.to, icon: workspaceIcon(m.id) })),
      ),
    ],
    [modules],
  );

  const announce = useCallback((message: string, visual = false) => {
    if (announceFrame.current !== undefined) {
      window.cancelAnimationFrame(announceFrame.current);
      announceFrame.current = undefined;
    }
    const write = () => {
      announced.current = message;
      setAnnouncement(message);
    };
    if (message === announced.current && typeof window.requestAnimationFrame === "function") {
      announced.current = "";
      setAnnouncement("");
      announceFrame.current = window.requestAnimationFrame(write);
    } else {
      write();
    }
    if (visual) {
      setToast(message);
      window.clearTimeout(toastTimer.current);
      toastTimer.current = window.setTimeout(() => setToast(""), TOAST_MS);
    }
  }, []);

  const announceQuietly = useCallback((message: string) => announce(message), [announce]);

  useEffect(
    () => () => {
      if (announceFrame.current !== undefined) {
        window.cancelAnimationFrame(announceFrame.current);
      }
      window.clearTimeout(toastTimer.current);
    },
    [],
  );

  const openPalette = useCallback(() => {
    const focused = document.activeElement;
    returnFocusRef.current = focused instanceof HTMLElement && focused !== document.body ? focused : null;
    paletteOpenRef.current = true;
    setPaletteOpen(true);
  }, []);

  const closePalette = useCallback((restoreFocus = true) => {
    paletteOpenRef.current = false;
    setPaletteOpen(false);
    if (restoreFocus) {
      const stored = returnFocusRef.current;
      const target = stored?.isConnected
        ? stored
        : [searchRef.current, menuRef.current].find((el): el is HTMLButtonElement => el !== null && isShown(el)) ?? titleRef.current;
      target?.focus();
    }
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (paletteOpenRef.current) {
          (document.getElementById("global-search") as HTMLInputElement | null)?.select();
        } else {
          openPalette();
          announce(copy.announce.searchOpened);
        }
      } else if (e.key === "Escape" && paletteOpenRef.current) {
        closePalette();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [openPalette, closePalette, announce]);

  useEffect(() => {
    document.title = `${page.title} · Passage`;
  }, [page.title]);

  useEffect(() => {
    if (lastPath.current === pathname) {
      return;
    }
    lastPath.current = pathname;
    setDrawerOpen(false);
    titleRef.current?.focus({ preventScroll: true });
    document.documentElement.scrollTop = 0;
  }, [pathname]);

  useEffect(() => {
    if (!(drawerOpen || paletteOpen)) {
      return;
    }
    document.body.setAttribute("data-scroll-lock", "");
    return () => document.body.removeAttribute("data-scroll-lock");
  }, [drawerOpen, paletteOpen]);

  useEffect(() => {
    if (wasOnline.current === online) {
      return;
    }
    wasOnline.current = online;
    announce(online ? copy.announce.online : copy.announce.offline, true);
  }, [online, announce]);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") {
      return;
    }
    const wide = window.matchMedia(DRAWER_BREAKPOINT);
    const closeWhenWide = (event: MediaQueryListEvent) => {
      if (event.matches) {
        setDrawerOpen(false);
      }
    };
    wide.addEventListener("change", closeWhenWide);
    return () => wide.removeEventListener("change", closeWhenWide);
  }, []);

  function openSearch() {
    clickTrace("search");
    openPalette();
    announce(copy.announce.searchOpened);
  }

  function selectEntry(entry: PaletteEntry) {
    closePalette(false);
    announce(copy.announce.opened(entry.label));
    if (entry.to === pathname) {
      titleRef.current?.focus({ preventScroll: true });
    } else {
      navigate(entry.to);
    }
  }

  function closeDrawer() {
    if (!drawerOpen) {
      return;
    }
    setDrawerOpen(false);
    menuRef.current?.focus();
  }

  function applySavedView() {
    const view: SavedView = {
      id: "default",
      name: "Default",
      entity: demoNote.id,
      columns: demoNote.fields.map((f) => f.name),
      density,
    };
    setPersonalization(applyView(saveView(personalization, view), view.id));
    announce(copy.announce.savedViewApplied, true);
  }

  return (
    <div className={styles.root} data-density={density}>
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <div className={styles.frame}>
        <Sidebar
          modules={modules}
          shellNav={shellNav}
          drawerOpen={drawerOpen}
          searchRef={searchRef}
          onOpenSearch={openSearch}
          onCloseDrawer={closeDrawer}
        />
        {drawerOpen ? <div className={styles.scrim} onClick={closeDrawer} aria-hidden="true" /> : null}
        <main id="main" tabIndex={-1} className={styles.main}>
          <TopBar
            page={page}
            titleRef={titleRef}
            menuRef={menuRef}
            drawerOpen={drawerOpen}
            compact={density === "compact"}
            onOpenDrawer={() => setDrawerOpen(true)}
            onApplyView={applySavedView}
            onToggleCompact={() => setDensity((d) => (d === "compact" ? "comfortable" : "compact"))}
          />
          {online ? null : (
            <div className={styles.banner}>
              <Alert tone="warning">{copy.offlineBanner}</Alert>
            </div>
          )}
          <div className={styles.content}>
            <RouteErrorBoundary key={pathname}>
              <Suspense fallback={<PageSkeleton />}>
                <Outlet />
              </Suspense>
            </RouteErrorBoundary>
          </div>
        </main>
      </div>
      <div role="status" aria-live="polite" className="visually-hidden">
        {announcement}
      </div>
      {toast ? (
        <div className={styles.toast} aria-hidden="true">
          {toast}
        </div>
      ) : null}
      {paletteOpen ? <CommandPalette entries={entries} onSelect={selectEntry} onClose={() => closePalette()} announce={announceQuietly} /> : null}
    </div>
  );
}
