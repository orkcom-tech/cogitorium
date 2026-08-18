import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type MCPCatalogEntry, type MCPServer, type MCPTool, type MCPTransport, type User } from '../api'
import { PanelTitle } from '../deck/Drawer'
import { dragging } from '../dnd'

/**
 * External MCP servers: somebody else's tools, granted to an agent the way a
 * gear is.
 *
 * THE WHOLE REASON THIS SCREEN IS NOT A PLUGIN STORE. An MCP server is worse
 * than a gear on every axis, and this has to say so rather than making it feel
 * like installing an extension:
 *
 *   It is not sandboxed. A gear runs in a container with no network unless
 *   granted, no server files and a timeout. An MCP server is a subprocess of
 *   this server, spawned with a command line, holding whatever access this
 *   account holds — including the database and the provider keys in it.
 *
 *   It reaches out by definition. The point of a Jira server is that it talks
 *   to Jira. There is no no-network version, so the gate that covers web search
 *   does not cover this at all.
 *
 *   It is somebody else's code, fetched at spawn. `npx some-server` is a
 *   download from a registry every time it starts. The thing approved on
 *   Tuesday is not necessarily the thing that runs on Friday, and no hash over
 *   a command line catches that.
 *
 *   It is handed real credentials, belonging to a real person's account.
 *
 * So the approval gate here is the gear's, with harder wording — not softer.
 * And the tools are approved ONE AT A TIME, because a server's tool list is its
 * own claim about itself and can change between spawns: granting Jira should
 * not have to mean granting delete_issue.
 */
