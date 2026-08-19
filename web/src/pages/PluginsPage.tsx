import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type CatalogEntry, type CatalogListing, type Plugin, type PluginAction } from '../api'

/** The library.
 *
 * The screen is built around one distinction the server draws and nothing else
 * on this install draws: enabled and live are different questions. A plugin
 * can be switched on and still not be rendering, because its templates could
 * not run against this version — and the gap between those two states is the
 * reason somebody opens this page. So the state badge never says "enabled" on
 * its own; it says which of the two it means.
 *
 * Everything under the name comes from the server having composed the
 * templates, not from the manifest. A plugin cannot look better here by
 * claiming less.
 */

/** Not a plugin id: ids are lowercase, so this cannot collide with one. */
const RESTART = '\u0000restart'

type Sort = 'state' | 'name' | 'order'
type View = 'installed' | 'library'
type CatalogSort = 'name' | 'author' | 'verified'

export default function PluginsPage() {
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  /** Sticky once true. A restart is owed until it happens, and an operator who
   *  enables three plugins should not watch the reminder disappear because the
   *  last of the three happened to change nothing. */
  const [restartOwed, setRestartOwed] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<Sort>('state')
  const [view, setView] = useState<View>('installed')
  const [catalog, setCatalog] = useState<CatalogListing | null>(null)
  const [catalogError, setCatalogError] = useState<string | null>(null)
  const [catalogQuery, setCatalogQuery] = useState('')
  const [catalogSort, setCatalogSort] = useState<CatalogSort>('name')
  const reloadSeq = useRef(0)
  const catalogSeq = useRef(0)

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

  // Fetched when the library is opened, not on page load: browsing costs a
  // request to somebody else's server, and an operator who came here to switch
  // a plugin off should not have made one.
  useEffect(() => {
    if (view !== 'library') return
    const seq = ++catalogSeq.current
    const t = setTimeout(() => {
      api.catalog
        .browse(catalogQuery)
        .then((c) => {
          if (catalogSeq.current !== seq) return
          setCatalog(c)
          setCatalogError(null)
        })
        .catch((e: Error) => {
          if (catalogSeq.current === seq) setCatalogError(e.message)
        })
    }, catalogQuery ? 250 : 0)
    return () => clearTimeout(t)
  }, [view, catalogQuery, plugins])

  const act = useCallback(
    (id: string, run: () => Promise<PluginAction>) => {
      setBusy(id)
      run()
        .then((res) => {
          setNotice(res.message)
          setError(null)
          if (res.restart_required) setRestartOwed(true)
          reload()
        })
        .catch((e: Error) => setError(e.message))
        .finally(() => setBusy(null))
    },
    [reload],
  )

  /** Restarting is not a plugin, so it borrows the busy slot under a name no
   *  plugin can have — ids are lowercase and this is not. */
  const restart = useCallback(() => {
    setBusy(RESTART)
    api
      .restart()
      .then((res) => {
        setNotice(res.message)
        setError(null)
        // The server is replacing itself, so this poll is waiting for a
        // different process to answer on the same port. Reloading immediately
        // would show a connection error for the second it takes.
        const until = Date.now() + 30_000
        const poll = () => {
          api.plugins
            .list()
            .then(() => window.location.reload())
            .catch(() => {
              if (Date.now() < until) setTimeout(poll, 500)
              else {
                setBusy(null)
                setError('It has not come back. Check the terminal it was started from.')
              }
            })
        }
        setTimeout(poll, 1000)
      })
      .catch((e: Error) => {
        setError(e.message)
        setBusy(null)
      })
  }, [])

  const shown = useMemo(() => filterAndSort(plugins, query, sort), [plugins, query, sort])

  return (
    <div className="page">
      <h2>Plugins</h2>

      {restartOwed && (
        <p className="banner">
          Restart Cogitorium to apply your changes — what is running has not changed yet.{' '}
          <button className="banner-act" disabled={busy === RESTART} onClick={restart}>
            {busy === RESTART ? 'Restarting…' : 'Restart now'}
          </button>
        </p>
      )}
      {error && <p className="error">{error}</p>}
      {notice && !error && <p className="hint">{notice}</p>}

      {/* Two views rather than two routes. What is installed and what could be
          are the same question asked twice, and making somebody navigate
          between them is how the second half goes unread. */}
      <div className="plugin-views" role="tablist">
        <button role="tab" aria-selected={view === 'installed'}
                className={view === 'installed' ? 'is-current' : ''}
                onClick={() => setView('installed')}>
          Installed{plugins.length > 0 ? ` (${plugins.length})` : ''}
        </button>
        <button role="tab" aria-selected={view === 'library'}
                className={view === 'library' ? 'is-current' : ''}
                onClick={() => setView('library')}>
          Library
          {catalog && catalog.updates.length > 0 && (
            <span className="badge is-warn">{catalog.updates.length} to update</span>
          )}
        </button>
      </div>

      {view === 'library' ? (
        <Library
          listing={catalog}
          error={catalogError}
          query={catalogQuery}
          onQuery={setCatalogQuery}
          sort={catalogSort}
          onSort={setCatalogSort}
          busy={busy}
          onInstall={(id) => act(id, () => api.catalog.install(id))}
        />
      ) : (
      <>
      <Upload
        onDone={(res) => {
          setNotice(res.message)
          setError(null)
          if (res.restart_required) setRestartOwed(true)
          reload()
        }}
        onError={setError}
      />

      {plugins.length > 1 && (
        <div className="plugin-filters">
          <input
            type="search"
            placeholder="Search by name, id, or a template it overrides"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            aria-label="Search plugins"
          />
          <label>
            Sort
            <select value={sort} onChange={(e) => setSort(e.target.value as Sort)}>
              {/* State first by default: the reason to open this page is
                  usually that something is wrong, so what is wrong sorts to
                  the top rather than being alphabetically buried. */}
              <option value="state">Needs attention first</option>
              <option value="order">Layer order</option>
              <option value="name">Name</option>
            </select>
          </label>
        </div>
      )}

      {plugins.length === 0 && !error && (
        <p className="hint">
          Nothing installed. A plugin is a folder with a <code>plugin.yaml</code> and a{' '}
          <code>templates/</code> directory — drop its zip above, or run{' '}
          <code>cogitorium plugins install &lt;bundle.zip&gt;</code>.
        </p>
      )}

      {plugins.length > 0 && shown.length === 0 && (
        <p className="hint">Nothing matches “{query}”.</p>
      )}

      {shown.map((p) => (
        <PluginCard
          key={p.id}
          plugin={p}
          busy={busy === p.id}
          onApprove={() => act(p.id, () => api.plugins.approve(p.id))}
          onRevoke={() => act(p.id, () => api.plugins.revoke(p.id))}
          onEnable={() => act(p.id, () => api.plugins.enable(p.id))}
          onDisable={() => act(p.id, () => api.plugins.disable(p.id))}
          onRemove={() => act(p.id, () => api.plugins.remove(p.id))}
          onMove={(dir) => act(p.id, () => api.plugins.order(moved(plugins, p.id, dir)))}
        />
      ))}
      </>
      )}
    </div>
  )
}

