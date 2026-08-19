import { useCallback, useEffect, useRef } from 'react'

/** Process a node if htmx has loaded, and keep trying until it has. */
function tell(node: Element): () => void {
  let frame = 0
  const attach = () => {
    // htmx is a deferred script placed before this application's module, so it
    // has run by now. The retry is for the day somebody reorders them: without
    // it, a missing htmx here is silent, and silence is precisely the failure
    // this hook exists to end.
    const htmx = (window as unknown as { htmx?: { process(el: Element): void } }).htmx
    if (htmx) {
      htmx.process(node)
      return
    }
    frame = requestAnimationFrame(attach)
  }
  attach()
  return () => cancelAnimationFrame(frame)
}

/**
 * Tell htmx about a node this application just rendered.
 *
 * htmx scans the document once, when it loads. Anything React mounts afterwards
 * is invisible to it: the attributes are in the DOM, they look right in the
 * inspector, and nothing ever fires. That is not a subtle failure — it is every
 * drawer in the workspace opening empty, with the server answering 200 and the
 * markup sitting there unfetched, which is exactly what it did.
 *
 * `htmx.process(node)` is the documented way to say "this is new, look at it".
 *
 * A CALLBACK ref rather than a ref object with an effect beside it, and that
 * distinction is the whole reason this file has a comment. A drawer's container
 * does not exist until the drawer opens, so an effect keyed on anything else
 * runs once — while the ref is still null — and never again. The first version
 * of this hook did exactly that, and the one panel that happened to work worked
 * by accident, because its key contained the drawer's own name and so changed
 * as it opened. React calls a callback ref at the moment the node appears,
 * which is the moment that matters.
 *
 * `url` re-processes when what the container asks for changes: opening memory
 * for a different agent rewrites hx-get on the same element, and a node still
 * carrying the previous URL answers with the previous agent's memory.
 */
export function useHtmx<T extends HTMLElement>(url: string) {
  const node = useRef<T | null>(null)
  const cancel = useRef<(() => void) | null>(null)

  const ref = useCallback((el: T | null) => {
    cancel.current?.()
    cancel.current = null
    node.current = el
    if (el) cancel.current = tell(el)
  }, [])

  useEffect(() => {
    if (!node.current) return
    cancel.current?.()
    cancel.current = tell(node.current)
    return () => cancel.current?.()
  }, [url])

  return ref
}
