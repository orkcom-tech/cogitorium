import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import BlueprintEditor from './BlueprintEditor'
import TerminalPage from './TerminalPage'
import AgentMemory from './AgentMemory'
import {
  api,
  wsChatStream,
  type Agent,
  type AgentStatus,
  type ContextBinding,
  type ContextFile,
  type Gear,
  type GearBinding,
  type Model,
  type WSMessage,
  type Workspace,
} from '../api'

export default function WorkspacePage() {
  const { id } = useParams()
  const wsId = Number(id)

  const [workspace, setWorkspace] = useState<Workspace | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [messages, setMessages] = useState<WSMessage[]>([])
  const [statuses, setStatuses] = useState<Map<number, AgentStatus>>(new Map())
  const [streams, setStreams] = useState<Map<number, string>>(new Map())
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null)
  const [view, setView] = useState<'chat' | 'blueprint' | 'terminal'>('chat')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  // Fresh agent ids for stream callbacks — a stale closure over `agents`
  // would refetch on every status event of a newly created agent.
  const agentIdsRef = useRef<Set<number>>(new Set())

  const reloadAgents = useCallback(
    () =>
      api.workspaces
        .agents(wsId)
        .then((a) => {
          agentIdsRef.current = new Set(a.map((x) => x.id))
          setAgents(a)
        })
        .catch((e: Error) => setError(e.message)),
    [wsId],
  )

  useEffect(() => {
    Promise.all([
      api.workspaces.get(wsId),
      api.workspaces.agents(wsId),
      api.models.list(),
      api.workspaces.messages(wsId),
      api.workspaces.status(wsId),
    ])
      .then(([w, a, m, msgs, sts]) => {
        setWorkspace(w)
        agentIdsRef.current = new Set(a.map((x) => x.id))
        setAgents(a)
        setModels(m)
        setMessages(msgs)
        setStatuses(new Map(sts.map((s) => [s.agent_id, s])))
      })
      .catch((e: Error) => setError(e.message))
  }, [wsId])

  useEffect(() => () => abortRef.current?.abort(), [])

  const send = async (text: string) => {
    setBusy(true)
    setError(null)
    const ac = new AbortController()
    abortRef.current = ac
    try {
      await wsChatStream(
        wsId,
        text,
        (ev) => {
          if (ev.type === 'message' && ev.message) {
            const msg = ev.message
            setMessages((m) => [...m, msg])
            // A persisted message replaces that agent's live stream buffer.
            if (msg.agent_id != null)
              setStreams((s) => {
                const next = new Map(s)
                next.delete(msg.agent_id!)
                return next
              })
          }
          if (ev.type === 'delta' && ev.agent_id != null) {
            const agentId = ev.agent_id
            setStreams((s) => {
              const next = new Map(s)
              next.set(agentId, (next.get(agentId) ?? '') + (ev.text ?? ''))
              return next
            })
          }
          if (ev.type === 'status' && ev.status) {
            const st = ev.status
            setStatuses((s) => new Map(s).set(st.agent_id, st))
            // An idle agent has finished — its live stream bubble, if any,
            // is stale (a failed delegation never sends a message event).
            if (st.state === 'idle')
              setStreams((s) => {
                if (!s.has(st.agent_id)) return s
                const next = new Map(s)
                next.delete(st.agent_id)
                return next
              })
            // A status for an agent we don't know yet means the orchestrator
            // just created one.
            if (!agentIdsRef.current.has(st.agent_id)) void reloadAgents()
          }
        },
        ac.signal,
      )
    } catch (err) {
      if (!ac.signal.aborted) setError(err instanceof Error ? err.message : String(err))
    } finally {
      abortRef.current = null
      setBusy(false)
      setStreams(new Map())
      void reloadAgents()
      void api.workspaces.status(wsId).then((sts) => setStatuses(new Map(sts.map((s) => [s.agent_id, s]))))
    }
  }

  // The timeline is replayed into the model on every turn, so removing an
  // entry is genuinely forgetting it rather than hiding it.
  const forget = (m: WSMessage) => {
    if (!confirm('Forget this from the conversation? It stops being replayed to the model.')) return
    api.messages
      .forget(m.id)
      .then(() => setMessages((all) => all.filter((x) => x.id !== m.id)))
      .catch((e: Error) => setError(e.message))
  }

  if (!workspace) {
    return (
      <div className="page">
        {error ? <p className="error">{error}</p> : <p className="hint">loading…</p>}
      </div>
    )
  }

  // NULL agent_id means the operator only on user rows; on agent rows it
  // means the agent was deleted (FK SET NULL) — never attribute those to
  // the operator.
  const agentName = (id: number | null) =>
    id == null ? '(deleted agent)' : agents.find((a) => a.id === id)?.name ?? `agent #${id}`

  return (
    <div className="ws-layout">
      <div className="ws-main">
        <div className="ws-head">
          <Link to="/workspaces" className="muted">
            ← workspaces
          </Link>
          <h2>{workspace.name}</h2>
          <span className="muted">{workspace.description}</span>
          <span className="spacer" />
          <div className="tabs">
            <button
              className={view === 'chat' && !selectedAgent ? 'active' : ''}
              onClick={() => {
                setView('chat')
                setSelectedAgent(null)
              }}
            >
              chat
            </button>
            <button
              className={view === 'blueprint' && !selectedAgent ? 'active' : ''}
              onClick={() => {
                setView('blueprint')
                setSelectedAgent(null)
              }}
            >
              blueprint
            </button>
            <button
              className={view === 'terminal' && !selectedAgent ? 'active' : ''}
              onClick={() => {
                setView('terminal')
                setSelectedAgent(null)
              }}
            >
              terminal
            </button>
          </div>
        </div>
        {selectedAgent ? (
          <AgentPanel
            agent={selectedAgent}
            models={models}
            wsId={wsId}
            status={statuses.get(selectedAgent.id)}
            onClose={() => setSelectedAgent(null)}
            onChanged={(a) => {
              setSelectedAgent(a)
              void reloadAgents()
            }}
            onError={setError}
          />
        ) : view === 'terminal' ? (
          <TerminalPage workspaceId={wsId} />
        ) : view === 'blueprint' ? (
          <BlueprintEditor
            wsId={wsId}
            agents={agents}
            statuses={statuses}
            onChanged={reloadAgents}
            onSelectAgent={setSelectedAgent}
            onError={setError}
          />
        ) : (
          <Timeline
            messages={messages}
            streams={streams}
            agentName={agentName}
            busy={busy}
            onForget={forget}
          />
        )}
        {error && <p className="error">{error}</p>}
        {!selectedAgent && view === 'chat' && (
          <Composer busy={busy} onSend={send} onStop={() => abortRef.current?.abort()} />
        )}
      </div>
      <aside className="ws-agents">
        <h3>Agents</h3>
        {agents.map((a) => {
          const st = statuses.get(a.id)
          const state = st?.state ?? 'idle'
          return (
            <button key={a.id} className={`agent-row ${selectedAgent?.id === a.id ? 'selected' : ''}`} onClick={() => setSelectedAgent(a)}>
              <span className={`dot ${state}`} title={state + (st?.detail ? `: ${st.detail}` : '')} />
              <span className="agent-name">
                {a.name}
                {a.is_orchestrator && <span className="muted"> ★</span>}
              </span>
              <span className="muted agent-model">{a.model_label || 'no model'}</span>
            </button>
          )
        })}
      </aside>
    </div>
  )
}