/** Search covers what a plugin DOES, not only what it is called.
 *
 * "who overrode my gear row" is the question somebody actually arrives with,
 * and a name-only search cannot answer it. */
function filterAndSort(plugins: Plugin[], query: string, sort: Sort): Plugin[] {
  const q = query.trim().toLowerCase()
  const matched = q
    ? plugins.filter((p) =>
        [
          p.id,
          p.name,
          ...(p.overrides ?? []),
          ...(p.adds ?? []),
          ...(p.extends ?? []),
          ...(p.pages ?? []).map((pg) => pg.path),
        ]
          .join(' ')
          .toLowerCase()
          .includes(q),
      )
    : plugins.slice()

  return matched.sort((a, b) => {
    if (sort === 'name') return (a.name || a.id).localeCompare(b.name || b.id)
    if (sort === 'order') {
      // Disabled plugins have no position, so they follow the ordered ones
      // rather than crowding the front with zeroes.
      const ao = a.order || Number.MAX_SAFE_INTEGER
      const bo = b.order || Number.MAX_SAFE_INTEGER
      return ao - bo || (a.name || a.id).localeCompare(b.name || b.id)
    }
    return attention(a) - attention(b) || (a.name || a.id).localeCompare(b.name || b.id)
  })
}

/** Lower sorts first. Broken and on-but-not-loading are what somebody came for. */
function attention(p: Plugin): number {
  if (p.problem) return 0
  if (!p.available) return 1
  if (p.enabled && !p.live) return 0
  // Above an inert override and below a fault: it is the only state on this
  // screen that cannot resolve on its own, so it should not sort under the
  // plugins that are quietly working.
  if (p.pending) return 2
  if ((p.inert?.length ?? 0) > 0) return 2
  if (p.enabled) return 3
  return 4
}

