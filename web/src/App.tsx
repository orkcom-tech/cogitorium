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
import { applyTheme, loadTheme } from './styles/theme'

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
        <div className="shell-ground" aria-hidden />
        <div className="shell-grain" aria-hidden />
        <aside className="sidebar">
          <button
            className="nav-toggle"
            onClick={() => setNavOpen((v) => !v)}
            title={navOpen ? 'Collapse the sidebar (⌘B)' : 'Expand the sidebar (⌘B)'}
            aria-label={navOpen ? 'Collapse the sidebar' : 'Expand the sidebar'}
          >
            {navOpen ? '⟨' : '⟩'}
          </button>
          <h1 className="brand">Cogitorium</h1>
          <nav>
            <NavLink to="/workspaces" title="Workspaces">
              <span>Workspaces</span>
            </NavLink>
            {/* The Context page browses the whole space — every workspace's
                memory and every agent's private branch — so it follows the
                Terminal's rule and is admin-only. Members reach context
                through their own workspace's bindings. */}
            {user.role === 'admin' && <NavLink to="/context" title="Context">
              <span>Context</span>
            </NavLink>}
            <NavLink to="/gears" title="Gears">
              <span>Gears</span>
            </NavLink>
            <NavLink to="/instructions" title="Instructions">
              <span>Instructions</span>
            </NavLink>
            <NavLink to="/models" title="Models">
              <span>Models</span>
            </NavLink>
            {user.role === 'admin' && <NavLink to="/terminal" title="Terminal">
              <span>Terminal</span>
            </NavLink>}
            {user.role === 'admin' && <NavLink to="/people" title="People">
              <span>People</span>
            </NavLink>}
          </nav>
          <footer className="sidebar-footer">
            <div>
              {user.name} · {user.role}
            </div>
            <div>{health ? `${health.version} · ${health.status}` : 'server unreachable'}</div>
            <button className="linkish" onClick={signOut}>
              sign out
            </button>
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
