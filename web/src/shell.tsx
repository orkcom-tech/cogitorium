import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

/**
 * What the frame needs to know about what is in the cavity.
 *
 * The rule the shell is built on is that every control lives on the frame and
 * the cavity holds only content. A workspace, though, owns controls: which of
 * its three views is showing, which of its four drawers is out, its name, and
 * the way out of it. Those cannot live in the cavity, and the rail cannot
 * import them from a route it does not render.
 *
 * So the screen in the cavity PUBLISHES what the frame should offer, and the
 * rail renders it. The direction matters: the rail knows nothing about
 * workspaces, and the workspace knows nothing about how the rail draws. Either
 * one can be rebuilt without touching the other.
 *
 * It is deliberately not a store. There is exactly one screen in the cavity at
 * a time, so a single value with a setter is the whole requirement, and a
 * store would be a second place for the same truth to live.
 */

export type StageId = string

export type Stage = {
  id: StageId
  title: string
  /** Drawn on the rail. Inline SVG: this product fetches nothing at runtime. */
  icon: ReactNode
}

export type Drawer = {
  id: string
  title: string
  icon: ReactNode
  /** A count worth showing on the button, the way a tray icon carries a dot. */
  badge?: number
}

export type CavityShell = {
  /** What the cavity is showing, in the operator's words. Rotated onto the
   *  rail, because it is a label about the work rather than part of it. */
  here: { label: string; note?: string; state?: string }
  /** Where "out of here" goes, if there is an out. */
  back?: string
  stages?: { items: Stage[]; current: StageId; go: (id: StageId) => void }
  drawers?: { items: Drawer[]; open: string | null; toggle: (id: string | null) => void }
  /** One screen-specific action, if the screen has exactly one. More than one
   *  and it belongs in the screen, not on the frame. */
  action?: { label: string; title?: string; run: () => void }
}

const Ctx = createContext<{
  shell: CavityShell | null
  set: (s: CavityShell | null) => void
}>({ shell: null, set: () => {} })

export function ShellProvider({ children }: { children: ReactNode }) {
  const [shell, set] = useState<CavityShell | null>(null)
  return <Ctx.Provider value={{ shell, set }}>{children}</Ctx.Provider>
}

/** Read what the cavity published. The rail's side of the wire. */
export function useShell() {
  return useContext(Ctx).shell
}

/**
 * Publish what the frame should offer while this screen is in the cavity, and
 * take it down when the screen leaves.
 *
 * `deps` is the caller's own list, exactly as an effect's would be: this hook
 * cannot compute one, because the value is rebuilt on every render and
 * comparing it would mean comparing functions.
 */
export function usePublishShell(build: () => CavityShell | null, deps: unknown[]) {
  const { set } = useContext(Ctx)
  useEffect(() => {
    set(build())
    return () => set(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)
}