function Timeline({
  messages,
  streams,
  agentName,
  busy,
  onForget,
}: {
  messages: WSMessage[]
  streams: Map<number, string>
  agentName: (id: number | null) => string
  busy: boolean
  onForget: (m: WSMessage) => void
}) {
  const bottom = useRef<HTMLDivElement>(null)
  useEffect(() => bottom.current?.scrollIntoView({ behavior: 'smooth' }), [messages, streams])

  return (
    <div className="transcript">
      {messages.length === 0 && !busy && (
        <p className="hint">
          This is the workspace entry point: talk to the orchestrator. It can create agents, configure them, and
          delegate work.
        </p>
      )}
      {messages.map((m) => (
        <div key={m.id} className="timeline-entry">
          <MessageRow m={m} agentName={agentName} />
          <button className="forget" title="Forget this — it is replayed to the model on every turn" onClick={() => onForget(m)}>
            forget
          </button>
        </div>
      ))}
      {[...streams.entries()].map(([agentId, text]) =>
        text ? (
          <div key={`stream-${agentId}`} className="msg assistant">
            <span className="msg-role">{agentName(agentId)}</span>
            <div className="msg-body">{text}▍</div>
          </div>
        ) : null,
      )}
      <div ref={bottom} />
    </div>
  )
}

