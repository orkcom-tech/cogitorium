import { useCallback, useEffect, useState } from 'react'
import { BrowserRouter } from 'react-router-dom'
import { auth, setup, Unauthorized, type User } from './api'
import { session } from './session'
import Rail from './Rail'
import { ShellProvider } from './shell'

/**
 * The rail, on a screen the SERVER rendered.
 *
 * This product draws half its screens as documents and half in the browser,
 * and the rail was written twice — so the two differed in a dozen small ways
 * and every one of them reached somebody as "why is this screen not like that
 * one": a logo on one half, destinations on the other, a foot with different
 * controls, Plugins drawn twice where they overlapped.
 *
 * One RENDERER now. The template still draws a rail into the document — that
 * is what somebody sees before this runs, what a plugin overriding
 * cog.shell.rail changes, and what a browser with no JavaScript keeps — and
 * this replaces it with the same component the application's own screens use,
 * fed by the same description the template was fed.
 *
 * It mounts the rail and NOTHING else. The comment on appHead is still true:
 * booting the whole application on top of a page it is inside would put React
 * over a document the server already rendered. The rail is not that; it is the
 * frame around it, and the frame is the thing that has to be the same.
 */
export default function RailOnly({ onReady }: { onReady: () => void }) {
  const [user, setUser] = useState<User | null>(null)
  const [health, setHealth] = useState<{ status: string; version: string } | null>(null)

  useEffect(() => {
    // The same two questions the application asks, in the same order and for
    // the same reason: the install decides where a token may be kept, and a
    // token read before that is settled could be read from disk on a server
    // that must not keep one.
    setup
      .state()
      .then((st) => {
        session.setLocal(st.local)
        return st
      })
      .catch(() => null)
      .then((st) => {
        if (st?.needs_setup) return
        return auth
          .whoami()
          .then(setUser)
          .catch((e: unknown) => {
            if (!(e instanceof Unauthorized)) throw e
          })
      })
      .catch(() => {
        // The rail the template drew stays on screen. A frame that vanishes
        // because a request failed is worse than one that is a version behind.
      })
    fetch(session.url('/health'))
      .then((r) => r.json())
      .then(setHealth)
      .catch(() => setHealth(null))
  }, [])

  // The same sign-out the application does, and then away: this page is the
  // server's, and a signed-out person looking at it is looking at something
  // they may no longer read.
  const signOut = useCallback(() => {
    auth
      .logout()
      .catch(() => undefined)
      .finally(() => {
        session.setToken(null)
        window.location.assign('/')
      })
  }, [])

  // The template's rail is on screen until this one can replace it, so there
  // is no moment with no frame at all.
  useEffect(() => {
    if (user) onReady()
  }, [user, onReady])

  if (!user) return null

  return (
    <BrowserRouter>
      <ShellProvider>
        <Rail user={user} health={health} onSignOut={signOut} />
      </ShellProvider>
    </BrowserRouter>
  )
}