export default function McpPage({ me }: { me: User }) {
  const [servers, setServers] = useState<MCPServer[]>([])
  const [tools, setTools] = useState<Record<number, MCPTool[]>>({})
  const [openId, setOpenId] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [browsing, setBrowsing] = useState(false)
  const [byHand, setByHand] = useState(false)
  const [off, setOff] = useState(false)

  const admin = me.role === 'admin'

  const reload = useCallback(
    () =>
      api.mcp
        .servers()
        .then((s) => {
          setServers(s)
          setOff(false)
        })
        .catch((e: Error) => {
          // 404 is the capability being switched off, which is a state to
          // explain rather than an error to report.
          if (e.message.includes('mcp_clients')) {
            setOff(true)
            return
          }
          setError(e.message)
        }),
    [],
  )

  useEffect(() => {
    void reload()
  }, [reload])

  const loadTools = useCallback((id: number) => {
    api.mcp
      .tools(id)
      .then((t) => setTools((prev) => ({ ...prev, [id]: t })))
      .catch((e: Error) => setError(e.message))
  }, [])

  const open = (id: number) => {
    const next = openId === id ? null : id
    setOpenId(next)
    if (next !== null && !tools[next]) loadTools(next)
  }

  const run = (p: Promise<unknown>, after?: () => void) => {
    setBusy(true)
    setError(null)
    p.then(() => {
      after?.()
      return reload()
    })
      .catch((e: Error) => setError(e.message))
      .finally(() => setBusy(false))
  }

  if (off) {
    return (
      <div className="page">
        <PanelTitle>MCP servers</PanelTitle>
        <p className="hint">
          External MCP servers are switched off for this install. They are off unless asked for, and the default
          is the point: everything else this product runs is either its own code or a gear whose complete source
          is here, versioned, approved line by line and run in a container. An MCP server is a command — the
          source is never seen, and the child runs on this host as this server’s user.
        </p>
        <p className="hint">
          Set <code>mcp_clients: true</code> in the configuration and restart.
        </p>
      </div>
    )
  }

  return (
    <div className="page mcp-page">
      <PanelTitle>MCP servers</PanelTitle>

      {admin && (
        <div className="row">
          <button className="primary" onClick={() => { setBrowsing((v) => !v); setByHand(false) }}>
            {browsing ? 'cancel' : 'add from the library'}
          </button>
          {/* The library is the short path, not the only one. The interesting
              MCP server is very often internal — somebody's own, on their own
              machine — and a screen that could only install from a list would
              make the common case the one you need curl for. */}
          <button onClick={() => { setByHand((v) => !v); setBrowsing(false) }}>
            {byHand ? 'cancel' : 'add by hand'}
          </button>
        </div>
      )}

      {browsing && (
        <Library
          onPick={(e) =>
            run(
              api.mcp.install({
                name: e.name,
                description: e.title,
                transport: e.transport,
                // One half or the other; the server refuses a row wearing both.
                ...(e.transport === 'stdio'
                  ? { command: e.command, args: e.args, env_names: e.env_names ?? [] }
                  : { url: e.url, header_names: e.header_names ?? {} }),
              }),
              () => {
                setBrowsing(false)
                setNotice(
                  `${e.name} is installed and PENDING — it does nothing yet. Read what it will run, probe it to see what it offers, then approve the server and each tool you actually want.`,
                )
              },
            )
          }
          onError={setError}
        />
      )}

      {byHand && (
        <ServerForm
          submitLabel="install it"
          onSubmit={(body) =>
            run(api.mcp.install(body), () => {
              setByHand(false)
              setNotice(
                `${body.name} is installed and PENDING — it does nothing yet. Probe it to see what it offers, then approve the server and each tool you actually want.`,
              )
            })
          }
          busy={busy}
        />
      )}

      {notice && (
        <div className="card mcp-notice">
          <strong>{notice}</strong>
          <button onClick={() => setNotice(null)}>dismiss</button>
        </div>
      )}
      {error && <p className="error">{error}</p>}

      {servers.length === 0 && (
        <p className="hint">
          Nothing installed. An MCP server is somebody else’s tools, made callable by an agent you grant it to —
          your issue tracker, your documents, your database.
        </p>
      )}

      <div className="mcp-cards">
        {servers.map((s) => (
          <ServerCard
            key={s.id}
            server={s}
            tools={tools[s.id] ?? []}
            open={openId === s.id}
            admin={admin}
            busy={busy}
            onOpen={() => open(s.id)}
            onProbe={() =>
              run(api.mcp.probe(s.id), () => {
                loadTools(s.id)
                setNotice(
                  `${s.name} was started once and asked what it offers. Every tool it reported is UNAPPROVED, including any it had before — a server that grows a tool after approval has grown an inert one.`,
                )
              })
            }
            onStatus={(status) => run(api.mcp.setStatus(s.id, status))}
            onSignIn={() =>
              api.mcp
                .signIn(s.id)
                .then((r) => {
                  // A new tab rather than a redirect: the operator is in the
                  // middle of configuring this and should come back to it.
                  window.open(r.authorize_url, '_blank', 'noopener')
                  setNotice(
                    `Sent you to ${r.issuer} to sign in${
                      r.scopes.length ? ` for ${r.scopes.join(', ')}` : ''
                    }. Come back when it says so — signing in is not approving, and this server is still pending.`,
                  )
                })
                .catch((e: Error) => setError(e.message))
            }
            onEdit={(body) =>
              run(api.mcp.edit(s.id, body), () =>
                setNotice(
                  `${s.name} was changed, so it is PENDING again — everything editable here is inside what was approved, and approving what you just changed is approving something you have not seen.`,
                ),
              )
            }
            onTool={(toolId, approved) => run(api.mcp.approveTool(toolId, approved), () => loadTools(s.id))}
            onDelete={() => {
              if (!confirm(`Delete ${s.name}? Its tools and every grant of it go too.`)) return
              run(api.mcp.remove(s.id), () => setOpenId(null))
            }}
          />
        ))}
      </div>
    </div>
  )
}