function MessageRow({ m, agentName }: { m: WSMessage; agentName: (id: number | null) => string }) {
  switch (m.kind) {
    case 'user':
      return (
        <div className="msg user">
          <span className="msg-role">you</span>
          <div className="msg-body">{m.content}</div>
        </div>
      )
    case 'assistant': {
      let calls: { name: string }[] = []
      try {
        calls = (JSON.parse(m.meta)?.tool_calls as { Name?: string; name?: string }[])?.map((c) => ({
          name: c.Name ?? c.name ?? '?',
        })) ?? []
      } catch {
        /* display-only */
      }
      if (!m.content && calls.length === 0) return null
      return (
        <div className="msg assistant">
          <span className="msg-role">{agentName(m.agent_id)}</span>
          <div className="msg-body">
            {m.content}
            {calls.length > 0 && (
              <span className="tool-chips">
                {calls.map((c, i) => (
                  <code key={i} className="chip">
                    ⚙ {c.name}
                  </code>
                ))}
              </span>
            )}
          </div>
        </div>
      )
    }
    case 'tool_result': {
      let name = 'tool'
      let isErr = false
      try {
        const meta = JSON.parse(m.meta)
        name = meta?.name ?? 'tool'
        isErr = !!meta?.is_error
      } catch {
        /* display-only */
      }
      return (
        <details className={`msg-tool ${isErr ? 'tool-error' : ''}`}>
          <summary>
            {isErr ? '✗' : '✓'} {name}
          </summary>
          <pre>{m.content}</pre>
        </details>
      )
    }
    case 'delegation':
      return (
        <div className="msg delegation">
          <span className="msg-role">{agentName(m.agent_id)}</span>
          <div className="msg-body">{m.content}</div>
        </div>
      )
    case 'error':
      return <p className="error">✗ {m.content}</p>
    // Kinds not rendered above still exist on the timeline; they are
    // replayed to the model but say nothing to a reader.
    default:
      return null
  }
}

function Composer({ busy, onSend, onStop }: { busy: boolean; onSend: (text: string) => void; onStop: () => void }) {
  const [input, setInput] = useState('')
  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        if (!input.trim() || busy) return
        onSend(input.trim())
        setInput('')
      }}
    >
      <input
        className="grow"
        placeholder="tell the orchestrator what you need…"
        value={input}
        disabled={busy}
        onChange={(e) => setInput(e.target.value)}
      />
      {busy ? (
        <button type="button" onClick={onStop}>
          stop
        </button>
      ) : (
        <button type="submit" disabled={!input.trim()}>
          send
        </button>
      )}
    </form>
  )
}