/** moved returns the whole enable list with one plugin shifted by a place.
 *  The server takes the list rather than a delta, so precedence is never
 *  half-applied. */
function moved(plugins: Plugin[], id: string, dir: -1 | 1): string[] {
  const order = plugins
    .filter((p) => p.enabled)
    .sort((a, b) => a.order - b.order)
    .map((p) => p.id)
  const i = order.indexOf(id)
  const j = i + dir
  if (i < 0 || j < 0 || j >= order.length) return order
  const out = order.slice()
  ;[out[i], out[j]] = [out[j], out[i]]
  return out
}

function Upload({
  onDone,
  onError,
}: {
  onDone: (res: PluginAction) => void
  onError: (msg: string) => void
}) {
  const [over, setOver] = useState(false)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  const send = (file: File | undefined) => {
    if (!file) return
    setBusy(true)
    api.plugins
      .upload(file)
      .then(onDone)
      .catch((e: Error) => onError(e.message))
      .finally(() => {
        setBusy(false)
        if (input.current) input.current.value = ''
      })
  }

  return (
    <div
      className={`plugin-drop${over ? ' is-over' : ''}`}
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault()
        setOver(false)
        send(e.dataTransfer.files[0])
      }}
    >
      <input
        ref={input}
        type="file"
        accept=".zip,application/zip"
        onChange={(e) => send(e.target.files?.[0])}
        disabled={busy}
      />
      <span className="hint">
        {busy ? 'Installing…' : 'Drop a plugin bundle here, or choose one. It arrives switched off.'}
      </span>
    </div>
  )
}

