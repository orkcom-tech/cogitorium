import { useCallback, useEffect, useRef, useState } from 'react'
import { BrowserRouter, Link, NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { auth, Unauthorized, type User } from './api'
import { session } from './session'
import LoginPage from './pages/LoginPage'
import ModelsPage from './pages/ModelsPage'
import WorkspacesPage from './pages/WorkspacesPage'
import WorkspacePage from './pages/WorkspacePage'
import ContextPage from './pages/ContextPage'
import GearsPage from './pages/GearsPage'
import EnvPage from './pages/EnvPage'
import LibraryPage from './pages/LibraryPage'
import AdminPage from './pages/AdminPage'
import TerminalPage from './pages/TerminalPage'
import InstallMap from './pages/InstallMap'
import ThemeMenu from './pages/ThemeMenu'
import { applyTheme, loadTheme } from './styles/theme'
import { COG_MARK, DOCS_URL, ORKCOM_URL, ORK_MARK } from './styles/brand'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [checking, setChecking] = useState(true)
  // ⌘B and the collapse state went with the rail it collapsed. There is no
  // global key binding left in the app at all: Escape is deliberately absent
  // because the approval dialog owns it, scoped to itself, so one keypress can
  // never both dismiss some chrome and silently refuse a pending web search.

  // whoami decides the shell: on loopback it succeeds without credentials,
  // which is why a single-operator install never sees a login screen.
  const identify = useCallback(() => {
    setChecking(true)
    auth
      .whoami()
      .then(setUser)
      .catch((e: unknown) => {
        if (e instanceof Unauthorized) setUser(null)
      })
      .finally(() => setChecking(false))
  }, [])

  useEffect(identify, [identify])

  // The palette is applied before the first paint of the shell, so the app
  // never flashes the default ground on its way to the operator's own.
  useEffect(() => applyTheme(loadTheme()), [])

  useEffect(() => {
    fetch(session.url('/health'))
      .then((r) => r.json())
      .then(setHealth)
      .catch(() => setHealth(null))
  }, [user])

  if (checking) return <main className="shell">connecting…</main>
  if (!user) return <LoginPage onSignedIn={identify} />

  const signOut = () =>
    auth
      .logout()
      .catch(() => undefined)
      .finally(() => {
        session.setToken(null)
        setUser(null)
      })

  return (
    <BrowserRouter>
      {/* The shell.
       *
       * A white sheet resting on a grey ground, with everything the operator
       * navigates by in ONE row across its top. What this replaced was a
       * 200px left rail carrying nine destinations, a collapse toggle, the
       * brand, the account block and the sign-out button — a column that took
       * a sixth of every screen to hold things most of which are opened once a
       * week.
       *
       * The split is by frequency, not by kind. What you move between all day
       * is a pill in the middle. What you configure occasionally lives behind
       * the account button. Nothing is hidden that was reachable before; it is
       * one click further away and the canvas is a sixth wider. */}
      <div className="app-shell">
        <div className="shell-ground" aria-hidden />
        <div className="sheet">
          <header className="sheet-head">
            <div className="brand">
              <Link to="/workspaces" className="brand-link" title="Cogitorium">
                <img className="brand-mark" src={COG_MARK} alt="" width={24} height={24} />
                <span className="wordmark">Cogitorium</span>
              </Link>
              {/* The maker, stated once and nowhere else. The mark alone, with
                  no "com" set after it: the lockup already reads as a word, and
                  typing the rest of the company name beside a logo that spells
                  it is the same thing said twice.

                  It carries no colour of its own — alpha over currentColor —
                  so it belongs to whatever look is in force. */}
              <a className="by-ork" href={ORKCOM_URL} target="_blank" rel="noreferrer" title="ORKCOM">
                <span className="by-word">by</span>
                <span
                  className="ork-mark"
                  aria-hidden
                  style={{ maskImage: `url("${ORK_MARK}")`, WebkitMaskImage: `url("${ORK_MARK}")` }}
                />
                <span className="sr-only">ORKCOM</span>
              </a>
            </div>

            {/* The places you actually work, and only those. An admin's
                install-wide pages are in the account menu with the rest of the
                configuration. */}
            <nav className="pill-nav">
              <NavLink to="/workspaces">Workspaces</NavLink>
              <NavLink to="/map">Map</NavLink>
              <NavLink to="/gears">Gears</NavLink>
              <NavLink to="/instructions">Instructions</NavLink>
              <NavLink to="/models">Models</NavLink>
              {user.role === 'admin' && <NavLink to="/context">Context</NavLink>}
              {/* People is where the relationship graph and the access map
                  live. It went into the account menu with the other admin
                  pages and should not have: a picture of who can reach what is
                  something you go and look at, not a setting you change. */}
              {user.role === 'admin' && <NavLink to="/people">People</NavLink>}
            </nav>

            <div className="head-tools">
              <ThemeMenu />
              <a
                className="tool-btn round"
                href={DOCS_URL}
                target="_blank"
                rel="noreferrer"
                title="Documentation"
              >
                <span aria-hidden>?</span>
                <span className="sr-only">Documentation</span>
              </a>
              <AccountMenu user={user} health={health} onSignOut={signOut} />
            </div>
          </header>
        <main className="content">
          <Routes>
            <Route path="/" element={<Navigate to="/workspaces" replace />} />
            <Route path="/workspaces" element={<WorkspacesPage me={user} />} />
            <Route path="/map" element={<InstallMap />} />
            <Route path="/people" element={user.role === 'admin' ? <AdminPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/terminal" element={user.role === 'admin' ? <TerminalPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/workspaces/:id" element={<WorkspacePage />} />
            <Route path="/context" element={user.role === 'admin' ? <ContextPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/gears" element={<GearsPage me={user} />} />
            <Route path="/env" element={user.role === 'admin' ? <EnvPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/instructions" element={<LibraryPage />} />
            <Route path="/models" element={<ModelsPage />} />
          </Routes>
        </main>
        </div>
      </div>
    </BrowserRouter>
  )
}

/**
 * Everything that is not a destination: who you are, what the server is, the
 * install-wide pages, and the way out.
 *
 * It is a menu rather than a rail because none of it is used more than once a
 * session — and the one control in it that ends the session is kept apart from
 * the ones that merely navigate, so "sign out" is never the neighbour of a
 * page you meant to open.
 */
function AccountMenu({
  user,
  health,
  onSignOut,
}: {
  user: User
  health: { version: string; status: string } | null
  onSignOut: () => void
}) {
  const [open, setOpen] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    // Deferred, or the click that opened it closes it again on the same tick.
    const t = setTimeout(() => document.addEventListener('mousedown', away), 0)
    document.addEventListener('keydown', key)
    return () => {
      clearTimeout(t)
      document.removeEventListener('mousedown', away)
      document.removeEventListener('keydown', key)
    }
  }, [open])

  return (
    <div className="acct" ref={box}>
      <button
        className="acct-btn round"
        data-own
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        title={user.name}
      >
        {user.name.slice(0, 1).toUpperCase()}
      </button>
      {open && (
        <div className="acct-menu" role="menu">
          <div className="acct-who">
            <strong>{user.name}</strong>
            <span className="muted">{user.role}</span>
          </div>
          {user.role === 'admin' && (
            <>
              <hr />
              <Link to="/env" onClick={() => setOpen(false)}>Variables &amp; secrets</Link>
              <Link to="/terminal" onClick={() => setOpen(false)}>Terminal</Link>
            </>
          )}
          <hr />
          <div className="acct-server">
            {health ? `${health.version} · ${health.status}` : 'server unreachable'}
          </div>
          <button className="acct-out" onClick={onSignOut}>
            sign out
          </button>
        </div>
      )}
    </div>
  )
}