// AgentPanel is the per-agent view: identity, role editor, model binding,
// bound context, the assembled prompt preview, and its own activity trail.
function AgentPanel({
  agent,
  models,
  wsId,
  status,
  onClose,
  onChanged,
  onError,
}: {
  agent: Agent
  models: Model[]
  wsId: number
  status?: AgentStatus
  onClose: () => void
  onChanged: (a: Agent) => void
  onError: (msg: string) => void
}) {
  const [role, setRole] = useState(agent.role)
  const [modelId, setModelId] = useState<number | ''>(agent.model_id ?? '')
  const [activity, setActivity] = useState<WSMessage[]>([])
  const [bindings, setBindings] = useState<ContextBinding[]>([])
  const [spaceFiles, setSpaceFiles] = useState<ContextFile[]>([])
  const [contextUnavailable, setContextUnavailable] = useState(false)
  const [prompt, setPrompt] = useState<string | null>(null)
  const [gearBindings, setGearBindings] = useState<GearBinding[]>([])
  const [catalogGears, setCatalogGears] = useState<Gear[]>([])

  const reloadBindings = useCallback(
    () => api.context.bindings(wsId).then(setBindings).catch((e: Error) => onError(e.message)),
    [wsId, onError],
  )

  const reloadGears = useCallback(
    () =>
      Promise.all([api.gears.bindings(wsId), api.gears.list()])
        .then(([b, c]) => {
          setGearBindings(b)
          setCatalogGears(c)
        })
        .catch((e: Error) => onError(e.message)),
    [wsId, onError],
  )

  useEffect(() => {
    setRole(agent.role)
    setModelId(agent.model_id ?? '')
    setPrompt(null)
    api.workspaces
      .messages(wsId, agent.id)
      .then(setActivity)
      .catch((e: Error) => onError(e.message))
    void reloadBindings()
    void reloadGears()
    api.context
      .files()
      .then((f) => {
        setSpaceFiles(f)
        setContextUnavailable(false)
      })
      .catch(() => setContextUnavailable(true))
  }, [agent, wsId, onError, reloadBindings, reloadGears])

  // Bindings this agent actually sees: workspace-wide plus its own.
  const visible = bindings.filter((b) => b.agent_id === null || b.agent_id === agent.id)
  const boundPaths = new Set(visible.map((b) => b.path))

  const dirty = role !== agent.role || modelId !== (agent.model_id ?? '')

  return (
    <div className="agent-panel">
      <div className="card-head">
        <strong>{agent.name}</strong>
        {agent.is_orchestrator && <span className="muted">orchestrator — the workspace entry point</span>}
        <span className="muted">
          {status && status.state !== 'idle' ? `${status.state}${status.detail ? `: ${status.detail}` : ''}` : 'idle'}
        </span>
        <span className="spacer" />
        {!agent.is_orchestrator && (
          <button
            className="danger"
            onClick={() => {
              if (confirm(`Delete agent "${agent.name}"?`))
                api.agents
                  .remove(agent.id)
                  .then(() => {
                    onChanged(agent)
                    onClose()
                  })
                  .catch((e: Error) => onError(e.message))
            }}
          >
            delete
          </button>
        )}
        <button onClick={onClose}>back to chat</button>
      </div>

      <label className="field">
        <span className="muted">model</span>
        <select value={modelId} onChange={(e) => setModelId(e.target.value ? Number(e.target.value) : '')}>
          {/* Clearing a bound model isn't supported by the API; the empty
              option exists only to display agents that never had one. */}
          <option value="" disabled>
            no model
          </option>
          {models.map((m) => (
            <option key={m.id} value={m.id}>
              {m.provider_name} / {m.label || m.model_name}
            </option>
          ))}
        </select>
      </label>

      <label className="field">
        <span className="muted">role (system prompt)</span>
        <textarea rows={8} value={role} onChange={(e) => setRole(e.target.value)} />
      </label>

      <div className="row">
        <button
          disabled={!dirty}
          onClick={() => {
            const patch: { role?: string; model_id?: number } = {}
            if (role !== agent.role) patch.role = role
            if (modelId !== '' && modelId !== agent.model_id) patch.model_id = modelId
            api.agents
              .update(agent.id, patch)
              .then(onChanged)
              .catch((e: Error) => onError(e.message))
          }}
        >
          save
        </button>
      </div>

      <h3>Memory</h3>
      <AgentMemory agent={agent} onChanged={() => setPrompt(null)} onError={onError} />

      <h3>Add context</h3>
      {contextUnavailable ? (
        <p className="hint">
          Contextverse is not reachable — see the Context page. Agents run on their role alone until it is.
        </p>
      ) : (
        <>
          <BranchDocs agent={agent} spaceFiles={spaceFiles} onError={onError} onWritten={() => setPrompt(null)} />
          {visible.length > 0 && <p className="muted">Bound from elsewhere in the space:</p>}
          {visible.map((b) => (
            <div key={b.id} className="row binding">
              <code>{b.path}</code>
              <span className="muted">{b.agent_id === null ? 'whole workspace' : 'this agent'}</span>
              <span className="spacer" />
              <button
                onClick={() =>
                  api.context
                    .unbind(b.id)
                    .then(() => {
                      setPrompt(null)
                      return reloadBindings()
                    })
                    .catch((e: Error) => onError(e.message))
                }
              >
                unbind
              </button>
            </div>
          ))}
          <BindForm
            files={spaceFiles.filter((f) => !boundPaths.has(f.path))}
            onBind={(path, scope) =>
              api.context
                .bind(wsId, path, scope === 'agent' ? agent.id : null)
                .then(() => {
                  setPrompt(null)
                  return reloadBindings()
                })
                .catch((e: Error) => onError(e.message))
            }
          />
        </>
      )}

      <h3>Gears</h3>
      {(() => {
        const mine = gearBindings.filter((b) => b.agent_id === null || b.agent_id === agent.id)
        const boundIds = new Set(mine.map((b) => b.gear_id))
        const byId = new Map(catalogGears.map((g) => [g.id, g]))
        return (
          <>
            {mine.length === 0 && <p className="hint">No gears bound — this agent has no forged tools.</p>}
            {mine.map((b) => {
              const g = byId.get(b.gear_id)
              return (
                <div key={b.id} className="row binding">
                  <code>{b.gear_name || g?.name}</code>
                  {g && <span className={`status ${g.status}`}>{g.status}</span>}
                  <span className="muted">{b.agent_id === null ? 'whole workspace' : 'this agent'}</span>
                  <span className="spacer" />
                  <button
                    onClick={() =>
                      api.gears
                        .unbind(b.id)
                        .then(reloadGears)
                        .catch((e: Error) => onError(e.message))
                    }
                  >
                    unbind
                  </button>
                </div>
              )
            })}
            <GrantGearForm
              gears={catalogGears.filter((g) => !boundIds.has(g.id))}
              onGrant={(gearId, scope) =>
                api.gears
                  .bind(wsId, gearId, scope === 'agent' ? agent.id : null)
                  .then(reloadGears)
                  .catch((e: Error) => onError(e.message))
              }
            />
          </>
        )
      })()}

      <h3>Assembled prompt</h3>
      {prompt === null ? (
        <div className="row">
          <button
            onClick={() =>
              api.agents
                .prompt(agent.id)
                .then((p) => setPrompt(p.prompt))
                .catch((e: Error) => onError(e.message))
            }
          >
            show what this agent sees
          </button>
        </div>
      ) : (
        <>
          <div className="row">
            <span className="muted">{prompt.length} characters sent as the system prompt</span>
            <span className="spacer" />
            <button onClick={() => setPrompt(null)}>hide</button>
          </div>
          <pre className="prompt-preview">{prompt}</pre>
        </>
      )}

      <h3>Activity</h3>
      {activity.length === 0 ? (
        <p className="hint">Nothing yet — this agent hasn't produced or done anything.</p>
      ) : (
        <div className="transcript agent-activity">
          {activity.map((m) => (
            <MessageRow key={m.id} m={m} agentName={() => agent.name} />
          ))}
        </div>
      )}
    </div>
  )
}

