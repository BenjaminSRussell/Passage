const isMac = typeof navigator !== "undefined" && /Mac|iP/.test(navigator.platform);

export const shortcutHint = isMac ? "⌘K" : "Ctrl K";

export const copy = {
  nav: { home: "Home", notes: "Notes", general: "General" },
  buttons: {
    search: "Search",
    closeSearch: "Close search",
    openNavigation: "Open navigation",
    closeNavigation: "Close navigation",
    applySavedView: "Apply saved view",
    compactView: "Compact view",
    tryAgain: "Try again",
    browseWorkspaces: "Browse workspaces",
    goHome: "Go to home",
  },
  announce: {
    searchOpened: "Search opened",
    savedViewApplied: "Saved view applied",
    offline: "You're offline",
    online: "You're back online",
    noResults: "No results",
    oneResult: "1 result",
    results: (count: number) => `${count} results`,
    opened: (label: string) => `Opened ${label}`,
  },
  offlineBanner: "You're offline. Pages you've opened still work, but searches and saves will fail until you reconnect.",
  palette: {
    title: "Search",
    inputLabel: "Find a page",
    placeholder: "Search pages and workspaces",
    help: "Use the up and down arrows to move through results, then press Enter to open a page.",
    noResults: (query: string) => `No pages match “${query}”.`,
    noResultsHint: "Try a workspace name, like Admin or Tax.",
    listLabel: "Pages",
  },
  home: {
    pageTitle: "Workspaces",
    description: `Open a workspace, or press ${shortcutHint} to jump to any page.`,
    emptyTitle: "No workspaces yet",
    emptyDescription: "Workspaces appear here once they're installed.",
  },
  notes: {
    title: "Notes",
    description: "A preview of a form generated from entity metadata.",
    saved: (title: string) => `${title} saved`,
  },
  notFound: {
    title: "We couldn't find that page",
    description: "Check the address, or search for the page you need.",
    pageTitle: "Page not found",
  },
  notAvailable: {
    title: "This page isn't available to you",
    description: "Your account doesn't have the duty this page needs. Ask an administrator if you should have access.",
    pageTitle: "Page not available",
  },
  errorBoundary: {
    title: "This page couldn't load",
    body: "Something went wrong while showing this page. Your other workspaces still work.",
  },
  placeholder: {
    title: "This workspace isn't available yet",
    badge: "Coming soon",
  },
} as const;
