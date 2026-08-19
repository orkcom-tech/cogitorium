import { useCallback, useEffect, useState } from 'react'
import { api, type GraphData } from '../api'
import GraphCanvas from './GraphCanvas'
import { useHtmx } from '../htmx'

// Users and teams. A single-operator install never needs this page — the
// seeded admin is the only account — so it exists for the moment an install
// stops being single-operator.
export default function AdminPage() {
  const [error, setError] = useState<string | null>(null)
  // The half of this screen the server renders. See the hook: htmx does not
  // see what React mounts unless it is told.
  const lists = useHtmx<HTMLDivElement>('people-lists')
  const [map, setMap] = useState<GraphData | null>(null)
  const [mapLayers, setMapLayers] = useState<Record<string, boolean>>({})

  // Only the map is fetched here now. Who exists and who is in which team is
  // the server's to render, and it renders it into the panel below.
  const reload = useCallback(() => {
    api.graph
      .map()
      .then((m) => {
        setMap(m)
        setError(null)
      })
      .catch((e: Error) => setError(e.message))
  }, [])

  useEffect(reload, [reload])

  return (
    <div className="page">
      <h2>People</h2>
      {error && <p className="error">{error}</p>}

      <section>
        <h3>Access map</h3>
        <p className="hint">
          Who owns what and who can reach it — the same relationships the permission checks use, drawn instead of
          pieced together from the tables below. Click a colour in the legend to hide that layer.
        </p>
        <div className="map-canvas">
          <GraphCanvas
            data={map}
            layers={mapLayers}
            onToggleLayer={(kind) => setMapLayers((p) => ({ ...p, [kind]: p[kind] === false }))}
            emptyHint="Nothing to draw yet — create a workspace or a second user."
          />
        </div>
      </section>

      {/* Users and teams are the server's now: a list of names is words, and
          words are what a template is for. The access map above stays here —
          it is a drawn graph, and a template renders a thing that exists at a
          moment rather than a layout somebody drags. */}
      <div ref={lists} hx-get="/people/lists" hx-trigger="load" hx-swap="innerHTML" />

    </div>
  )
}