function PluginCard({
  plugin: p,
  busy,
  onApprove,
  onRevoke,
  onEnable,
  onDisable,
  onRemove,
  onMove,
}: {
  plugin: Plugin
  busy: boolean
  onApprove: () => void
  onRevoke: () => void
  onEnable: () => void
  onDisable: () => void
  onRemove: () => void
  onMove: (dir: -1 | 1) => void
}) {
  return (
    <section className="card">
      <header className="plugin-head">
        <h3>
          {p.name || p.id} <span className="hint">{p.version}</span>
        </h3>
        <State plugin={p} />
      </header>

      {/* When it is off, this is history rather than a current fault — and it
          is exactly what somebody needs to read before switching it back on. */}
      {p.problem && (
        <p className="error">
          {p.readable && !p.enabled ? 'Last time it was enabled: ' : ''}
          {p.problem}
        </p>
      )}
      {!p.available && p.refusal && <p className="error">{p.refusal}</p>}

      {/* Stated before the buttons rather than after them, because everything
          between here and Approve — what it overrides, which hosts it wants,
          which secrets — is the thing being agreed to. */}
      {p.pending && <p className="hint">{p.pending}</p>}
      {p.approved_by && (
        <p className="hint">
          Approved by {p.approved_by}
          {p.approved_at ? ` on ${new Date(p.approved_at).toLocaleDateString()}` : ''} — for this
          exact build. Rebuild it and it comes back here.
        </p>
      )}

      {p.enabled && (
        <p className="hint">
          Layer {p.order} — a plugin later in the order renders instead of an earlier one when both
          define the same name.
        </p>
      )}

      <Names label="Overrides" names={p.overrides} />
      <Names label="Adds" names={p.adds} />
      <Names label="Extends" names={p.extends} />
      <Names label="Overridden without declaring" names={p.undeclared} tone="warn" />
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

      <div className="plugin-actions">
        {/* An unreadable directory has no manifest anybody could trust, so the
            only thing offered is taking it away. Everything else can be
            switched, including something that failed to load — refusing that
            would strand it off with no way back. */}
        {p.readable && p.pending ? (
          <button onClick={onApprove} disabled={busy}>
            Approve
          </button>
        ) : null}
        {p.readable && !p.pending ? (
          p.enabled ? (
            <>
              <button onClick={onDisable} disabled={busy}>
                Disable
              </button>
              <button onClick={() => onMove(-1)} disabled={busy || p.order <= 1} title="Earlier layer">
                ↑
              </button>
              <button onClick={() => onMove(1)} disabled={busy} title="Later layer">
                ↓
              </button>
            </>
          ) : (
            <button onClick={onEnable} disabled={busy}>
              Enable
            </button>
          )
        ) : null}
        {p.readable && p.approved_by && !p.pending && (
          <button onClick={onRevoke} disabled={busy} title="Withdraw the approval and switch it off">
            Withdraw approval
          </button>
        )}
        <button className="danger" onClick={onRemove} disabled={busy}>
          Remove
        </button>
        {(p.docs || p.source) && (
          <span className="hint">
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
          </span>
        )}
      </div>
    </section>
  )
}

function State({ plugin: p }: { plugin: Plugin }) {
  // Unreadable and switched-off are different states and used to render the
  // same, which left a working plugin labelled broken and with no way back on.
  if (!p.readable) return <span className="badge is-danger">unreadable</span>
  // Not approved is not the same as off. Off is a choice somebody made; this
  // is a decision nobody has made yet, and the card offers different buttons
  // for the two, so the badge has to tell them apart.
  if (p.pending) return <span className="badge is-warn">needs approval</span>
  if (!p.enabled) return <span className="badge">off</span>
  if (!p.live) return <span className="badge is-danger">on, not loading</span>
  return <span className="badge is-ok">live</span>
}

