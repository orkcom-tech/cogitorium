import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { auth, setup, Unauthorized, type SetupState, type User } from './api'
import { session } from './session'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import WorkspacesPage from './pages/WorkspacesPage'
import WorkspacePage from './pages/WorkspacePage'
import GearsPage from './pages/GearsPage'
import EnvPage from './pages/EnvPage'
import AdminPage from './pages/AdminPage'
import TerminalPage from './pages/TerminalPage'
import InstallMap from './pages/InstallMap'
import PluginsPage from './pages/PluginsPage'
import { applyTheme, loadTheme } from './styles/theme'
import Rail from './Rail'
import { ShellProvider } from './shell'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [install, setInstall] = useState<SetupState | null>(null)
  const [checking, setChecking] = useState(true)
  // ⌘B and the collapse state went with the rail it collapsed. There is no
  // global key binding left in the app at all: Escape is deliberately absent
  // because the approval dialog owns it, scoped to itself, so one keypress can
  // never both dismiss some chrome and silently refuse a pending web search.

  // Two questions, in this order, and the order is the point.
  //
  // The install is asked about itself FIRST, without credentials, because the
  // answer decides where a token may be kept — and a token read before that is
  // settled could be read from disk on a server that must not keep one. Then
  // whoami, with whatever token that leaves us holding.
  //
  // There is no third case where neither is needed. A local install used to
  // skip both: any request from this machine was the admin, so the app opened
  // straight into somebody's workspaces without anyone proving anything.
  const identify = useCallback(() => {
    setChecking(true)
    setup
      .state()
      .then((st) => {
        setInstall(st)
        session.setLocal(st.local)
        return st
      })
      .catch(() => null)
      .then((st) => {
        // Nothing to be signed in as yet: skip whoami rather than spend a
        // guaranteed 401 on it.
        if (st?.needs_setup) {
          setUser(null)
          return
        }
        return auth
          .whoami()
          .then(setUser)
          .catch((e: unknown) => {
            if (e instanceof Unauthorized) setUser(null)
          })
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
  if (!user && install?.needs_setup) return <SetupPage local={install.local} onSignedIn={identify} />
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
            <Route path="/workspaces/:id" element={<WorkspacePage me={user} />} />
            {/* /context is a server template now, admin-only there as it was
                here. The component stays: the workspace opens the space as a
                drawer, which is a different surface still to be converted. */}
            <Route path="/gears" element={<GearsPage me={user} />} />
            <Route path="/env" element={user.role === 'admin' ? <EnvPage /> : <Navigate to="/workspaces" replace />} />
            {/* /instructions is served by the server as a template now, so
                the router must not claim it — a client route would shadow the
                page and put React back over the top of it. The component
                stays: the workspace still opens the library as a drawer, and
                that is a different surface with a different name still to be
                converted. */}
            {/* /models is a server template now, like /instructions. The
                component stays: the workspace opens the catalogue as a drawer,
                and that is a different surface still to be converted. */}
            <Route
              path="/plugins"
              element={user.role === 'admin' ? <PluginsPage /> : <Navigate to="/workspaces" replace />}
            />
          </Routes>
          </main>
        </div>
      </ShellProvider>
    </BrowserRouter>
  )
}