function ServerCard({
  server,
  tools,
  open,
  admin,
  busy,
  onOpen,
  onProbe,
  onStatus,
  onSignIn,
  onEdit,
  onTool,
  onDelete,
}: {
  server: MCPServer
  tools: MCPTool[]
  open: boolean
  admin: boolean
  busy: boolean
  onOpen: () => void
  onProbe: () => void
  onStatus: (s: MCPServer['status']) => void
  onSignIn: () => void
  onEdit: (body: ServerFields) => void
  onTool: (toolId: number, approved: boolean) => void
  onDelete: () => void
}) {
  const [editing, setEditing] = useState(false)
  const approvedTools = tools.filter((t) => t.approved).length
  return (
    <div className={`card mcp-card mcp-${server.status}`}>
      {/* Draggable for the same reason a gear is: the blueprint is where a
          thing is given to an agent, and dragging is the shortest true sentence
          for "this server, that agent". Not while the card is open — a drag
          started on a control the operator meant to press is a grant nobody
          intended. */}
      <div
        className="mcp-head"
        draggable={!open && admin}
        onDragStart={admin ? dragging({ kind: 'mcp', id: server.id, name: server.name, status: server.status }) : undefined}
      >
        <button className="linkish mcp-name" onClick={onOpen}>
          <strong>🔌 {server.name}</strong>
        </button>
        <span className="pill mcp-kind">{server.transport === 'stdio' ? 'runs here' : 'hosted'}</span>
        <span className={`pill mcp-status-${server.status}`}>{server.status}</span>
      </div>
      <span className="muted">{server.description || 'no description'}</span>
      <span className="muted">
        {tools.length === 0 ? 'nothing probed yet' : `${approvedTools} of ${tools.length} tools approved`}
      </span>

      {open && (
        <div className="mcp-detail">
          {/* THE FOUR FACTS, before anything else on this card. They are the
              difference between this and a plugin manager, and softening them
              would make the screen lie about what is being agreed to. */}
          <div className="card mcp-warning">
            <strong>What approving this grants</strong>
            {server.transport === 'stdio' ? (
              <ul>
                <li>
                  <strong>It is not sandboxed.</strong> It runs on this host as this server’s user, outside the
                  container gears run in — so it can read this install’s database, and the provider keys in it.
                </li>
                <li>
                  <strong>It reaches the network by definition.</strong> That is what it is for, and the gate that
                  covers agent web search does not cover it at all.
                </li>
                <li>
                  <strong>Its code is fetched when it starts</strong>, every time it starts. What you approve is
                  the command line, not the bytes that command will fetch tomorrow.
                </li>
                <li>
                  <strong>It is handed real credentials</strong> — by name, resolved at spawn, belonging to a real
                  account with that account’s permissions.
                </li>
              </ul>
            ) : (
              /* A hosted server is a genuinely different risk, and saying the
                 same four things would be false in two of them. Nothing runs
                 here — which is safer — and what leaves is the part that
                 matters. */
              <ul>
                <li>
                  <strong>Nothing runs on this machine.</strong> There is no process, no file access and nothing
                  to sandbox, which is the one way this is safer than a packaged server.
                </li>
                <li>
                  <strong>Your agents’ words leave this install.</strong> Every argument of every call to it goes
                  to a host somebody else operates, along with whatever the agent wrote to get there.
                </li>
                <li>
                  <strong>It is sent a real credential</strong>, on every request, belonging to a real account
                  with that account’s permissions.
                </li>
                <li>
                  <strong>A URL can be repointed and look identical.</strong> That is why the address is part of
                  what you approve here: change it and this server goes back to pending.
                </li>
              </ul>
            )}
          </div>

          {admin ? (
            <>
              <span className="field-label">{server.transport === 'stdio' ? 'what runs' : 'what is called'}</span>
              <pre className="mcp-cmd">
                <code>
                  {server.transport === 'stdio'
                    ? `${server.command} ${server.args.join(' ')}`
                    : server.url}
                </code>
              </pre>
              {server.transport === 'stdio' && server.cwd && <span className="muted">in {server.cwd}</span>}
              <span className="field-label">credentials it is given, by name</span>
              <span className="muted">
                {server.transport === 'stdio'
                  ? server.env_names.length === 0
                    ? 'none'
                    : server.env_names.join(', ')
                  : Object.keys(server.header_names ?? {}).length === 0
                    ? 'none'
                    : Object.entries(server.header_names)
                        .map(([h, v]) => `${h}: ${v}`)
                        .join(', ')}
              </span>
            </>
          ) : (
            <p className="hint">
              What this server runs, and which credentials it is handed, are shown to administrators only. You can
              see that it exists and whether it has been approved, which is why an agent here has its tools.
            </p>
          )}

          {admin && editing && (
            <ServerForm
              submitLabel="save it"
              busy={busy}
              initial={{
                name: server.name,
                description: server.description,
                transport: server.transport,
                command: server.command,
                args: server.args,
                cwd: server.cwd,
                env_names: server.env_names,
                url: server.url,
                header_names: server.header_names,
              }}
              onSubmit={(body) => {
                setEditing(false)
                onEdit(body)
              }}
            />
          )}

          {admin && (
            <div className="row">
              <button disabled={busy} onClick={onProbe} title="Start it once and ask what it offers, giving it nothing">
                probe it
              </button>
              {/* Two of the library's own entries ship a placeholder path and
                  say to change it before approving. Without this they could
                  only be corrected with curl, which made the library a list of
                  things you cannot finish installing. */}
              <button disabled={busy} onClick={() => setEditing((v) => !v)}>
                {editing ? 'cancel edit' : 'edit what it runs'}
              </button>
              {/* Only for a hosted server: a child process here takes its
                  credentials from named values, and there is nothing to sign
                  in to. */}
              {server.transport !== 'stdio' && (
                <button
                  disabled={busy}
                  onClick={onSignIn}
                  title="Sign this install in to the server, instead of pasting a token"
                >
                  sign in
                </button>
              )}
              {server.status !== 'approved' && (
                <button className="primary" disabled={busy} onClick={() => onStatus('approved')}>
                  approve the server
                </button>
              )}
              {server.status === 'approved' && (
                <button disabled={busy} onClick={() => onStatus('disabled')}>
                  disable it
                </button>
              )}
              <span className="spacer" />
              <button className="danger" disabled={busy} onClick={onDelete}>
                delete
              </button>
            </div>
          )}

          <span className="field-label">tools</span>
          {tools.length === 0 ? (
            <p className="hint">
              Nothing probed yet. Probing starts the server once, hands it no credentials at all, and records what
              it says it offers — which is the server’s own claim about itself, and the only thing you have instead
              of source to read.
            </p>
          ) : (
            <ul className="mcp-tools">
              {tools.map((t) => (
                <li key={t.id} className={t.approved ? 'on' : ''}>
                  <label>
                    <input
                      type="checkbox"
                      checked={t.approved}
                      disabled={busy || !admin}
                      onChange={(e) => onTool(t.id, e.target.checked)}
                    />
                    <code>{t.remote_name}</code>
                  </label>
                  <span className="muted">{t.description || 'no description'}</span>
                  <span className="muted mcp-offered">offered to the model as {t.offered_name}</span>
                </li>
              ))}
            </ul>
          )}
          {tools.length > 0 && (
            <p className="hint">
              One at a time, on purpose: a server’s tool list is its own account of itself and can change between
              spawns. Granting your issue tracker should not have to mean granting <code>delete_issue</code>.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

/**
 * The library: adding a server by choosing it rather than by knowing that its
 * server is an npm package, what its binary is called, which arguments it takes
 * and which environment variables it reads.
 *
 * It is a static list compiled into the binary, so nothing is fetched to show
 * it, it works offline, and it cannot change under an install between the day
 * it was reviewed and the day somebody installs from it. Picking one fills in
 * the form; it does not skip a single gate.
 */
function Library({ onPick, onError }: { onPick: (e: MCPCatalogEntry) => void; onError: (m: string) => void }) {
  const [entries, setEntries] = useState<MCPCatalogEntry[]>([])
  const [warning, setWarning] = useState('')
  const [q, setQ] = useState('')
  const seq = useRef(0)

  useEffect(() => {
    const mine = ++seq.current
    api.mcp
      .library(q)
      .then((r) => {
        if (seq.current !== mine) return
        setEntries(r.entries ?? [])
        setWarning(r.fetched_at_spawn)
      })
      .catch((e: Error) => {
        if (seq.current === mine) onError(e.message)
      })
  }, [q, onError])

  return (
    <div className="card mcp-library">
      <input
        autoFocus
        aria-label="Search the library"
        placeholder="your issues, your documents, your database…"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <p className="hint">{warning}</p>
      {entries.length === 0 && <p className="hint">Nothing here matches. Anything else installs by name.</p>}
      {entries.map((e) => (
        <div key={e.id} className="mcp-entry">
          <div className="row">
            <strong>{e.title}</strong>
            <span className="spacer" />
            <button onClick={() => onPick(e)}>add it</button>
          </div>
          <span className="muted">
            <span className="pill mcp-kind">{e.transport === 'stdio' ? 'runs here' : 'hosted'}</span>{' '}
            {e.reaches}
          </span>
          <pre className="mcp-cmd">
            <code>{e.transport === 'stdio' ? `${e.command} ${e.args.join(' ')}` : e.url}</code>
          </pre>
          {/* Stated rather than discovered: most of these are an npx or a uvx
              away, which means node or python on the machine, and an entry that
              does not say so produces a spawn failure nobody can read. */}
          <span className="muted">needs: {e.needs}</span>
          <a href={e.docs} target="_blank" rel="noreferrer">
            what its arguments mean
          </a>
        </div>
      ))}
    </div>
  )
}

/** What an operator types to install or correct a server. */
export type ServerFields = {
  name: string
  description?: string
  transport: MCPTransport
  command?: string
  args?: string[]
  cwd?: string
  env_names?: string[]
  url?: string
  header_names?: Record<string, string>
}

/**
 * The form behind both "add by hand" and "edit what it runs".
 *
 * ONE COMPONENT FOR BOTH, because they are the same fields and the same rules,
 * and two copies would be two places for the argument splitting to drift. The
 * only difference is the label on the button and whether it starts filled.
 *
 * ARGUMENTS ARE ONE PER LINE, not a space-separated string. A single string
 * means somebody eventually splits it, and splitting means quoting, and quoting
 * means an argument containing a space becomes two arguments with no error
 * anywhere — which is the exact reason the database stores them as a JSON array
 * and refuses to hold a command line.
 */
function ServerForm({
  initial,
  submitLabel,
  busy,
  onSubmit,
}: {
  initial?: ServerFields
  submitLabel: string
  busy: boolean
  onSubmit: (body: ServerFields) => void
}) {
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [transport, setTransport] = useState<MCPTransport>(initial?.transport ?? 'stdio')
  const [command, setCommand] = useState(initial?.command ?? '')
  const [args, setArgs] = useState((initial?.args ?? []).join('\n'))
  const [cwd, setCwd] = useState(initial?.cwd ?? '')
  const [envNames, setEnvNames] = useState((initial?.env_names ?? []).join('\n'))
  const [url, setUrl] = useState(initial?.url ?? '')
  const [headers, setHeaders] = useState(
    Object.entries(initial?.header_names ?? {})
      .map(([h, v]) => `${h}: ${v}`)
      .join('\n'),
  )

  // The store's own rule, checked here so an operator learns it while typing
  // rather than from a 400 after filling the rest in.
  const editingExisting = initial !== undefined
  const badName = name !== '' && !/^[a-z0-9][a-z0-9_-]{0,39}$/.test(name)
  const remote = transport !== 'stdio'
  // Cleartext to anywhere but this machine would send the credential and
  // everything the agent says in clear, and the server refuses it. Said here so
  // it is not a 400 after filling the form in.
  const badURL =
    remote && url !== '' && !/^https:\/\//.test(url) && !/^http:\/\/(localhost|127\.0\.0\.1)([:/]|$)/.test(url)

  const lines = (v: string) =>
    v
      .split('\n')
      .map((x) => x.trim())
      .filter(Boolean)

  // `Authorization: JIRA_TOKEN` per line — a header and the NAME of the value
  // it should be given, never the value.
  const headerMap = () => {
    const out: Record<string, string> = {}
    for (const line of lines(headers)) {
      const at = line.indexOf(':')
      if (at <= 0) continue
      const header = line.slice(0, at).trim()
      const named = line.slice(at + 1).trim()
      if (header && named) out[header] = named
    }
    return out
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    onSubmit({
      name: name.trim(),
      description: description.trim(),
      transport,
      ...(remote
        ? { url: url.trim(), header_names: headerMap() }
        : { command: command.trim(), args: lines(args), cwd: cwd.trim(), env_names: lines(envNames) }),
    })
  }

  const ready = name.trim() !== '' && !badName && (remote ? url.trim() !== '' && !badURL : command.trim() !== '')

  return (
    <form className="card mcp-form" onSubmit={submit}>
      {!editingExisting && (
        <label className="field">
          <span className="muted">name — lowercase, digits, dash or underscore</span>
          <input required autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="jira" />
        </label>
      )}
      {badName && (
        <span className="hint danger">
          a name may hold lowercase letters, digits, dash and underscore, and must start with a letter or digit
        </span>
      )}

      <label className="field">
        <span className="muted">what it reaches, in a sentence</span>
        <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="our tickets" />
      </label>

      {/* THE CHOICE THAT DECIDES WHAT IS BEING AGREED TO, so it is a control
          rather than something inferred from which field was filled in. */}
      <span className="field-label">how it is reached</span>
      <div className="mode-row">
        {(
          [
            ['stdio', 'a command here'],
            ['streamable-http', 'a URL'],
            ['sse', 'a URL (old)'],
          ] as const
        ).map(([id, label]) => (
          <button
            key={id}
            type="button"
            data-own
            className={`mode-option ${transport === id ? 'on' : ''}`}
            aria-pressed={transport === id}
            onClick={() => setTransport(id)}
          >
            {label}
          </button>
        ))}
      </div>
      <span className="hint">
        {remote ? (
          <>
            <strong>Nothing runs on this machine.</strong> What leaves instead is the credential below and
            whatever your agents say to it, over https to a host you name. Pick the old shape only if the server
            told you to — it is deprecated, and its endpoint is chosen by the server rather than by you.
          </>
        ) : (
          <>
            <strong>This runs a process here</strong>, as this server’s user, outside the container gears run in.
            It can read this install’s database and the provider keys in it.
          </>
        )}
      </span>

      {remote ? (
        <>
          <label className="field">
            <span className="muted">endpoint — https, or http on this machine</span>
            <input
              required
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://mcp.example.com/mcp"
            />
          </label>
          {badURL && (
            <span className="hint danger">
              a remote server must be https: its headers carry a credential and its body carries what your agents
              say
            </span>
          )}
          <label className="field">
            <span className="muted">headers it is given — one per line, as `Header: NAME_OF_VALUE`</span>
            <textarea
              rows={2}
              value={headers}
              onChange={(e) => setHeaders(e.target.value)}
              placeholder={'Authorization: JIRA_TOKEN'}
            />
          </label>
        </>
      ) : (
        <>
          <label className="field">
            <span className="muted">command — the executable alone, without its arguments</span>
            <input required value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx" />
          </label>
          <label className="field">
            <span className="muted">arguments, one per line</span>
            <textarea
              rows={4}
              value={args}
              onChange={(e) => setArgs(e.target.value)}
              placeholder={'-y\n@acme/jira-mcp\n--site\nacme.atlassian.net'}
            />
          </label>
          <label className="field">
            <span className="muted">working directory — blank for this server’s own</span>
            <input value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/srv/shared" />
          </label>
          <label className="field">
            <span className="muted">credentials it is given, by NAME, one per line</span>
            <textarea
              rows={2}
              value={envNames}
              onChange={(e) => setEnvNames(e.target.value)}
              placeholder="JIRA_TOKEN"
            />
          </label>
        </>
      )}
      <span className="hint">
        Names, never values. Each is resolved when this server is contacted, from this install’s variables and
        secrets, exactly as a gear’s are — so the value never reaches a prompt, a schema or this screen. Set them
        under Variables.
      </span>

      <div className="row spread">
        <span className="hint">
          {editingExisting
            ? 'Saving returns this server to pending: everything here is inside what was approved.'
            : 'It arrives pending. Installing is not approving.'}
        </span>
        <button className="primary" type="submit" disabled={busy || !ready}>
          {submitLabel}
        </button>
      </div>
    </form>
  )
}