function Names({ label, names, tone }: { label: string; names?: string[]; tone?: 'warn' }) {
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
            secret <code>{s}</code>{' '}
            <span className="hint">(the name; the value is never handed over)</span>
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

/** The library: what the shared catalog lists, and what it says about it.
 *
 * The catalog is an index and nothing else — it says where a plugin lives, and
 * never that it is safe. Installing from here does exactly what dropping a zip
 * above does: the plugin arrives switched off and unapproved, and somebody
 * still has to read it. There is deliberately no "install and enable": being
 * listed is not a decision anybody on this install made.
 */
function Library({
  listing,
  error,
  query,
  onQuery,
  sort,
  onSort,
  busy,
  onInstall,
}: {
  listing: CatalogListing | null
  error: string | null
  query: string
  onQuery: (q: string) => void
  sort: CatalogSort
  onSort: (s: CatalogSort) => void
  busy: string | null
  onInstall: (id: string) => void
}) {
  const entries = useMemo(() => {
    if (!listing) return []
    const rank = (v: string) => (v === 'verified' ? 0 : v === 'verified-other-version' ? 1 : 2)
    return listing.entries.slice().sort((a, b) => {
      if (sort === 'author') return a.author.localeCompare(b.author) || a.name.localeCompare(b.name)
      if (sort === 'verified') return rank(a.verified) - rank(b.verified) || a.name.localeCompare(b.name)
      return a.name.localeCompare(b.name)
    })
  }, [listing, sort])

  if (error) {
    return (
      <>
        <p className="error">{error}</p>
        <p className="hint">
          The catalog is fetched over the network and this install could not reach it. Nothing is
          wrong with what you have installed — this list is the only thing missing.
        </p>
      </>
    )
  }
  if (!listing) return <p className="hint">Reading the catalog…</p>

  return (
    <>
      {/* A cached list is not a current one, and presenting yesterday's as
          today's is how somebody installs a version withdrawn yesterday. */}
      {listing.cached && (
        <p className="banner">
          This is a cached copy{listing.fetched ? ` from ${new Date(listing.fetched).toLocaleString()}` : ''} — the
          catalog could not be reached just now.
        </p>
      )}

      {listing.updates.length > 0 && (
        <p className="hint">
          Newer versions are listed for{' '}
          {listing.updates.map((u) => `${u.name} (${u.installed} → ${u.available})`).join(', ')}.
        </p>
      )}
      {/* Said out loud rather than shown as "everything is current": without a
          published index there are no versions to compare, and an empty update
          list would otherwise resolve the flattering way. */}
      {!listing.versioned && listing.entries.length > 0 && (
        <p className="hint">
          The catalog has not published versions yet, so this install cannot tell whether anything
          you have is out of date.
        </p>
      )}

      <div className="plugin-filters">
        <input
          type="search"
          placeholder="Search the catalog"
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          aria-label="Search the catalog"
        />
        <label>
          Sort
          <select value={sort} onChange={(e) => onSort(e.target.value as CatalogSort)}>
            <option value="name">Name</option>
            <option value="author">Author</option>
            <option value="verified">Read by the team first</option>
          </select>
        </label>
      </div>

      {entries.length === 0 && (
        <p className="hint">
          {query
            ? `Nothing in the catalog matches “${query}”.`
            : 'The catalog lists nothing yet.'}
        </p>
      )}

      {entries.map((e) => (
        <section className="card" key={e.id}>
          <header className="plugin-head">
            <h3>
              {e.name} <span className="hint">{e.version || ''}</span>
            </h3>
            <VerifiedBadge entry={e} />
          </header>

          <p>{e.description}</p>
          <p className="hint">
            by {e.author} ·{' '}
            <a href={e.source} target="_blank" rel="noreferrer noopener">
              Read the code
            </a>
          </p>

          {e.verified !== 'unchecked' && e.verified_note && (
            <p className="hint">
              {e.verified_by ? `${e.verified_by}: ` : ''}
              {e.verified_note}
            </p>
          )}

          <div className="plugin-actions">
            {e.installed ? (
              e.update ? (
                <button onClick={() => onInstall(e.id)} disabled={busy === e.id}>
                  Update to {e.version}
                </button>
              ) : (
                <span className="hint">Installed{e.installed_version ? ` — ${e.installed_version}` : ''}</span>
              )
            ) : (
              <button onClick={() => onInstall(e.id)} disabled={busy === e.id}>
                Install
              </button>
            )}
            <span className="hint">
              Arrives switched off. You approve it after reading it, like anything else.
            </span>
          </div>
        </section>
      ))}
    </>
  )
}

/** Three states, never a tick.
 *
 * A badge that survives a version change is a badge about a name rather than
 * about code, so "we read 1.2.0" beside an installed 1.4.0 says so instead of
 * carrying the old reassurance forward. */
function VerifiedBadge({ entry: e }: { entry: CatalogEntry }) {
  if (e.verified === 'verified') return <span className="badge is-ok">read by the team</span>
  if (e.verified === 'verified-other-version') {
    return (
      <span className="badge is-warn" title={`The team read ${e.verified_version}, not what you have`}>
        read at {e.verified_version}
      </span>
    )
  }
  // Not an accusation, and worded so nobody reads it as one. Most plugins in
  // any healthy catalog will sit here forever.
  return <span className="badge">nobody has looked</span>
}
