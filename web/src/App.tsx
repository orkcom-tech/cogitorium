import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { auth, Unauthorized, type User } from './api'
import { session } from './session'
import LoginPage from './pages/LoginPage'
import ModelsPage from './pages/ModelsPage'
import WorkspacesPage from './pages/WorkspacesPage'
import WorkspacePage from './pages/WorkspacePage'
import ContextPage from './pages/ContextPage'
import GearsPage from './pages/GearsPage'
import LibraryPage from './pages/LibraryPage'
import AdminPage from './pages/AdminPage'
import TerminalPage from './pages/TerminalPage'
import ThemeMenu from './pages/ThemeMenu'
import { applyTheme, loadTheme } from './styles/theme'
import { COG_MARK, DOCS_URL, ORKCOM_URL, ORK_MARK } from './styles/brand'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [checking, setChecking] = useState(true)
  // The sidebar is 200px the two most space-starved views cannot spare. It
  // collapses to a rail of icons and remembers the choice per device.
  const [navOpen, setNavOpen] = useState(() => localStorage.getItem('cogitorium.nav') !== 'collapsed')

  useEffect(() => {
    localStorage.setItem('cogitorium.nav', navOpen ? 'open' : 'collapsed')
  }, [navOpen])

  // The ONLY global key binding in the app. Escape is deliberately absent:
  // the approval dialog owns it, scoped to itself, so one keypress can never
  // both dismiss some chrome and silently refuse a pending web search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'b') {
        e.preventDefault()
        setNavOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

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
      <div className={`layout ${navOpen ? '' : 'nav-collapsed'}`}>
        <div className="shell-backdrop" aria-hidden>
          <Backdrop />
        </div>
        <div className="shell-scrim" aria-hidden />
        <div className="shell-ground" aria-hidden />
        <div className="shell-glow" aria-hidden />
        <div className="shell-grain" aria-hidden />
        <button
          className="nav-toggle"
          onClick={() => setNavOpen((v) => !v)}
          title={navOpen ? 'Hide the menu (⌘B)' : 'Show the menu (⌘B)'}
          aria-label={navOpen ? 'Hide the menu' : 'Show the menu'}
          aria-expanded={navOpen}
        >
          {navOpen ? '⟨' : '☰'}
        </button>
        <aside className="sidebar" aria-hidden={!navOpen}>
          <div className="brand-block">
            <h1 className="brand">
              <img className="brand-mark" src={COG_MARK} alt="" width={26} height={26} />
              <span className="wordmark">Cogitorium</span>
            </h1>
            {/* The maker, stated once and not repeated anywhere else. The mark
                carries no colour of its own — it is alpha over currentColor —
                so it belongs to whatever palette the operator has chosen. */}
            <a
              className="by-ork"
              href={ORKCOM_URL}
              target="_blank"
              rel="noreferrer"
              title="ORKCOM on GitHub"
            >
              <span className="by-word">by</span>
              {/* The mark spells ORK and the company is ORKCOM, so the type
                  finishes the word the logo starts. Setting both the mark and
                  the full name side by side would read as a duplicate. */}
              <span className="ork-lockup" aria-hidden>
                <span className="ork-mark" style={{ maskImage: `url("${ORK_MARK}")`, WebkitMaskImage: `url("${ORK_MARK}")` }} />
                <span className="ork-com">com</span>
              </span>
              <span className="sr-only">ORKCOM</span>
            </a>
          </div>
          <nav className="pane side-pane">
            <NavLink to="/workspaces" title="Workspaces">
              <span className="nav-icon" aria-hidden>▦</span>
              <span>Workspaces</span>
            </NavLink>
            {/* The Context page browses the whole space — every workspace's
                memory and every agent's private branch — so it follows the
                Terminal's rule and is admin-only. Members reach context
                through their own workspace's bindings. */}
            {user.role === 'admin' && <NavLink to="/context" title="Context">
              <span className="nav-icon" aria-hidden>◇</span>
              <span>Context</span>
            </NavLink>}
            <NavLink to="/gears" title="Gears">
              <span className="nav-icon" aria-hidden>⚙</span>
              <span>Gears</span>
            </NavLink>
            <NavLink to="/instructions" title="Instructions">
              <span className="nav-icon" aria-hidden>❏</span>
              <span>Instructions</span>
            </NavLink>
            <NavLink to="/models" title="Models">
              <span className="nav-icon" aria-hidden>◈</span>
              <span>Models</span>
            </NavLink>
            {user.role === 'admin' && <NavLink to="/terminal" title="Terminal">
              <span className="nav-icon" aria-hidden>▤</span>
              <span>Terminal</span>
            </NavLink>}
            {user.role === 'admin' && <NavLink to="/people" title="People">
              <span className="nav-icon" aria-hidden>◉</span>
              <span>People</span>
            </NavLink>}
            {/* An <a>, not a NavLink: the documentation is a site rather than a
                route, and it opens beside the workbench instead of replacing
                it — nobody wants to lose a running turn to read a page. */}
            <a href={DOCS_URL} target="_blank" rel="noreferrer" title="Documentation" className="nav-external">
              <span className="nav-icon" aria-hidden>❯</span>
              <span>Documentation</span>
            </a>
          </nav>
          <footer className="pane side-pane account">
            <div className="account-who">
              <span className="avatar" aria-hidden>
                {user.name.slice(0, 1).toUpperCase()}
              </span>
              <div className="account-id">
                <strong>{user.name}</strong>
                <span className="muted">{user.role}</span>
              </div>
            </div>

            <ThemeMenu />

            <div className="account-server">
              {health ? `${health.version} · ${health.status}` : 'server unreachable'}
            </div>

            {/* Signing out gets its own bordered block: it is the one control
                here that ends the session, and it read as a footnote. */}
            <div className="account-out">
              <button className="danger" onClick={signOut} title="Sign out">
                <span className="out-icon" aria-hidden>
                  ⏻
                </span>
                <span className="out-label">sign out</span>
              </button>
            </div>
          </footer>
        </aside>
        <main className="content">
          <Routes>
            <Route path="/" element={<Navigate to="/workspaces" replace />} />
            <Route path="/workspaces" element={<WorkspacesPage me={user} />} />
            <Route path="/people" element={user.role === 'admin' ? <AdminPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/terminal" element={user.role === 'admin' ? <TerminalPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/workspaces/:id" element={<WorkspacePage />} />
            <Route path="/context" element={user.role === 'admin' ? <ContextPage /> : <Navigate to="/workspaces" replace />} />
            <Route path="/gears" element={<GearsPage />} />
            <Route path="/instructions" element={<LibraryPage />} />
            <Route path="/models" element={<ModelsPage />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}

/** The operator's own clip, when they set one. An image is a CSS background;
 *  a video needs a real element, muted and inline so a browser will autoplay
 *  it at all. */
function Backdrop() {
  const t = loadTheme()
  if (t.bg.kind !== 'video' || !t.bg.data) return null
  return <video src={t.bg.data} autoPlay loop muted playsInline />
}
