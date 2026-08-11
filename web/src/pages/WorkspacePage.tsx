import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import BlueprintEditor from './BlueprintEditor'
import TerminalPage from './TerminalPage'
import AgentMemory from './AgentMemory'
import FilesPage from './FilesPage'
import CodeEditor from './CodeEditor'
import ApprovalDialog from './ApprovalDialog'
import InletsPanel from './InletsPanel'
import Bench, { type PanelDef } from '../bench/Bench'
import { useLayout } from '../bench/store'
import { loadTheme } from '../styles/theme'
import { PRESETS } from '../bench/presets'
import LayoutMenu from '../bench/LayoutMenu'

// The set of ids the layout parser will accept. A restored layout naming a
// panel nothing can render would otherwise be a permanent white screen.
const PANEL_IDS = new Set(['chat', 'blueprint', 'files', 'editor', 'terminal', 'agents', 'agent', 'inlets'])
import {
  api,
  wsChatStream,
  type Agent,
  type AgentStatus,
  type AgentUsage,
  type ApprovalRequest,
  type Attachment,
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
  // The file the editor panel holds. It lives here rather than in either panel
  // because the tree and the editor are two panels that have to agree on it.
  const [openPath, setOpenPath] = useState<string | null>(null)
  // Bumped on every save so the tree can re-read the directory and stop
  // reporting a stale size.
  const [savedTick, setSavedTick] = useState(0)
  const [usage, setUsage] = useState<Map<number, AgentUsage>>(new Map())
  // A paused turn waiting for permission to search the web. The SSE event is
  // a latency optimisation; the poll below is the mechanism, so a buffering
  // proxy that swallows the stream costs a second rather than the feature.
  const [approval, setApproval] = useState<ApprovalRequest | null>(null)
  const [answering, setAnswering] = useState(false)
  const [busy, setBusy] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  // Fresh agent ids for stream callbacks — a stale closure over `agents`
  // would refetch on every status event of a newly created agent.
  const agentIdsRef = useRef<Set<number>>(new Set())

  const reloadUsage = useCallback(
    () =>
      api.usage
        .workspace(wsId)
        .then((u) => setUsage(new Map(u.map((x) => [x.agent_id, x]))))
        .catch((e: Error) => setError(e.message)),
    [wsId],
  )

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
      api.usage.workspace(wsId),
    ])
      .then(([w, a, m, msgs, sts, u]) => {
        setWorkspace(w)
        agentIdsRef.current = new Set(a.map((x) => x.id))
        setAgents(a)
        setModels(m)
        setMessages(msgs)
        setStatuses(new Map(sts.map((s) => [s.agent_id, s])))
        setUsage(new Map(u.map((x) => [x.agent_id, x])))
      })
      .catch((e: Error) => setError(e.message))
  }, [wsId])

  useEffect(() => () => abortRef.current?.abort(), [])

  useEffect(() => {
    if (!busy) {
      setApproval(null)
      return
    }
    const poll = setInterval(() => {
      api.egress
        .pending(wsId)
        .then(setApproval)
        .catch(() => {})
    }, 2000)
    return () => clearInterval(poll)
  }, [busy, wsId])

  const answerApproval = useCallback(
    (allow: boolean) => {
      if (!approval) return
      setAnswering(true)
      api.egress
        .answer(approval.token, allow)
        .then(() => setApproval(null))
        .catch((e: Error) => {
          // A lost race is worth showing rather than swallowing: the operator
          // needs to know whether their click landed.
          setError(e.message)
          setApproval(null)
        })
        .finally(() => setAnswering(false))
    },
    [approval],
  )

  const send = async (text: string, attachments: string[]) => {
    setBusy(true)
    setError(null)
    const ac = new AbortController()
    abortRef.current = ac
    try {
      await wsChatStream(
        wsId,
        text,
        attachments,
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
      void reloadUsage()
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

  const layout = useLayout(1, (id) => PANEL_IDS.has(id))

  // The look carries its own arrangement. Choosing "canvas-first" and then
  // having to hunt for a matching layout preset would be two decisions for
  // one intention.
  const lookRef = useRef<string | null>(null)
  useEffect(() => {
    const apply = () => {
      const look = loadTheme().look
      if (lookRef.current === look) return
      const first = lookRef.current === null
      lookRef.current = look
      if (first) return // never overwrite an arrangement on a plain reload
      const preset = PRESETS.find((p) => (look === 'canvas' ? p.id === 'canvasfirst' : p.id === 'wire'))
      if (preset) layout.apply(preset.build())
    }
    apply()
    const t = setInterval(apply, 1200)
    return () => clearInterval(t)
  }, [layout])

  // Opening a file clears the bench for it.
  //
  // A file wants width. Everything except the conversation, the shell and the
  // tree you clicked in is put away, and the conversation simply narrows
  // rather than closing — watching a turn while editing what it produced is
  // the whole reason both are on screen. Nothing is destroyed: every panel put
  // away here is one chip away in the top bar, and the editor itself can be
  // floated out like any other.
  const KEEP_WITH_EDITOR = new Set(['chat', 'terminal', 'files', 'editor'])
  const openFile = useCallback(
    (path: string) => {
      setOpenPath(path)
      for (const id of PANEL_IDS) if (!KEEP_WITH_EDITOR.has(id)) layout.close(id)
      setSelectedAgent(null)
      layout.show('editor', 'aux')
      // A file needs width, and the side slot arrives at whatever the last
      // panel to sit in it left behind — 460px from the blueprint, which is
      // not a file. Ask for a generous one; the bench clamps stored sizes at
      // paint time against its own box, so this is an intent rather than a
      // measurement, and it survives being asked for on a small screen.
      layout.resize('aux', 760)
    },
    // KEEP_WITH_EDITOR is a module-level constant in spirit; rebuilding it per
    // render costs nothing and keeps it beside the rule it encodes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [layout],
  )

  // The inspector opens by clicking an agent and closes when the selection
  // goes. A restored layout must not bring back an empty one — that is the
  // "Agents / Agent, which is which?" confusion, rebuilt on every reload.
  useEffect(() => {
    if (!selectedAgent && layout.slotOf('agent')) layout.close('agent')
  }, [selectedAgent, layout])

  // Same rule as the agent inspector: a restored layout must not bring back an
  // editor with no file in it.
  useEffect(() => {
    if (!openPath && layout.slotOf('editor')) layout.close('editor')
  }, [openPath, layout])

  // ⌘↵ maximizes the focused panel; Escape is deliberately NOT bound here —
  // the approval dialog owns it, so one keypress can never both dismiss chrome
  // and silently refuse a pending web search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        layout.maximize(layout.layout.slots.main.active)
      }
      // ⌘J toggles the bottom dock, the way an editor's panel key works.
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'j') {
        e.preventDefault()
        if (layout.layout.slots.bottom.panels.length > 0) layout.toggleOpen('bottom')
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [layout])

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

  // Panel nodes are rebuilt on every render, which is exactly what happened
  // before the bench existed. What matters is that the ARRAY is stable and
  // keyed by id: React keeps each subtree alive, so the terminal's socket and
  // the blueprint's canvas survive being moved between slots.
  const panels: PanelDef[] = [
    {
      id: 'chat',
      minW: 520,
      title: 'Chat',
      home: 'main',
      canClose: false,
      node: (
        <div className="bn-body chat-body">
          <Timeline messages={messages} streams={streams} agentName={agentName} busy={busy} onForget={forget} />
          <Composer
            busy={busy}
            onSend={send}
            onStop={() => abortRef.current?.abort()}
            onAttach={(file) => api.workspaces.attach(wsId, file)}
          />
        </div>
      ),
    },
    {
      id: 'blueprint',
      minW: 420,
      title: 'Blueprint',
      home: 'aux',
      node: (
        <div className="bn-body">
          <BlueprintEditor
            wsId={wsId}
            agents={agents}
            statuses={statuses}
            onChanged={reloadAgents}
            onSelectAgent={(a) => {
              setSelectedAgent(a)
              layout.show('agent', 'aux')
            }}
            onError={setError}
          />
        </div>
      ),
    },
    {
      id: 'files',
      minW: 200,
      title: 'Files',
      home: 'left',
      node: (
        <div className="bn-body bn-scroll">
          <FilesPage wsId={wsId} openPath={openPath} savedTick={savedTick} onOpen={openFile} onError={setError} />
        </div>
      ),
    },
    {
      id: 'editor',
      minW: 460,
      // The title is the file, so a tab strip never says "Editor" twice.
      title: openPath ? openPath.slice(openPath.lastIndexOf('/') + 1) : 'Editor',
      home: 'aux',
      node: (
        <CodeEditor
          wsId={wsId}
          path={openPath}
          onClose={() => {
            setOpenPath(null)
            layout.close('editor')
          }}
          onSaved={() => setSavedTick((n) => n + 1)}
          onError={setError}
        />
      ),
    },
    {
      id: 'terminal',
      minW: 360,
      title: 'Terminal',
      home: 'bottom',
      restore: 'onDemand',
      node: (
        <div className="bn-body">
          <TerminalPage workspaceId={wsId} />
        </div>
      ),
    },
    {
      id: 'agents',
      minW: 200,
      title: 'Agents',
      home: 'right',
      node: (
        <div className="bn-body bn-scroll agent-cards">
          {agents.map((a) => {
            const st = statuses.get(a.id)
            const state = st?.state ?? 'idle'
            const u = usage.get(a.id)
            // The bar is this agent's share of the workspace's spend, not a
            // percentage of some invented budget. A number nobody can act on
            // is decoration; a share tells you where the money went.
            const total = [...usage.values()].reduce((n, x) => n + x.input_tokens + x.output_tokens, 0)
            const mine = u ? u.input_tokens + u.output_tokens : 0
            const share = total > 0 ? Math.round((mine / total) * 100) : 0
            return (
              <button
                key={a.id}
                className={`agent-card ${selectedAgent?.id === a.id ? 'selected' : ''}`}
                onClick={() => {
                  setSelectedAgent(a)
                  layout.show('agent', 'aux')
                }}
              >
                <span className="agent-card-head">
                  <span className={`dot ${state}`} title={state + (st?.detail ? `: ${st.detail}` : '')} />
                  <span className="agent-name">{a.name}</span>
                  {a.is_orchestrator && <span className="star" title="the workspace entry point">★</span>}
                </span>
                <span className="agent-spend-big" title={spendTitle(u)}>
                  {spendLabel(u)}
                </span>
                <span className="agent-model muted">{a.model_label || 'no model'}</span>
                <span className="share-bar" aria-hidden>
                  <span style={{ width: `${share}%` }} />
                </span>
                <span className="muted share-label">{total > 0 ? `${share}% of this workspace` : 'no spend yet'}</span>
              </button>
            )
          })}
        </div>
      ),
    },
    {
      id: 'inlets',
      minW: 460,
      title: 'Inlets',
      home: 'aux',
      node: (
        <div className="bn-body bn-scroll">
          {/* Whether the panel is placed at all decides whether it loads: the
              bench keeps every panel mounted, and a workspace with no doors
              should not pay two queries on every open for a feature it does
              not use. */}
          <InletsPanel wsId={wsId} agents={agents} shown={!!layout.slotOf('inlets')} onError={setError} />
        </div>
      ),
    },
    {
      id: 'agent',
      minW: 420,
      // The title IS the agent's name, so "Agents" (the roster) and this can
      // never be mistaken for each other on a tab strip.
      title: selectedAgent ? selectedAgent.name : 'Agent',
      home: 'aux',
      node: (
        <div className="bn-body bn-scroll">
          {selectedAgent ? (
            <AgentPanel
              agent={selectedAgent}
              models={models}
              wsId={wsId}
              status={statuses.get(selectedAgent.id)}
              onClose={() => {
                setSelectedAgent(null)
                layout.close('agent')
              }}
              onChanged={(a) => {
                setSelectedAgent(a)
                void reloadAgents()
              }}
              onError={setError}
            />
          ) : (
            <p className="hint">Pick an agent from the roster to inspect it.</p>
          )}
        </div>
      ),
    },
  ]

  return (
    <div className="ws-shell">
      {approval && <ApprovalDialog request={approval} onAnswer={answerApproval} busy={answering} />}
      {exporting && <ExportDialog wsId={wsId} name={workspace.name} onClose={() => setExporting(false)} />}
      <div className="ws-head">
        <Link to="/workspaces" className="muted">
          ←
        </Link>
        <h2>{workspace.name}</h2>
        <span className="muted">{workspace.description}</span>
        <span className="spacer" />
        <button
          onClick={() => setExporting(true)}
          title="Download this workspace as a bundle another install can rebuild it from"
        >
          export
        </button>
        <div className="row appearance">
          <LayoutMenu layout={layout} />
        </div>
        <div className="tabs">
          {panels
            .filter((p) => p.id !== 'agent')
            .map((p) => (
            <button
              key={p.id}
              className={layout.slotOf(p.id) ? 'active' : ''}
              onClick={() => (layout.slotOf(p.id) ? layout.close(p.id) : layout.show(p.id, p.home))}
              title={layout.slotOf(p.id) ? `Hide ${p.title}` : `Show ${p.title}`}
            >
                {p.title}
              </button>
            ))}
        </div>
      </div>
      {error && (
        <p className="error" onClick={() => setError(null)} title="dismiss">
          {error}
        </p>
      )}
      <Bench panels={panels} layout={layout} />
    </div>
  )
}

