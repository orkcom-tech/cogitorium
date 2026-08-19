import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type Plugin } from '../api'

/** The library.
 *
 * The screen is built around one distinction the server draws and nothing else
 * on this install draws: enabled and live are different questions. A plugin
 * can be switched on and still not be rendering, because its templates could
 * not run against this version — and the gap between those two states is the
 * reason somebody opens this page at all. So the state column never says
 * "enabled" on its own; it says which of the two it means.
 *
 * Everything under the name — what it overrides, what it adds — comes from the
 * server having composed the templates, not from the manifest. A plugin cannot
 * look better here by claiming less.
 */
export default function PluginsPage() {
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [error, setError] = useState<string | null>(null)
  const reloadSeq = useRef(0)

  const reload = useCallback(() => {
    const seq = ++reloadSeq.current
    api.plugins
      .list()
      .then((p) => {
        if (reloadSeq.current !== seq) return
        setPlugins(p)
        setError(null)
      })
      .catch((e: Error) => {
        if (reloadSeq.current === seq) setError(e.message)
      })
  }, [])

  useEffect(reload, [reload])

  return (
    <div className="page">
      <h2>Plugins</h2>
      {error && <p className="error">{error}</p>}

      {plugins.length === 0 && !error && (
        <p className="hint">
          Nothing installed. A plugin is a folder with a <code>plugin.yaml</code> and a{' '}
          <code>templates/</code> directory, installed with{' '}
          <code>cogitorium plugins install &lt;bundle.zip&gt;</code>.
        </p>
      )}

      {plugins.map((p) => (
        <PluginCard key={p.id} plugin={p} />
      ))}
    </div>
  )
}

function PluginCard({ plugin: p }: { plugin: Plugin }) {
  return (
    <section className="card">
      <header className="plugin-head">
        <h3>
          {p.name || p.id} <span className="hint">{p.version}</span>
        </h3>
        <State plugin={p} />
      </header>

      {p.problem && <p className="error">{p.problem}</p>}

      {/* An unavailable tier is a fact about this install, not about the
          plugin, so it is stated where an operator is deciding what to do
          rather than left in a log they would have to go and find. */}
      {!p.available && p.refusal && <p className="error">{p.refusal}</p>}

      {p.enabled && p.live && (
        <p className="hint">
          Layer {p.order} — a plugin later in the order renders instead of an earlier one when both
          define the same name.
        </p>
      )}

      <Names label="Overrides" names={p.overrides} />
      <Names label="Adds" names={p.adds} />
      <Names label="Extends" names={p.extends} />

      {/* Not an error. Declaring an override earns nothing and is not
          required — but the difference between what an operator approved and
          what is actually happening is worth being able to see. */}
      <Names label="Overridden without declaring" names={p.undeclared} tone="warn" />

      {/* A definition nobody owns never renders. Silently inert is the
          hardest kind of plugin bug to find, so it is named here. */}
      <Names label="Inert — nothing installed owns that namespace" names={p.inert} tone="warn" />

      {p.pages && p.pages.length > 0 && (
        <div className="plugin-rows">
          <span className="label">Pages</span>
          <ul>
            {p.pages.map((pg) => (
              <li key={pg.path}>
                {p.live ? <a href={pg.path}>{pg.path}</a> : <span>{pg.path}</span>}{' '}
                <span className={pg.auth === 'none' ? 'error' : 'hint'}>
                  {pg.auth === 'none' ? 'open — no sign-in required' : pg.auth}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      <Grants plugin={p} />

      {(p.docs || p.source) && (
        <p className="hint">
          {p.docs && (
            <a href={p.docs} target="_blank" rel="noreferrer noopener">
              Documentation
            </a>
          )}
          {p.docs && p.source && ' · '}
          {p.source && (
            <a href={p.source} target="_blank" rel="noreferrer noopener">
              Source
            </a>
          )}
        </p>
      )}
    </section>
  )
}

/** The state, and the whole reason this column is not a checkbox. */
function State({ plugin: p }: { plugin: Plugin }) {
  if (p.problem && !p.enabled) return <span className="badge is-danger">broken</span>
  if (!p.enabled) return <span className="badge">off</span>
  if (!p.live) return <span className="badge is-danger">on, not loading</span>
  return <span className="badge is-ok">live</span>
}

function Names({
  label,
  names,
  tone,
}: {
  label: string
  names?: string[]
  tone?: 'warn'
}) {
  if (!names || names.length === 0) return null
  return (
    <div className="plugin-rows">
      <span className={tone === 'warn' ? 'label error' : 'label'}>{label}</span>
      <ul>
        {names.map((n) => (
          <li key={n}>
            <code>{n}</code>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** What it asked for. Grouped away from what it contributes, because these are
 *  the lines an operator is agreeing to rather than the ones describing what
 *  they get. */
function Grants({ plugin: p }: { plugin: Plugin }) {
  const has = (p.hosts?.length ?? 0) + (p.secrets?.length ?? 0) + (p.api?.length ?? 0)
  if (has === 0) return null
  return (
    <div className="plugin-rows">
      <span className="label">Asks for</span>
      <ul>
        {p.hosts?.map((h) => (
          <li key={`h-${h}`}>
            network <code>{h}</code>
          </li>
        ))}
        {p.secrets?.map((s) => (
          <li key={`s-${s}`}>
            secret <code>{s}</code> <span className="hint">(the name; the value is never handed over)</span>
          </li>
        ))}
        {p.api?.map((a) => (
          <li key={`a-${a}`}>
            api <code>{a}</code>
          </li>
        ))}
      </ul>
    </div>
  )
}
