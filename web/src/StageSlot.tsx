import { useHtmx } from './htmx'

/**
 * The one place a plugin can reach the screens this application draws.
 *
 * Four screens are not templates and cannot honestly become ones — the
 * blueprint and the map are drawn canvases, the editor is live text, the
 * terminal is a socket, and a template renders a thing that exists at a
 * moment. So what a plugin is given is the space AROUND them: a strip above
 * the canvas, rendered by the server through the same composed template stack
 * as every other screen, which means an override of `cog.slot.stagehead`
 * applies here exactly as it applies anywhere else.
 *
 * It renders nothing at all unless somebody has overridden that name. The
 * container stays in the tree either way, because a container that appears
 * only when a plugin arrives is a container React has to be told about at the
 * wrong moment — see htmx.ts for what that costs.
 */
export default function StageSlot({ screen, wsId }: { screen: string; wsId?: number }) {
  const url = `/cog/slot/stage-head?screen=${encodeURIComponent(screen)}${wsId ? `&ws=${wsId}` : ''}`
  const ref = useHtmx<HTMLDivElement>(url)
  return <div className="stage-slot" ref={ref} hx-get={url} hx-trigger="load" hx-swap="innerHTML" />
}