// ExportDialog downloads the workspace as a bundle: the agents, their roles
// and prohibitions, the wiring, and — only if asked for — the source of its
// gears and its context documents.
//
// Both extras are off until someone ticks them, because both are more than a
// shape. Gear source is executable code, and context documents are the notes
// the operator and the agents wrote; a workspace layout can be handed to a
// stranger without thinking, and those two cannot.
function ExportDialog({ wsId, name, onClose }: { wsId: number; name: string; onClose: () => void }) {
  const [gears, setGears] = useState(false)
  const [withContext, setWithContext] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const download = () => {
    setBusy(true)
    setError(null)
    api.workspaces
      .exportBundle(wsId, { gears, context: withContext })
      .then(({ text, filename }) => {
        const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
        const a = document.createElement('a')
        a.href = url
        a.download = filename
        // The anchor is put in the document because a click on a detached one
        // is ignored in Firefox, and the object URL is released on a later
        // tick because revoking it in this one cancels the download it just
        // started.
        document.body.appendChild(a)
        a.click()
        a.remove()
        setTimeout(() => URL.revokeObjectURL(url), 0)
        onClose()
      })
      .catch((e: Error) => {
        setError(e.message)
        setBusy(false)
      })
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      {/* Closed by the backdrop, cancel, or ×, and deliberately not by Escape.
          Escape on this page belongs to the approval dialog, and it only
          reaches a dialog that holds focus — clicking a button on macOS does
          not give it any — so a handler here would work sometimes, which is
          worse than not having one. */}
      <div className="modal create-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
        <div className="row theme-head">
          <h3>Export {name}</h3>
          <span className="spacer" />
          <button onClick={onClose} title="Close">
            ×
          </button>
        </div>
        <div className="create-body">
          <p className="hint">
            A bundle is a template, not a copy: it carries the agents, their roles and prohibitions, and the
            wiring — never a provider key, an owner, or a line of this conversation.
          </p>
          <label className="row">
            <input autoFocus type="checkbox" checked={gears} onChange={(e) => setGears(e.target.checked)} />
            include gears
          </label>
          <span className="hint">
            The full source of every tool bound here. On the other install they arrive needing approval, and a
            name it already uses is left alone.
          </span>
          <label className="row">
            <input type="checkbox" checked={withContext} onChange={(e) => setWithContext(e.target.checked)} />
            include context
          </label>
          <span className="hint">
            The documents on this workspace's branches — shared notes and each agent's own memory. Read them
            first if the bundle is leaving your machine.
          </span>
          {error && <p className="error">{error}</p>}
          <div className="row">
            <span className="spacer" />
            <button type="button" onClick={onClose}>
              cancel
            </button>
            <button className="primary" onClick={download} disabled={busy}>
              {busy ? 'building…' : 'download bundle'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function Spend({ agentId }: { agentId: number }) {
  const [u, setU] = useState<AgentUsage | null>(null)
  useEffect(() => {
    let live = true
    api.usage
      .agent(agentId)
      .then((x) => live && setU(x))
      .catch(() => live && setU(null))
    return () => {
      live = false
    }
  }, [agentId])

  if (!u) return null
  if (u.turns === 0) return <span className="muted">no model calls yet</span>
  const total = u.input_tokens + u.output_tokens
  const blind = u.unreported_turns === u.turns && total === 0
  return (
    <span className="muted agent-spend-detail" title={spendTitle(u)}>
      {blind ? (
        <>this provider reports no token usage</>
      ) : (
        <>
          {total.toLocaleString()} tokens
          <span className="dim">
            {' '}
            ({u.input_tokens.toLocaleString()} in / {u.output_tokens.toLocaleString()} out, {u.turns}{' '}
            {u.turns === 1 ? 'call' : 'calls'})
          </span>
          {u.unreported_turns > 0 && <span className="warn"> · {u.unreported_turns} unreported</span>}
        </>
      )}
    </span>
  )
}

// Spend is shown compactly in the list and in full on hover. Rounding to "k"
// is fine for a glance; the tooltip carries the exact figures because the
// point of the display is comparing what each agent actually costs.
function compact(n: number) {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`
  return `${(n / 1_000_000).toFixed(1)}M`
}

function spendLabel(u?: AgentUsage) {
  if (!u || u.turns === 0) return '—'
  const total = u.input_tokens + u.output_tokens
  // A provider that reports nothing would otherwise show a confident 0.
  if (total === 0 && u.unreported_turns === u.turns) return 'n/a'
  return compact(total)
}

function spendTitle(u?: AgentUsage) {
  if (!u || u.turns === 0) return 'has not run yet'
  const lines = [
    `${u.input_tokens.toLocaleString()} in + ${u.output_tokens.toLocaleString()} out`,
    `${u.turns} model ${u.turns === 1 ? 'call' : 'calls'}`,
  ]
  if (u.unreported_turns > 0) {
    lines.push(`${u.unreported_turns} of them reported no usage — the real spend is higher`)
  }
  return lines.join('\n')
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
  // Scroll the transcript itself, never scrollIntoView: that walks UP the tree
  // and scrolls every scrollable ancestor, which drags the workspace header
  // off screen. Only follow when the operator is already at the bottom —
  // yanking them away from something they scrolled back to read is worse than
  // missing the newest line.
  useEffect(() => {
    const el = bottom.current?.parentElement
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 120
    if (nearBottom) el.scrollTop = el.scrollHeight
  }, [messages, streams])

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
            <div className="msg-body">
              {agentName(agentId) !== 'orchestrator' && <span className="who">{agentName(agentId)}</span>}
              {text}
              <span className="caret">▍</span>
            </div>
          </div>
        ) : null,
      )}
      <div ref={bottom} />
    </div>
  )
}

function MessageRow({ m, agentName }: { m: WSMessage; agentName: (id: number | null) => string }) {
  switch (m.kind) {
    case 'user': {
      // What was attached is part of what was said, so it is shown on the
      // message rather than in a panel somewhere: a conversation where the
      // operator's file is invisible is one where nobody can tell why the
      // answer talks about a diagram.
      let attachments: Attachment[] = []
      try {
        attachments = (JSON.parse(m.meta)?.attachments as Attachment[]) ?? []
      } catch {
        /* display-only */
      }
      return (
        <div className="msg user">
          <div className="msg-body">{m.content}</div>
          {attachments.length > 0 && (
            <div className="attachments">
              {attachments.map((a) => (
                <span key={a.path} className={`attachment ${a.kind ? '' : 'to-gear'}`} title={attachmentTitle(a)}>
                  <span className="attachment-name">{fileName(a.path)}</span>
                  <span className="muted">{bytes(a.bytes)}</span>
                  {!a.kind && <span className="muted">→ gear</span>}
                </span>
              ))}
            </div>
          )}
        </div>
      )
    }
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
          <div className="msg-body">
            {/* Only a delegate is named. The orchestrator is the voice of the
                workspace, so labelling every one of its replies is the same
                noise as labelling your own. */}
            {agentName(m.agent_id) !== 'orchestrator' && <span className="who">{agentName(m.agent_id)}</span>}
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
          <div className="msg-body">
            <span className="who">{agentName(m.agent_id)}</span>
            {m.content}
          </div>
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

// Composer takes the operator's words and their files.
//
// A file is uploaded the moment it is picked, not when send is pressed. That is
// what lets the server answer what will become of it — shown to the model, or
// handed to the agent as a path — while there is still time to do something
// about it. The message itself then carries only paths.
function Composer({
  busy,
  onSend,
  onStop,
  onAttach,
}: {
  busy: boolean
  onSend: (text: string, attachments: string[]) => void
  onStop: () => void
  onAttach: (file: File) => Promise<Attachment>
}) {
  const [input, setInput] = useState('')
  const [attached, setAttached] = useState<Attachment[]>([])
  const [uploading, setUploading] = useState(0)
  const [attachError, setAttachError] = useState<string | null>(null)
  const picker = useRef<HTMLInputElement>(null)

  const take = async (files: FileList | null) => {
    if (!files || files.length === 0) return
    setAttachError(null)
    // One at a time, and each one kept as it lands: a fourth file the server
    // refuses must not take the three that worked with it.
    for (const file of Array.from(files)) {
      setUploading((n) => n + 1)
      try {
        const att = await onAttach(file)
        setAttached((all) => [...all, att])
      } catch (err) {
        setAttachError(`${file.name}: ${err instanceof Error ? err.message : String(err)}`)
      } finally {
        setUploading((n) => n - 1)
      }
    }
  }

  const sendable = (input.trim() !== '' || attached.length > 0) && uploading === 0

  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault()
        if (!sendable || busy) return
        onSend(
          input.trim(),
          attached.map((a) => a.path),
        )
        setInput('')
        setAttached([])
        setAttachError(null)
      }}
    >
      {attached.length > 0 && (
        <div className="attachments">
          {attached.map((a) => (
            <span
              key={a.path}
              className={`attachment ${a.kind ? '' : 'to-gear'} ${a.warning ? 'refused' : ''}`}
              title={attachmentTitle(a)}
            >
              <span className="attachment-name">{fileName(a.path)}</span>
              <span className="muted">{bytes(a.bytes)}</span>
              {/* The one thing the operator cannot see for themselves: whether
                  the model is going to look at this, or whether it is going to
                  their agents as a path for a gear to open. */}
              {!a.kind && <span className="muted">→ gear</span>}
              {/* A file this model was never said to accept would take the
                  whole message down with it, so it is marked before send
                  rather than explained afterwards. */}
              {a.warning && <span className="warn">⚠</span>}
              <button
                type="button"
                className="attachment-drop"
                title="Take this off the message. The file stays in the workspace."
                onClick={() => setAttached((all) => all.filter((x) => x.path !== a.path))}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      {attachError && <p className="error">{attachError}</p>}
      <div className="row">
        <input
          type="file"
          multiple
          ref={picker}
          className="hidden-file"
          onChange={(e) => {
            void take(e.target.files)
            // Cleared so that picking the same file again is still a change
            // event — attaching one file twice is a thing people do.
            e.target.value = ''
          }}
        />
        <button
          type="button"
          title="Attach files. Anything at all: what the model can read it is shown, and everything else your agents get as a path."
          disabled={busy}
          onClick={() => picker.current?.click()}
        >
          {uploading > 0 ? '…' : '+'}
        </button>
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
          <button type="submit" disabled={!sendable}>
            send
          </button>
        )}
      </div>
    </form>
  )
}

function fileName(path: string) {
  return path.slice(path.lastIndexOf('/') + 1)
}

function bytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// The tooltip carries the whole truth: the path an agent can be told, and the
// server's own sentence about why the model is not being shown it. That
// sentence is the server's rather than this file's on purpose — it is the same
// one the agent is given, so operator and agent are never told two stories.
function attachmentTitle(a: Attachment) {
  const lines = [a.path, a.media_type || 'type unknown']
  lines.push(a.kind ? `the model is shown this, as ${a.kind}` : (a.skipped ?? 'the model is not shown this'))
  if (a.warning) lines.push(a.warning)
  return lines.join('\n')
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
  const [avoid, setAvoid] = useState(agent.avoid)
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
    setAvoid(agent.avoid)
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

  const dirty = role !== agent.role || avoid !== agent.avoid || modelId !== (agent.model_id ?? '')

  return (
    <div className="agent-panel">
      <div className="card-head">
        <strong>{agent.name}</strong>
        {agent.is_orchestrator && <span className="muted">orchestrator — the workspace entry point</span>}
        <span className="muted">
          {status && status.state !== 'idle' ? `${status.state}${status.detail ? `: ${status.detail}` : ''}` : 'idle'}
        </span>
        <Spend agentId={agent.id} />
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

      {/* The prohibitions sit beside the role because they are the other half
          of the same instruction, and they are a list rather than prose
          because a list is what an operator actually writes. */}
      <label className="field">
        <span className="muted">never do this (one rule per line)</span>
        <textarea
          rows={4}
          value={avoid}
          onChange={(e) => setAvoid(e.target.value)}
          placeholder={'never install packages\nnever email anyone outside the team'}
        />
        <span className="hint">
          Goes last in the system prompt, after everything else, and holds for the whole conversation — a request
          that needs one of these is refused. Leave it empty and nothing is added at all. "Show what this agent
          sees", below, is the exact text.
        </span>
      </label>

      <div className="row">
        <button
          disabled={!dirty}
          onClick={() => {
            const patch: { role?: string; avoid?: string; model_id?: number } = {}
            if (role !== agent.role) patch.role = role
            // Sent even when it is empty: "" is how the last rule is removed,
            // and an omitted field leaves the prohibitions as they were.
            if (avoid !== agent.avoid) patch.avoid = avoid
            if (modelId !== '' && modelId !== agent.model_id) patch.model_id = modelId
            api.agents
              .update(agent.id, patch)
              .then((a) => {
                // The preview is the point of the field — a rule the operator
                // cannot see is a rule they cannot debug — so a stale one must
                // not survive the save that changed it.
                setPrompt(null)
                onChanged(a)
              })
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
