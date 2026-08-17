import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
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
import { applyTheme, loadTheme } from './styles/theme'
import Rail from './Rail'
import { ShellProvider } from './shell'

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
      {/* The shell: a frame, and a hole in it.
       *
       * The bezel is ground showing around everything. The rail stands on its
       * left as floating capsules. The cavity is the hole they surround, and
       * it holds CONTENT ONLY — no tabs, no counts, no title, no account.
       * Every control is on the frame.
       *
       * What this replaced was a header row across the top of the work
       * carrying the brand, seven destinations, the theme, the docs link and
       * the account. It read as a web page because it was one: chrome above,
       * content below. The instrument now surrounds the window instead of
       * sitting on top of it, which is what the reference shell does and what
       * the whole look depends on. */}
      <ShellProvider>
        <Rail user={user} health={health} onSignOut={signOut} />
        <div className="frame">
        <main className="cavity">
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
      </ShellProvider>
    </BrowserRouter>
  )
}
