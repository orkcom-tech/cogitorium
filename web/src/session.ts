// Session holds what the client needs to reach a server: which one, and as
// whom. The same bundle runs in three shells — served by the server itself
// (web), wrapped in a desktop app, and later a TUI — so the server address
// is data, not a build-time constant.
//
// Served from the server, the origin IS the server and no address is needed.
// A desktop shell has no origin of its own, so it stores one, along with the
// servers seen before.

const TOKEN_KEY = "cogitorium.token";
const SERVER_KEY = "cogitorium.server";
const KNOWN_KEY = "cogitorium.servers";

// WHERE THE CREDENTIAL LIVES, and why this file mostly does not know.
//
// Served by the server itself — which is every ordinary install, web shell and
// desktop alike — the token is not kept here at all. It is in an HttpOnly
// cookie the server sets at sign-in, which this code cannot read by design:
// script on the page cannot copy it, and the browser attaches it on its own.
// Whether it survives the browser closing is the server's decision too, taken
// from whether the install is local. See internal/server/session.go.
//
// The fallback below is for the one shape that cannot use a cookie: a client
// pointed at a server on ANOTHER origin, where the browser would not send one.
// There the token is stored and sent as a bearer header, and `local` decides
// whether it outlives the tab — the same rule the cookie follows, kept in step
// by hand because two mechanisms cannot share one implementation.
let remembered: boolean | null = null;
function store(): Storage {
  return remembered === false ? sessionStorage : localStorage;
}

// Served over http(s) from a real host, the page's own origin is the server.
// A desktop shell loads from file:// or a bundled origin and must be told.
function defaultServer(): string {
  if (typeof window === "undefined") return "";
  const { protocol, origin } = window.location;
  return protocol === "http:" || protocol === "https:" ? origin : "";
}

export const session = {
  server(): string {
    return localStorage.getItem(SERVER_KEY) || defaultServer();
  },
  setServer(url: string) {
    const trimmed = url.trim().replace(/\/+$/, "");
    localStorage.setItem(SERVER_KEY, trimmed);
    if (trimmed) {
      const known = new Set(session.knownServers());
      known.add(trimmed);
      localStorage.setItem(KNOWN_KEY, JSON.stringify([...known]));
    }
  },
  // knownServers is what makes reconnecting to a saved server one click
  // rather than retyping an address.
  knownServers(): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(KNOWN_KEY) ?? "[]");
      return Array.isArray(raw) ? (raw as string[]) : [];
    } catch {
      return [];
    }
  },
  forgetServer(url: string) {
    localStorage.setItem(
      KNOWN_KEY,
      JSON.stringify(session.knownServers().filter((s) => s !== url)),
    );
  },
  // sameOrigin reports whether this page is served by the server it talks to,
  // which is when the cookie works and nothing needs storing.
  sameOrigin(): boolean {
    const base = localStorage.getItem(SERVER_KEY);
    return !base || base === defaultServer();
  },
  // setLocal records what the install said about itself, and is called before
  // anything reads a token. Learning that this is a server DEMOTES a token
  // already sitting in localStorage rather than leaving it there: an install
  // that used to be local, or was reached this way before this rule existed,
  // must not keep a credential on disk once it says it is a server.
  setLocal(local: boolean) {
    remembered = local;
    if (local) return;
    const stale = localStorage.getItem(TOKEN_KEY);
    if (stale) {
      localStorage.removeItem(TOKEN_KEY);
      sessionStorage.setItem(TOKEN_KEY, stale);
    }
  },
  local(): boolean {
    return remembered !== false;
  },
  // Both are read, in that order, because a token is written before the app
  // has always finished asking which kind of install this is.
  token(): string | null {
    return localStorage.getItem(TOKEN_KEY) ?? sessionStorage.getItem(TOKEN_KEY);
  },
  // keep is what a sign-in calls. Served by the server it talks to, the cookie
  // it just set is the credential and storing a second copy in JavaScript would
  // undo exactly the protection the cookie was chosen for.
  keep(token: string) {
    if (session.sameOrigin()) {
      session.setToken(null);
      return;
    }
    session.setToken(token);
  },
  setToken(token: string | null) {
    localStorage.removeItem(TOKEN_KEY);
    sessionStorage.removeItem(TOKEN_KEY);
    if (token) store().setItem(TOKEN_KEY, token);
  },
  // url builds an absolute URL when a server is configured, and leaves the
  // path relative when the page is served by the server itself.
  url(path: string): string {
    const base = session.server();
    return base ? base + path : path;
  },
  headers(extra: Record<string, string> = {}): Record<string, string> {
    const token = session.token();
    return token ? { ...extra, Authorization: `Bearer ${token}` } : extra;
  },
};
