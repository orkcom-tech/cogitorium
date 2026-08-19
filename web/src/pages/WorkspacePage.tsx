import { useCallback, useEffect, useRef, useState, useMemo } from 'react'
import { useParams } from 'react-router-dom'
import BlueprintEditor from './BlueprintEditor'
import TerminalPage from './TerminalPage'
import AgentMemory from './AgentMemory'
import FilesPage from './FilesPage'
import CodeEditor from './CodeEditor'
import ApprovalDialog from './ApprovalDialog'
import { Select } from './Select'
import { Deck, ShellGate, Workbench } from '../deck/Deck'
import { Drawer, type Edge } from '../deck/Drawer'
import { useDeck } from '../deck/store'
import type { OverlayId, ViewId } from '../deck/types'
import { usePublishShell } from '../shell'
import { STAGE_ICON, DRAWER_ICON } from '../shell-icons'
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
  type User,
  type WSMessage,
  type Workspace,
  contributions,
} from '../api'

export default function WorkspacePage({ me }: { me: User }) {
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

  const deck = useDeck(1)
  // The one overlay currently open, if any. Not persisted, deliberately: an
  // overlay is something you consulted a minute ago, not part of how the
  // workspace is arranged, and restoring one on load would put a panel over
  // the operator's work for a question they already had answered.
  // A plugin's panel is identified by "plugin:<id>", which cannot collide with
  // an OverlayId because none of those contains a colon. Widening the state's
  // type rather than the union keeps the host's own overlays exhaustively
  // checked by the compiler, which is what stops a new one being forgotten in
  // the switch below.
  const [overlay, setOverlay] = useState<OverlayId | `plugin:${string}` | null>(null)
  const mounts = useMemo(
    () => contributions().mounts.filter((m) => m.point === 'workspace.drawer'),
    [],
  )
  const openMount = typeof overlay === 'string' && overlay.startsWith('plugin:')
    ? mounts.find((m) => `plugin:${m.from}` === overlay)
    : undefined
  // A gear the operator asked to see the source of, from somewhere that is not
  // the gear list — today, the note a blueprint drop leaves when the gear that
  // landed has never been approved. It opens the card; it approves nothing.
  const [reviewGear, setReviewGear] = useState<number | null>(null)
  // Whether the operator has asked for a shell IN THIS SESSION. Never
  // persisted: see ShellGate.
  const [shell, setShell] = useState(false)

  // Opening a file goes to the workbench, which is the view a file lives in.
  //
  // This used to be nine lines that closed five panels by name and then asked
  // the side dock for 760px, because a file had to fight the blueprint and the
  // queue for the same slot. A view does not share, so there is nothing to
  // clear and nothing to size.
  const openFile = useCallback(
    (path: string) => {
      setOpenPath(path)
      deck.go('workbench')
    },
    [deck],
  )

  // The inspector belongs to a selection, so it closes with it — and opening
  // it IS selecting an agent. That removes the "Agents / Agent, which is
  // which?" confusion by construction rather than by naming them apart.
  useEffect(() => {
    if (!selectedAgent && overlay === 'agent') setOverlay(null)
  }, [selectedAgent, overlay])

  // Escape is deliberately bound nowhere: the approval dialog owns it, so one
  // keypress can never both dismiss chrome and silently refuse a pending web
  // search. An overlay closes by clicking away from it, or on its own button.


  // What the frame should offer while this workspace is in the cavity: the
  // three views become stages on the rail, the four overlays become drawers,
  // the name becomes the rotated text, and export becomes the one action.
  //
  // It sits ABOVE the early return for a workspace that has not loaded, because
  // a hook after a conditional return is called on some renders and not others
  // — React notices, and the rail ended up with nothing on it. Publishing null
  // until there is something to publish is the same statement without the bug.
  // Which edge each drawer comes out of unless the operator has moved it.
  // One sentence explains the whole table: you take FROM the right, and what
  // happens over TIME arrives at the bottom.
  const DRAWER_EDGE: Record<string, Edge> = {
    agents: 'right',
    gears: 'right',
    mcp: 'right',
    instructions: 'right',
    memory: 'right',
    env: 'right',
    agent: 'right',
    inlets: 'bottom',
    queue: 'bottom',
    terminal: 'bottom',
  }
  // Capsule 3, in the agreed order: the roster, the two catalogues an agent
  // draws on, what it remembers, the door in, the work waiting, and the names
  // a gear is given. Gears and Instructions used to be destinations that
  // replaced the whole screen; they are things you consult while working, so
  // they crawl out over the work instead of taking it away.
  const OVERLAY_ITEMS: { id: OverlayId; title: string }[] = [
    { id: 'agents', title: 'Agents' },
    { id: 'gears', title: 'Gears' },
    // Beside Gears, because they are the same kind of object: somebody else's
    // code, granted to an agent, behind an approval. The card says how they
    // differ, which is on every axis and not in this list's favour.
    { id: 'mcp', title: 'MCP servers' },
    { id: 'instructions', title: 'Instructions' },
    { id: 'memory', title: 'Memory' },
    { id: 'inlets', title: 'Receivers' },
    { id: 'queue', title: 'Queue' },
    { id: 'env', title: 'Variables' },
    // The context space, admin-only, and a drawer for the same reason
    // everything else here is one: it is consulted while you work, not a place
    // you go instead of working. It was the last thing that existed only as a
    // page of its own.
    ...(me.role === 'admin' ? [{ id: 'context' as OverlayId, title: 'Context' }] : []),
    // A terminal is pulled out and pushed back, not lived in — so it crawls
    // out of the bottom edge like the other things that happen over time,
    // rather than taking the cavity from the work it is being run against.
    { id: 'terminal', title: 'Terminal' },
  ]
  usePublishShell(
    () =>
      workspace
        ? {
            here: {
              label: workspace.name,
              note: workspace.description,
              state: busy ? 'running' : undefined,
            },
            back: '/workspaces',
            stages: {
              items: [
                { id: 'chat', title: 'Chat', icon: STAGE_ICON.chat },
                { id: 'blueprint', title: 'Blueprint', icon: STAGE_ICON.blueprint },
                { id: 'workbench', title: 'Editor', icon: STAGE_ICON.workbench },
              ],
              current: deck.deck.view,
              go: (id: string) => deck.go(id as ViewId),
            },
            drawers: {
              items: [
                ...OVERLAY_ITEMS.map((o) => ({ id: o.id, title: o.title, icon: DRAWER_ICON[o.id] })),
                // After the host's own, because a plugin adding a panel is
                // adding to this workspace rather than rearranging it.
                ...mounts.map((m) => ({
                  id: `plugin:${m.from}`,
                  title: m.title,
                  icon: DRAWER_ICON.gears,
                })),
              ],
              open: overlay,
              toggle: (id: string | null) =>
                setOverlay(id as OverlayId | `plugin:${string}` | null),
            },
            action: {
              label: 'export',
              title: 'Download this workspace as a bundle another install can rebuild it from',
              run: () => setExporting(true),
            },
          }
        : null,
    [workspace?.name, workspace?.description, busy, deck.deck.view, overlay, mounts],
  )

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

  // The three views, and the four things you consult.
  //
  // Every view is mounted for the whole life of the page — see Deck's
  // invariant. The nodes below are rebuilt on each render and that is fine;
  // what matters is that the tree shape never changes, so the terminal's
  // socket and the blueprint's canvas are never torn down.
  /** The rail's name for a drawer, as the server knows it.
   *
   *  The two differ for two of them, and they are mapped here rather than by
   *  renaming either: the word on screen is what somebody reads, and the word
   *  in the code is what the rest of this file already uses. */
  const drawerName = (o: string) =>
    o === 'env' ? 'variables' : o === 'inlets' ? 'receivers' : o

  // An overlay that is shut does not load and does not poll. The receivers
  // cost two queries, the queue runs a timer, and a workspace using neither
  // should pay for neither.
  const OVERLAYS = OVERLAY_ITEMS
  const overlayTitle =
    overlay === 'agent'
      ? (selectedAgent?.name ?? 'Agent')
      : (OVERLAYS.find((o) => o.id === overlay)?.title ?? '')


  return (
    <div className="ws-shell">
      {approval && <ApprovalDialog request={approval} onAnswer={answerApproval} busy={answering} />}
      {exporting && <ExportDialog wsId={wsId} name={workspace.name} onClose={() => setExporting(false)} />}
      {/* No header. The three views, the four drawers, the name, the way out
          and export all live on the rail now — published from here, drawn
          there. See shell.tsx: the cavity holds content and nothing else, and
          a workspace's own toolbar was the last thing breaking that. */}
      {error && (
        <p className="error" onClick={() => setError(null)} title="dismiss">
          {error}
        </p>
      )}

      <Deck
        view={deck.deck.view}
        views={[
          {
            id: 'chat',
            node: (
              <div className="dk-body chat-body">
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
            node: (
              <div className="dk-body">
                <BlueprintEditor
                  wsId={wsId}
                  agents={agents}
                  models={models}
                  statuses={statuses}
                  onChanged={reloadAgents}
                  onSelectAgent={(a) => {
                    setSelectedAgent(a)
                    setOverlay('agent')
                  }}
                  onReviewGear={(id) => {
                    setReviewGear(id)
                    setOverlay('gears')
                  }}
                  onError={setError}
                />
              </div>
            ),
          },
          {
            id: 'workbench',
            node: (
              <Workbench
                deck={deck}
                /* No padded, scrolling wrapper. It inset the tree eight pixels
                   from the cavity's own rounded corner — two edges in the one
                   corner where a doubled edge is impossible to miss — and its
                   scrollbar was a second one beside the tree's own. The tree
                   reaches the edge and scrolls its rows itself. */
                files={
                  <FilesPage
                    wsId={wsId}
                    openPath={openPath}
                    savedTick={savedTick}
                    onOpen={openFile}
                    onError={setError}
                  />
                }
                editor={
                  <CodeEditor
                    wsId={wsId}
                    path={openPath}
                    onClose={() => setOpenPath(null)}
                    onSaved={() => setSavedTick((n) => n + 1)}
                    onError={setError}
                  />
                }
              />
            ),
          },
        ]}
      />

      {/* Right for the things you take from, bottom for the things that happen
          over time — and any of the four by choice, remembered per drawer. */}
      <Drawer
        id={overlay ?? 'none'}
        open={overlay !== null}
        title={overlayTitle}
        defaultEdge={DRAWER_EDGE[overlay ?? 'agents'] ?? 'right'}
        defaultSize={overlay === 'queue' ? 320 : 400}
        onClose={() => {
          setOverlay(null)
          setReviewGear(null)
        }}
      >
        {openMount && (
          /* An iframe rather than injected markup, and deliberately so: the
             panel is the plugin's own page, served by the server at its own
             URL, so what renders here is exactly what renders when somebody
             opens that URL in a tab. One implementation, one thing to test,
             and the plugin's styles cannot leak into the workspace around it. */
          <iframe
            className="dk-body plugin-panel"
            src={openMount.page}
            title={openMount.title}
            sandbox="allow-scripts allow-same-origin"
          />
        )}
        {overlay === 'terminal' && (
          <ShellGate started={shell} onStart={() => setShell(true)}>
            <div className="dk-body">
              <TerminalPage workspaceId={wsId} />
            </div>
          </ShellGate>
        )}
        {/* Three panels the SERVER renders now, swapped in by htmx.
            This is the seam the conversion is happening on: the workspace —
            its chat, its blueprint, its editor — is still the client's, and
            these are templates going through the composed stack. A plugin
            overriding cog.drawer.gears changes what somebody sees in here,
            without this page having to become a template first.

            The context drawer keeps its admin rule; the server applies it, so
            a rule that lives in one place cannot disagree with itself. */}
        {/* The roster is the one panel whose rows drive this page: picking an
            agent opens a different drawer, and which drawer is open is this
            component's state. So the server renders the markup and the click
            stays here — a delegated listener on the container, reading the id
            the row carries. That is the seam that let the roster become a
            template without the workspace around it having to be one. */}
        {overlay === 'agents' && (
          <div
            key="agents"
            className="dk-body"
            hx-get={`/workspaces/${wsId}/drawers/agents${
              selectedAgent ? `?selected=${selectedAgent.id}` : ''
            }`}
            hx-trigger="load"
            hx-swap="innerHTML"
            onClick={(e) => {
              const card = (e.target as HTMLElement).closest('[data-agent-id]')
              if (!card) return
              const id = Number(card.getAttribute('data-agent-id'))
              const picked = agents.find((a) => a.id === id)
              if (picked) {
                setSelectedAgent(picked)
                setOverlay('agent')
              }
            }}
          />
        )}

        {(overlay === 'gears' ||
          overlay === 'instructions' ||
          overlay === 'context' ||
          overlay === 'env' ||
          overlay === 'inlets' ||
          overlay === 'mcp' ||
          overlay === 'queue') && (
          <div
            key={overlay}
            className="dk-body"
            hx-get={`/workspaces/${wsId}/drawers/${drawerName(overlay)}${
              overlay === 'gears' && reviewGear ? `?open=${reviewGear}` : ''
            }`}
            hx-trigger="load"
            hx-swap="innerHTML"
          />
        )}
        {overlay === 'memory' &&
          (selectedAgent ? (
            <AgentMemory agent={selectedAgent} onChanged={() => void reloadAgents()} onError={setError} />
          ) : (
            <p className="hint">
              Memory belongs to one agent. Pick one in Agents, and it opens here.
            </p>
          ))}
        {overlay === 'agent' &&
          (selectedAgent ? (
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
          ) : (
            <p className="hint">Pick an agent from the roster to inspect it.</p>
          ))}
      </Drawer>
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
        /* Centred, because an empty transcript is a whole screen and a line of
           grey text jammed into its top-left corner reads as something left
           behind rather than as an invitation. */
        <p className="hint empty-note">
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
                className="attachment-drop" data-own
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
        {/* No name here. The drawer head above already prints it, forty pixels
            up, and this printed it a second time — the same defect the file
            tree and every other panel had. */}
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
        {/* Clearing a bound model isn't supported by the API; the placeholder
            exists only to display agents that never had one. */}
        <Select
          value={modelId === '' ? '' : String(modelId)}
          aria-label="Model"
          placeholder="no model"
          onChange={(v) => setModelId(v ? Number(v) : '')}
          options={models.map((m) => ({
            value: String(m.id),
            label: `${m.provider_name} / ${m.label || m.model_name}`,
          }))}
        />
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
          aria-label="New note"
          placeholder="house-style"
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
      <Select
        value={gearId === '' ? '' : String(gearId)}
        aria-label="Grant a gear from the catalog"
        placeholder="grant a gear from the catalog…"
        className="grow"
        onChange={(v) => setGearId(v ? Number(v) : '')}
        options={gears.map((g) => ({ value: String(g.id), label: `${g.name} (${g.status})` }))}
      />
      <Select
        value={scope}
        aria-label="Scope of this grant"
        onChange={(v) => setScope(v as 'agent' | 'workspace')}
        options={[
          { value: 'agent', label: 'this agent only' },
          { value: 'workspace', label: 'whole workspace' },
        ]}
      />
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
      <Select
        value={path}
        aria-label="Bind a context file"
        placeholder="bind a context file…"
        className="grow"
        onChange={setPath}
        options={files.map((f) => ({ value: f.path, label: f.path }))}
      />
      <Select
        value={scope}
        aria-label="Scope of this binding"
        onChange={(v) => setScope(v as 'agent' | 'workspace')}
        options={[
          { value: 'agent', label: 'this agent only' },
          { value: 'workspace', label: 'whole workspace' },
        ]}
      />
      <button type="submit" disabled={!path}>
        bind
      </button>
    </form>
  )
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
