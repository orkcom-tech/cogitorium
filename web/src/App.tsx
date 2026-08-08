import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { auth, Unauthorized, type User } from './api'
import { session } from './session'
import LoginPage from './pages/LoginPage'
import ModelsPage from './pages/ModelsPage'
import ChatPage from './pages/ChatPage'
import WorkspacesPage from './pages/WorkspacesPage'
import WorkspacePage from './pages/WorkspacePage'
import ContextPage from './pages/ContextPage'
import GearsPage from './pages/GearsPage'
import LibraryPage from './pages/LibraryPage'
import AdminPage from './pages/AdminPage'
import TerminalPage from './pages/TerminalPage'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [checking, setChecking] = useState(true)

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
      <div className="layout">
        <aside className="sidebar">
          <h1 className="brand">Cogitorium</h1>
          <nav>
            <NavLink to="/workspaces">Workspaces</NavLink>
            {/* The Context page browses the whole space — every workspace's
                memory and every agent's private branch — so it follows the
                Terminal's rule and is admin-only. Members reach context
                through their own workspace's bindings. */}
            {user.role === 'admin' && <NavLink to="/context">Context</NavLink>}
            <NavLink to="/gears">Gears</NavLink>
            <NavLink to="/instructions">Instructions</NavLink>
            <NavLink to="/models">Models</NavLink>
            <NavLink to="/chat">Scratch chat</NavLink>
            {user.role === 'admin' && <NavLink to="/terminal">Terminal</NavLink>}
            {user.role === 'admin' && <NavLink to="/people">People</NavLink>}
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
            <Route path="/chat" element={<ChatPage />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}