// BranchDocs shows the agent's own Contextverse branch — context it reads
// without anyone binding it, organized by whose it is rather than by a list
// of manual bindings — and lets the operator write into it.
function BranchDocs({
  agent,
  spaceFiles,
  onError,
  onWritten,
}: {
  agent: Agent
  spaceFiles: ContextFile[]
  onError: (m: string) => void
  onWritten: () => void
}) {
  const [name, setName] = useState('')
  const mine = spaceFiles.filter((f) => f.path.startsWith(agent.branch + '/'))

  return (
    <>
      <p className="muted">
        Its own branch — <code>{agent.branch}/</code> — read automatically, no binding needed:
      </p>
      {mine.length === 0 && <p className="hint">Empty. Anything you put here only this agent sees.</p>}
      {mine.map((f) => (
        <div key={f.path} className="row binding">
          <code>{f.path.slice(agent.branch.length + 1)}</code>
          <span className="muted">{f.version}</span>
        </div>
      ))}
      <form
        className="row"
        onSubmit={(e) => {
          e.preventDefault()
          const file = name.trim().endsWith('.md') ? name.trim() : `${name.trim()}.md`
          api.context
            .put(`${agent.branch}/${file}`, `# ${name.trim()}\n\n`)
            .then(() => {
              setName('')
              onWritten()
            })
            .catch((err: Error) => onError(err.message))
        }}
      >
        <input
          className="grow"
          placeholder="new note in this agent's branch, e.g. house-style"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <button type="submit" disabled={!name.trim()}>
          create
        </button>
      </form>
    </>
  )
}

