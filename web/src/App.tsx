import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { auth, setup, Unauthorized, type SetupState, type User } from './api'
import { session } from './session'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import WorkspacePage from './pages/WorkspacePage'
import AdminPage from './pages/AdminPage'
import TerminalPage from './pages/TerminalPage'
import InstallMap from './pages/InstallMap'
import { applyTheme, loadTheme } from './styles/theme'
import Rail from './Rail'
import { ShellProvider } from './shell'

type Health = { status: string; version: string }

/** Go to a screen the server renders, by leaving this application.
 *
 * react-router's Navigate pushes a path and looks for a route. There is no
 * route for a server template, so it renders nothing and the person is left
 * looking at an empty frame with a working rail around it.
 */
/**
 * A path this application does not own.
 *
 * It asks the server for it, which is what should have happened in the first
 * place — and remembers it tried, so a path neither side serves says so
 * instead of reloading for ever.
 */
function Elsewhere() {
  const path = window.location.pathname + window.location.search
  const tried = sessionStorage.getItem(RETRIED) === path
  useEffect(() => {
    if (tried) return
    sessionStorage.setItem(RETRIED, path)
    window.location.replace(path)
  }, [tried, path])
  if (!tried) return null
  return (
    <p className="hint">
      There is no screen at <code>{path}</code>. <a href="/workspaces">Back to your workspaces</a>.
    </p>
  )
}

const RETRIED = 'cogitorium.retried'

function Leave({ to }: { to: string }) {
  useEffect(() => {
    window.location.replace(to)
  }, [to])
  return null
}

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
            {/* A real navigation, not a client-side one. /workspaces is a
                server template: pushing the path would render an empty cavity,
                which is exactly what signing in used to land on. */}
            <Route path="/" element={<Leave to="/workspaces" />} />
            {/* Everything not listed above is a server template: /workspaces,
                /gears, /models, /instructions, /context, /env and /plugins.
                The router must not claim them — a client route would shadow
                the page and put React back over the top of it.

                What is left here is what a template cannot be. /map and the
                blueprint are drawn canvases, the editor is live text, and
                /terminal is a socket: a template renders a thing that exists
                at a moment, and all four exist in motion. */}
            <Route path="/map" element={<InstallMap />} />
            <Route path="/people" element={user.role === 'admin' ? <AdminPage /> : <Leave to="/workspaces" />} />
            <Route path="/terminal" element={user.role === 'admin' ? <TerminalPage /> : <Leave to="/workspaces" />} />
            <Route path="/workspaces/:id" element={<WorkspacePage me={user} />} />
            {/* Anything else here is a screen the SERVER renders, and the only
                way this router is looking at one is history: pressing Back out
                of a workspace restores this application at /workspaces without
                a request, and React Router — correctly — matches nothing. The
                result was a blank cavity and no way out but the address bar.

                So: fetch it properly, once. The guard is what keeps it from
                becoming a loop on a path nothing serves. */}
            <Route path="*" element={<Elsewhere />} />
          </Routes>
          </main>
        </div>
      </ShellProvider>
    </BrowserRouter>
  )
}
