// Session holds what the client needs to reach a server: which one, and as
// whom. The same bundle runs in three shells — served by the server itself
// (web), wrapped in a desktop app, and later a TUI — so the server address
// is data, not a build-time constant.
//
// Served from the server, the origin IS the server and no address is needed.
// A desktop shell has no origin of its own, so it stores one, along with the
// servers seen before.

const TOKEN_KEY = 'cogitorium.token'
const SERVER_KEY = 'cogitorium.server'
const KNOWN_KEY = 'cogitorium.servers'

// Served over http(s) from a real host, the page's own origin is the server.
// A desktop shell loads from file:// or a bundled origin and must be told.
function defaultServer(): string {
  if (typeof window === 'undefined') return ''
  const { protocol, origin } = window.location
  return protocol === 'http:' || protocol === 'https:' ? origin : ''
}

export const session = {
  server(): string {
    return localStorage.getItem(SERVER_KEY) || defaultServer()
  },
  setServer(url: string) {
    const trimmed = url.trim().replace(/\/+$/, '')
    localStorage.setItem(SERVER_KEY, trimmed)
    if (trimmed) {
      const known = new Set(session.knownServers())
      known.add(trimmed)
      localStorage.setItem(KNOWN_KEY, JSON.stringify([...known]))
    }
  },
  // knownServers is what makes reconnecting to a saved server one click
  // rather than retyping an address.
  knownServers(): string[] {
    try {
      const raw = JSON.parse(localStorage.getItem(KNOWN_KEY) ?? '[]')
      return Array.isArray(raw) ? (raw as string[]) : []
    } catch {
      return []
    }
  },
  forgetServer(url: string) {
    localStorage.setItem(KNOWN_KEY, JSON.stringify(session.knownServers().filter((s) => s !== url)))
  },
  token(): string | null {
    return localStorage.getItem(TOKEN_KEY)
  },
  setToken(token: string | null) {
    if (token) localStorage.setItem(TOKEN_KEY, token)
    else localStorage.removeItem(TOKEN_KEY)
  },
  // url builds an absolute URL when a server is configured, and leaves the
  // path relative when the page is served by the server itself.
  url(path: string): string {
    const base = session.server()
    return base ? base + path : path
  },
  headers(extra: Record<string, string> = {}): Record<string, string> {
    const token = session.token()
    return token ? { ...extra, Authorization: `Bearer ${token}` } : extra
  },
}