function GrantGearForm({
  gears,
  onGrant,
}: {
  gears: Gear[]
  onGrant: (gearId: number, scope: 'agent' | 'workspace') => void
}) {
  const [gearId, setGearId] = useState<number | ''>('')
  const [scope, setScope] = useState<'agent' | 'workspace'>('agent')

  if (gears.length === 0) return null
  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        if (gearId === '') return
        onGrant(gearId, scope)
        setGearId('')
      }}
    >
      <select className="grow" value={gearId} onChange={(e) => setGearId(e.target.value ? Number(e.target.value) : '')}>
        <option value="">grant a gear from the catalog…</option>
        {gears.map((g) => (
          <option key={g.id} value={g.id}>
            {g.name} ({g.status})
          </option>
        ))}
      </select>
      <select value={scope} onChange={(e) => setScope(e.target.value as 'agent' | 'workspace')}>
        <option value="agent">this agent only</option>
        <option value="workspace">whole workspace</option>
      </select>
      <button type="submit" disabled={gearId === ''}>
        grant
      </button>
    </form>
  )
}

function BindForm({
  files,
  onBind,
}: {
  files: ContextFile[]
  onBind: (path: string, scope: 'agent' | 'workspace') => void
}) {
  const [path, setPath] = useState('')
  const [scope, setScope] = useState<'agent' | 'workspace'>('agent')

  return (
    <form
      className="row"
      onSubmit={(e) => {
        e.preventDefault()
        if (!path) return
        onBind(path, scope)
        setPath('')
      }}
    >
      <select className="grow" value={path} onChange={(e) => setPath(e.target.value)}>
        <option value="">bind a context file…</option>
        {files.map((f) => (
          <option key={f.path} value={f.path}>
            {f.path}
          </option>
        ))}
      </select>
      <select value={scope} onChange={(e) => setScope(e.target.value as 'agent' | 'workspace')}>
        <option value="agent">this agent only</option>
        <option value="workspace">whole workspace</option>
      </select>
      <button type="submit" disabled={!path}>
        bind
      </button>
    </form>
  )
}
