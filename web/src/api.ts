export type Provider = {
  id: number
  name: string
  type: 'anthropic' | 'openai-compatible'
  base_url: string
  has_key: boolean
}

export type Model = {
  id: number
  provider_id: number
  provider_name: string
  provider_type: string
  model_name: string
  label: string
}

export type TestResult = { ok: boolean; models?: string[]; error?: string }

export class Unauthorized extends Error {}

async function req<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(session.url(url), {
    ...init,
    headers: session.headers({ 'Content-Type': 'application/json', ...(init?.headers as object) }),
  })
  if (r.status === 401) throw new Unauthorized('sign in required')
  if (r.status === 204) return undefined as T
  const body = await r.json().catch(() => null)
  if (!r.ok) {
    throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
  }
  return body as T
}

// form wraps one file as the multipart body the server reads. The field name
// is irrelevant to the server — it takes the first part that has a filename —
// so it says what it is for the benefit of anyone reading a request log.
function form(file: File): FormData {
  const fd = new FormData()
  fd.append('file', file, file.name)
  return fd
}

// bundleFilename reads the name the server chose for a downloaded bundle. The
// server owns that rule — it is the same code that keeps an operator-supplied
// workspace name from closing the quoted header value — so the client asks
// rather than deriving its own. A response that reaches us without the header
// (a proxy that dropped it) still has to save as something, and this is the
// name the server itself uses for a workspace whose name carries no letters.
function bundleFilename(disposition: string | null): string {
  return disposition?.match(/filename="([^"]+)"/)?.[1] ?? 'workspace.cogitorium.json'
}

export type User = { id: number; name: string; role: 'admin' | 'team-lead' | 'member'; teams: number[] }

// The server assembles relationships into one graph because it is the only
// place that knows all of them; the client only draws what it is given.
export type GraphNodeKind =
  | 'agent'
  | 'gear'
  | 'shared'
  | 'private'
  | 'document'
  | 'instruction'
  | 'user'
  | 'team'
  | 'workspace'
export type GraphNode = {
  id: string
  kind: GraphNodeKind
  label: string
  detail?: string
  status?: string
  agent_id?: number
}
export type GraphEdge = {
  from: string
  to: string
  kind: 'delegates' | 'tool' | 'knows' | 'owns' | 'shared' | 'member'
  label?: string
  id?: number
}
export type GraphData = { nodes: GraphNode[]; edges: GraphEdge[] }

// unreported_turns counts model calls whose provider sent no usage figures.
// It is shown rather than hidden: a spend of 0 means something different when
// the provider never reports, and the operator should be told which it is.
export type FileEntry = { name: string; path: string; dir: boolean; size: number; mtime: string }

// The internet gate. Two human keys: a master switch that lives only in the
// server's configuration file, and a per-agent grant an operator draws on the
// blueprint. Neither is reachable from an agent, and every individual search
// still stops the turn and waits for a person.
// Is there a newer release, and may this install ask.
//
// `mode` is three-valued rather than a bool because "nobody has answered yet"
// is a real state: an install that has not been asked is not the same as one
// that said no. `off` is set in the server's config file and cannot be changed
// from here — the interface shows it and does not offer the switch.
export type UpdateMode = 'ask' | 'on' | 'off'

export type UpdateRelease = {
  tag: string
  name: string
  notes: string
  url: string
  published_at: string
}

export type UpdateProduct = {
  name: string
  /** What this machine runs. Empty means the product is not installed here. */
  running: string
  latest?: UpdateRelease
  /** Strictly newer, and only when the comparison was conclusive. */
  newer: boolean
  /** False for a development build: "up to date" and "cannot say" differ. */
  comparable: boolean
  error?: string
}

// How this copy got onto the machine, which decides what an honest "take it"
// line can offer. A container and a cluster carry no command on purpose: the
// deploy pipeline owns the version there, and anything typed into a pod is
// undone by the next roll.
export type UpdateInstall = {
  kind: 'homebrew' | 'scoop' | 'winget' | 'deb-rpm' | 'container' | 'kubernetes' | 'desktop' | 'manual'
  command?: string
  note: string
}

export type UpdateReport = {
  mode: UpdateMode
  checked_at: string
  products: UpdateProduct[]
  install: UpdateInstall
}

export type EgressStatus = {
  enabled: boolean
  reason: string
  destination: string
  killed: boolean
  killed_by?: string
  killed_at?: string
  sandboxed?: boolean
}

export type EgressGrant = {
  id: number
  workspace_id: number
  agent_id: number
  agent_name: string
  granted_by_name: string
  granted_auth: string
  created_at: string
  // stale means the agent's role, model or bound context changed after a
  // human reviewed it, so the grant no longer applies.
  stale: boolean
}

export type EgressOverlap = { chars: number; source: string }

export type ApprovalRequest = {
  token: string
  workspace_id: number
  agent_id: number
  agent_name: string
  role_excerpt: string
  chain: string[]
  query: string
  // wire is the exact string that leaves the machine. It is the primary
  // artefact in the dialog, not the raw query, because they can differ.
  wire: string
  expires_at: string
  facts: {
    runes: number
    bytes: number
    non_ascii: number
    blob_shaped: boolean
    overlap: EgressOverlap[] | null
    used_this_turn: number
    max_per_turn: number
    used_24h: number
    max_24h: number
    granted_by: string
    granted_at: string
    prev_queries: string[] | null
  }
}

export type SearchRecord = {
  id: number
  agent_id: number | null
  agent_name: string
  chain: string[] | null
  query: string
  blob_flag: boolean
  overlap: string
  state: string
  decided_name: string
  decided_auth: string
  decided_at: string
  http_status: number | null
  result_bytes: number | null
  error: string
  created_at: string
}

// The other direction from egress: a door INTO a workspace. An inlet is not an
// agent — no model, no role, no memory. It has an address, its own key and a
// list of tasks, and a caller posting to POST /i/{address}/{task} runs one of
// them on one agent.
//
// The key exists in full exactly once, at the moment it is issued; only its
// hash is stored, and the hash never leaves the server. has_key is therefore
// everything this client can know about whether the door opens at all — an
// inlet with no key issued refuses every delivery.
export type Inlet = {
  id: number
  workspace_id: number
  address: string
  description: string
  has_key: boolean
  key_issued_at: string
  key_last_used_at: string
  created_at: string
  updated_at: string
  tasks: InletTask[]
}

// A task is how a caller selects a job. `accepts` decides which of the next two
// fields means anything: a JSON task is checked against `schema` before any
// model is called, and a file task's body must match `content_type` and lands
// in the workspace, where the agent is given its path rather than its bytes.
export type InletTask = {
  id: number
  inlet_id: number
  name: string
  accepts: 'json' | 'file'
  schema: string
  content_type: string
  agent_name: string
  instruction: string
  expect: InletExpect
  /** where the finished run is posted, or empty: nobody is told */
  callback_url: string
  created_at: string
  updated_at: string
}

// What defines a task, on the way in and on the way back in when it is fixed.
// One shape for both, because a task that can be created but not edited into
// the same state is a task with two definitions of what it may be.
export type InletTaskInput = {
  name: string
  accepts: 'json' | 'file'
  // The schema travels as a JSON value, never as a string containing JSON: the
  // server decodes this field as the schema object itself, so a string would
  // arrive as a string and be refused as one.
  schema?: unknown
  content_type?: string
  agent: string
  instruction: string
  // What this task requires of a run before its answer counts. Left out
  // entirely, the task is never judged and behaves as it always did.
  expect?: InletExpect
  callback_url?: string
}

// What a task declares success to be. Every field is optional, and a task that
// declares nothing runs exactly as tasks ran before any of this existed.
//
// The first two are checked against the run's RECORD and never against what the
// agent wrote, which is the whole point: a run whose answer is beautiful and
// whose record is empty fails. produces_files counts a file the agent typed out
// itself as well as a gear's output — both are files that appeared — so it is
// runs_gear, not a file count, that answers "did the real work happen".
export type InletExpect = {
  /** the least number of files that must appear during the run */
  produces_files?: number
  /** a gear that must have run and exited successfully, by its own name */
  runs_gear?: string
  /** a JSON Schema the answer must satisfy, in the subset payload schemas use */
  schema?: unknown
  /** where the result comes from; the agent's own words unless it is "gear" */
  answer_from?: 'agent' | 'gear'
}

// The record of what a run actually did, threaded out of the engine's own
// bookkeeping — which tools were called and how each ended, every file that
// appeared with its path and size, how many times a model was asked and what it
// cost. Nothing in it is written by a model, and nothing in it can be.
//
// An empty tools list is not a gap in the record: it is the answer to "did it
// do anything", and it is the shape of the failure this whole thing exists to
// make visible.
export type InletRunRecord = {
  tools: { name: string; ok: boolean; ms: number }[]
  files: { path: string; bytes: number }[]
  model_calls: number
  tokens: { in: number; out: number }
}

// The eight states the ledger enumerates, and nothing else — the table's CHECK
// says so. Two are live (accepted, running) and six are terminal.
//
// The last two are what a task's expect block produces, and they are separate
// on purpose: refused_expectation means the work did not happen, and
// refused_output_schema means it happened and the answer came back the wrong
// shape. Different people, different fixes, different hours of the night.
export type InletRunState =
  | 'accepted'
  // Waiting for its workspace to be free. The state this whole queue exists
  // for: a busy workspace used to make this `failed`, which is what a broken
  // job gets.
  | 'queued'
  | 'refused_schema'
  | 'running'
  | 'completed'
  | 'failed'
  | 'interrupted'
  | 'refused_expectation'
  | 'refused_output_schema'
  // Stopped at the token ceiling an operator set for one delivery. Its own
  // state so a caller can stop rather than retry.
  | 'refused_budget'

// One unit of queued work as an operator reads it. Deliberately without its
// args: a delivery's args carry the caller's payload, and a queue view is not a
// place to read other people's data.
export type QueueEntry = {
  unit: number
  kind: 'delivery' | 'chat' | 'callback'
  state: 'queued' | 'claimed'
  run?: number
  position: number
  since: string
  deadline?: string
}

export type QueueView = { queued: number; running: number; entries: QueueEntry[] }

// What a clock dials. `task` is the original and is still right when a job has
// a door as well as a clock; the other two exist because a task describes a
// DOOR — an inlet, an address, a key, a caller — and a schedule is not that.
export type ScheduleTarget = 'task' | 'agent' | 'gear'

export type Schedule = {
  id: number
  workspace_id: number
  target_kind: ScheduleTarget
  /** Set only for a task schedule. */
  task_id?: number
  /** Null on a BROKEN schedule: deleting an agent or a gear nulls the target
   *  rather than cascading the schedule away, so a nightly job that lost what
   *  it dialled shows as broken instead of vanishing. */
  target_agent_id?: number
  target_gear_id?: number
  /** The sentence an agent target is given; the arguments a gear is called
   *  with, held against that gear's schema when the schedule is saved. */
  instruction?: string
  args?: string
  name: string
  spec: string
  tz: string
  payload: string
  enabled: boolean
  on_miss: 'skip' | 'run'
  next_at: string
  last_work_id?: number
  last_fired_at?: string
  last_outcome?: 'fired' | 'skipped' | 'failed'
  fires: number
  skips: number
  /** Resolved by the server, because the row cannot answer either on its own:
   *  what this dials by name, and which agent node an edge should land on —
   *  including for a task schedule, whose agent is named by the task. */
  target_name?: string
  edge_agent_id?: number
  broken: boolean
}

export type NewSchedule = {
  target_kind?: ScheduleTarget
  name: string
  spec: string
  tz?: string
  on_miss?: 'skip' | 'run'
  /** task */
  task_id?: number
  payload?: unknown
  /** agent */
  target_agent_id?: number
  instruction?: string
  /** gear */
  target_gear_id?: number
  args?: unknown
}

// One delivery, whatever became of it. inlet_id and agent_id are nullable
// because the row outlives both: a ledger that disappeared when the door was
// deleted could not answer "did job 4471 happen", which is the only reason it
// exists.
// What to ask the record. Every field is optional; an empty query is the plain
// listing. tool matches a tool the run CALLED (a gear appears under its tool
// name), agent matches any agent that worked in it anywhere in the delegation
// tree, context a document it read, file a file it produced.
export type RunQuery = {
  tool?: string
  agent?: string
  context?: string
  file?: string
  state?: string
  failed?: boolean
}

export type InletRun = {
  id: number
  workspace_id: number
  inlet_id: number | null
  inlet_address: string
  task_name: string
  agent_id: number | null
  agent_name: string
  payload_bytes: number
  payload_path: string
  state: InletRunState
  result: string
  error: string
  // What the run did. Null means no record was kept — the row is still in
  // flight, or it predates the record entirely — which is deliberately NOT the
  // same as a record showing nothing happened.
  did: InletRunRecord | null
  created_at: string
  updated_at: string
}

// What comes back from issuing a key: the one copy of it, and the server's own
// sentence about what that means. The notice differs between the first key and
// a replacement, so it is shown rather than restated here.
export type IssuedInletKey = { key: string; notice: string }

// inletDeliveryUrl is the address an operator hands to whoever will be calling.
// It is built from the server this client is talking to, because the inlet row
// knows its own address and nothing about the host a caller has to reach — and
// a path with no host is not something anyone can paste into curl.
export function inletDeliveryUrl(address: string, taskName: string): string {
  const path = `/i/${address}/${taskName}`
  const base = session.server()
  return base ? base + path : new URL(path, window.location.href).toString()
}

export type AgentUsage = {
  agent_id: number
  input_tokens: number
  output_tokens: number
  turns: number
  unreported_turns: number
  last_at?: string
}
export type Team = { id: number; name: string }

export const auth = {
  whoami: () => req<User>('/api/v1/whoami'),
  login: (name: string, password: string) =>
    req<{ user: User; token: string }>('/api/v1/login', {
      method: 'POST',
      body: JSON.stringify({ name, password }),
    }),
  logout: () => req<void>('/api/v1/logout', { method: 'POST' }),
  setPassword: (id: number, password: string) =>
    req<void>(`/api/v1/users/${id}/password`, { method: 'PUT', body: JSON.stringify({ password }) }),
  users: () => req<User[]>('/api/v1/users'),
  createUser: (u: { name: string; role: string; password?: string }) =>
    req<{ user: User; token: string; notice: string }>('/api/v1/users', {
      method: 'POST',
      body: JSON.stringify(u),
    }),
  deleteUser: (id: number) => req<void>(`/api/v1/users/${id}`, { method: 'DELETE' }),
  teams: () => req<Team[]>('/api/v1/teams'),
  createTeam: (name: string) =>
    req<Team>('/api/v1/teams', { method: 'POST', body: JSON.stringify({ name }) }),
  deleteTeam: (id: number) => req<void>(`/api/v1/teams/${id}`, { method: 'DELETE' }),
  addMember: (teamId: number, userId: number) =>
    req<void>(`/api/v1/teams/${teamId}/members`, { method: 'POST', body: JSON.stringify({ user_id: userId }) }),
  removeMember: (teamId: number, userId: number) =>
    req<void>(`/api/v1/teams/${teamId}/members/${userId}`, { method: 'DELETE' }),
}

export const api = {
  providers: {
    list: () => req<Provider[]>('/api/v1/providers'),
    create: (p: { name: string; type: string; base_url: string; api_key: string }) =>
      req<Provider>('/api/v1/providers', { method: 'POST', body: JSON.stringify(p) }),
    remove: (id: number) => req<void>(`/api/v1/providers/${id}`, { method: 'DELETE' }),
    test: (id: number) => req<TestResult>(`/api/v1/providers/${id}/test`, { method: 'POST' }),
  },
  models: {
    list: () => req<Model[]>('/api/v1/models'),
    create: (m: { provider_id: number; model_name: string; label: string }) =>
      req<Model>('/api/v1/models', { method: 'POST', body: JSON.stringify(m) }),
    remove: (id: number) => req<void>(`/api/v1/models/${id}`, { method: 'DELETE' }),
  },
  workspaces: {
    list: () => req<Workspace[]>('/api/v1/workspaces'),
    get: (id: number) => req<Workspace>(`/api/v1/workspaces/${id}`),
    create: (w: { name: string; description: string; orchestrator_model_id: number }) =>
      req<Workspace>('/api/v1/workspaces', { method: 'POST', body: JSON.stringify(w) }),
    remove: (id: number) => req<void>(`/api/v1/workspaces/${id}`, { method: 'DELETE' }),
    // null clears the colour. Omitting the field is a 400 rather than a
    // silent no-op, so there is deliberately no way to call this with nothing.
    colour: (id: number, hue: number | null) =>
      req<Workspace>(`/api/v1/workspaces/${id}`, { method: 'PATCH', body: JSON.stringify({ hue }) }),
    clone: (id: number, name: string) =>
      req<Workspace>(`/api/v1/workspaces/${id}/clone`, { method: 'POST', body: JSON.stringify({ name }) }),
    share: (id: number, teamId: number) =>
      req<Workspace>(`/api/v1/workspaces/${id}/teams`, {
        method: 'POST',
        body: JSON.stringify({ team_id: teamId }),
      }),
    unshare: (id: number, teamId: number) =>
      req<Workspace>(`/api/v1/workspaces/${id}/teams/${teamId}`, { method: 'DELETE' }),
    // The bundle is a download, and the token lives in localStorage rather
    // than a cookie, so a plain link to this route would arrive unauthenticated
    // — the document has to come back through the same authenticated path as
    // everything else and be handed to the browser as a file.
    exportBundle: (id: number, opts: { gears: boolean; context: boolean }) => {
      const p = new URLSearchParams({ gears: opts.gears ? '1' : '0', context: opts.context ? '1' : '0' })
      return fetch(session.url(`/api/v1/workspaces/${id}/export?${p}`), {
        headers: session.headers(),
      }).then(async (r) => {
        if (!r.ok) {
          const body = await r.json().catch(() => null)
          throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
        }
        // The server's own bytes are what gets saved, never a re-serialization
        // of them: what an operator hands to someone else should be the
        // document this install produced, character for character.
        return { text: await r.text(), filename: bundleFilename(r.headers.get('Content-Disposition')) }
      })
    },
    importBundle: (b: { name: string; bundle: WorkspaceBundle; include_gears: boolean; include_context: boolean }) =>
      req<ImportReport>('/api/v1/workspaces/import', { method: 'POST', body: JSON.stringify(b) }),
    agents: (id: number) => req<Agent[]>(`/api/v1/workspaces/${id}/agents`),
    createAgent: (id: number, a: { name: string; role: string; model_id: number }) =>
      req<Agent>(`/api/v1/workspaces/${id}/agents`, { method: 'POST', body: JSON.stringify(a) }),
    messages: (id: number, agentId?: number) =>
      req<WSMessage[]>(`/api/v1/workspaces/${id}/messages${agentId ? `?agent_id=${agentId}` : ''}`),
    status: (id: number) => req<AgentStatus[]>(`/api/v1/workspaces/${id}/status`),
    // attach uploads one file into the workspace and answers with what will
    // become of it. The body is the file itself rather than JSON, so this
    // cannot go through req(): the Content-Type req() sets would be a lie, and
    // base64 in a JSON field would cost a third of the bytes for nothing.
    //
    // One file per call. Several attachments are several calls, which is also
    // how the composer can report the third one failing while keeping the two
    // that landed.
    attach: (id: number, file: File) =>
      fetch(session.url(`/api/v1/workspaces/${id}/attachments`), {
        method: 'POST',
        // The filename travels in the multipart part, which is the only place
        // the server can learn it — a raw body has no name, and the name is
        // what gives the file its extension in the workspace.
        headers: session.headers(),
        body: form(file),
      }).then(async (r) => {
        if (r.status === 401) throw new Unauthorized('sign in required')
        const body = await r.json().catch(() => null)
        if (!r.ok) throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
        return body as Attachment
      }),
  },
  agents: {
    update: (
      id: number,
      // avoid is sent as "" to clear it, so it is a value the caller means
      // rather than a field it may omit — an absent avoid leaves the agent's
      // prohibitions exactly as they were.
      patch: { name?: string; role?: string; avoid?: string; model_id?: number; pos_x?: number; pos_y?: number },
    ) =>
      req<Agent>(`/api/v1/agents/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
    remove: (id: number) => req<void>(`/api/v1/agents/${id}`, { method: 'DELETE' }),
    prompt: (id: number) => req<{ prompt: string }>(`/api/v1/agents/${id}/prompt`),
    memory: (id: number) => req<MemoryItem[]>(`/api/v1/agents/${id}/memory`),
  },
  messages: {
    forget: (id: number) => req<void>(`/api/v1/messages/${id}`, { method: 'DELETE' }),
  },
  wires: {
    list: (wsId: number) => req<Wire[]>(`/api/v1/workspaces/${wsId}/wires`),
    create: (wsId: number, from: number, to: number, label = '') =>
      req<Wire>(`/api/v1/workspaces/${wsId}/wires`, {
        method: 'POST',
        body: JSON.stringify({ from_agent_id: from, to_agent_id: to, label }),
      }),
    remove: (id: number) => req<void>(`/api/v1/wires/${id}`, { method: 'DELETE' }),
  },
  gears: {
    list: (q = '', tag = '') => {
      const p = new URLSearchParams()
      if (q) p.set('q', q)
      if (tag) p.set('tag', tag)
      const qs = p.toString()
      return req<Gear[]>(`/api/v1/gears${qs ? `?${qs}` : ''}`)
    },
    // One request, because it is one screen and one decision: the source, the
    // credentials this code would be given, and whether it may reach out. Two
    // requests that can fail independently is a way of showing them apart, and
    // shown apart the decision is made blind.
    get: (id: number, version?: number) =>
      req<{ gear: Gear; files: GearFile[]; version: number; env: EnvStatus[] }>(
        `/api/v1/gears/${id}${version ? `?version=${version}` : ''}`,
      ),
    create: (g: {
      name: string
      description: string
      runtime: string
      code?: string
      entrypoint?: string
      files?: GearFile[]
      tags?: string[]
      args_schema?: string
      env_names?: string[]
    }) => req<Gear>('/api/v1/gears', { method: 'POST', body: JSON.stringify(g) }),
    // Approving and granting travel together, for the same reason they are
    // shown together: between two requests there is a moment when the code is
    // runnable and the operator has not finished saying what it may do.
    setStatus: (id: number, status: 'pending' | 'approved' | 'disabled', network?: GearNetwork) =>
      req<Gear>(`/api/v1/gears/${id}`, { method: 'PATCH', body: JSON.stringify({ status, network }) }),
    setNetwork: (id: number, network: GearNetwork) =>
      req<Gear>(`/api/v1/gears/${id}`, { method: 'PATCH', body: JSON.stringify({ network }) }),
    setTimeout: (id: number, timeout_seconds: number) =>
      req<Gear>(`/api/v1/gears/${id}`, { method: 'PATCH', body: JSON.stringify({ timeout_seconds }) }),
    // network is the grant being CONSIDERED, so a gear whose whole job is to
    // fetch something can still be judged by what it does. Nothing is granted
    // by trying it; the connections are recorded like any other.
    run: (id: number, args: unknown, network?: GearNetwork) =>
      req<GearRunResult>(`/api/v1/gears/${id}/run`, { method: 'POST', body: JSON.stringify({ args, network }) }),
    runs: (id: number) => req<GearRun[]>(`/api/v1/gears/${id}/runs`),
    connections: (id: number) => req<GearConnection[]>(`/api/v1/gears/${id}/connections`),
    approvals: (id: number) => req<GearApproval[]>(`/api/v1/gears/${id}/approvals`),
    remove: (id: number) => req<void>(`/api/v1/gears/${id}`, { method: 'DELETE' }),
    bindings: (wsId: number) => req<GearBinding[]>(`/api/v1/workspaces/${wsId}/gears`),
    bind: (wsId: number, gearId: number, agentId: number | null) =>
      req<GearBinding>(`/api/v1/workspaces/${wsId}/gears`, {
        method: 'POST',
        body: JSON.stringify({ gear_id: gearId, agent_id: agentId }),
      }),
    unbind: (bindingId: number) => req<void>(`/api/v1/gear-bindings/${bindingId}`, { method: 'DELETE' }),
  },
  // The named values a gear is given at run time. A gear is given NAMES and
  // reads the VALUES from its own environment; nothing here ever sends a
  // secret's value back, in either direction, after it has been set.
  //
  // The install-wide scope is the administrator's, on the same reasoning as
  // gear approval: one name here reaches every workspace. A workspace's own
  // overrides go through the same access rule as everything else in it.
  env: {
    list: () => req<{ values: EnvRecord[]; sources: EnvSources }>('/api/v1/env'),
    set: (name: string, v: { kind: string; value: string; description?: string }) =>
      req<EnvRecord>(`/api/v1/env/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(v) }),
    remove: (name: string) => req<void>(`/api/v1/env/${encodeURIComponent(name)}`, { method: 'DELETE' }),
    listWorkspace: (wsId: number) =>
      req<{ values: EnvRecord[]; sources: EnvSources }>(`/api/v1/workspaces/${wsId}/env`),
    setWorkspace: (wsId: number, name: string, v: { kind: string; value: string; description?: string }) =>
      req<EnvRecord>(`/api/v1/workspaces/${wsId}/env/${encodeURIComponent(name)}`, {
        method: 'PUT',
        body: JSON.stringify(v),
      }),
    // Removing an override uncovers the install-wide value again rather than
    // leaving a hole: a gear here does not stop working, it starts seeing the
    // other value. Worth knowing before deleting.
    removeWorkspace: (wsId: number, name: string) =>
      req<void>(`/api/v1/workspaces/${wsId}/env/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  },
  instructions: {
    list: (q = '', tag = '') => {
      const p = new URLSearchParams()
      if (q) p.set('q', q)
      if (tag) p.set('tag', tag)
      const qs = p.toString()
      return req<Instruction[]>(`/api/v1/instructions${qs ? `?${qs}` : ''}`)
    },
    get: (id: number) => req<{ instruction: Instruction; text: string }>(`/api/v1/instructions/${id}`),
    save: (i: { name: string; description: string; text: string; tags?: string[] }) =>
      req<Instruction>('/api/v1/instructions', { method: 'POST', body: JSON.stringify(i) }),
    remove: (id: number) => req<void>(`/api/v1/instructions/${id}`, { method: 'DELETE' }),
  },
  terminal: {
    status: () => req<TerminalStatus>('/api/v1/terminal/status'),
  },
  graph: {
    workspace: (wsId: number) => req<GraphData>(`/api/v1/workspaces/${wsId}/graph`),
    map: () => req<GraphData>('/api/v1/map'),
  },
  usage: {
    workspace: (wsId: number) => req<AgentUsage[]>(`/api/v1/workspaces/${wsId}/usage`),
    agent: (agentId: number) => req<AgentUsage>(`/api/v1/agents/${agentId}/usage`),
  },
  // Whether a newer release exists. Reading never triggers a request: the
  // server holds the last answer and asks GitHub at most once a day, so a rail
  // that rendered on every navigation cannot rate-limit a team out of the API.
  updates: {
    status: () => req<UpdateReport>('/api/v1/updates'),
    // Asks now. Works while the setting is still `ask` — one press is one look
    // — and is refused when the config file says off.
    checkNow: () => req<UpdateReport>('/api/v1/updates/check', { method: 'POST' }),
    setMode: (mode: UpdateMode) =>
      req<UpdateReport>('/api/v1/updates/mode', { method: 'PUT', body: JSON.stringify({ mode }) }),
  },

  egress: {
    status: () => req<EgressStatus>('/api/v1/egress/status'),
    kill: () => req<{ killed: boolean; notice: string }>('/api/v1/egress/off', { method: 'POST' }),
    grants: (wsId: number) =>
      req<{ enabled: boolean; destination: string; grants: EgressGrant[]; reach: Record<string, string[]> }>(
        `/api/v1/workspaces/${wsId}/egress`,
      ),
    grant: (wsId: number, agentId: number) =>
      req<EgressGrant>(`/api/v1/workspaces/${wsId}/egress`, {
        method: 'POST',
        body: JSON.stringify({ agent_id: agentId }),
      }),
    revoke: (grantId: number) => req<void>(`/api/v1/egress-grants/${grantId}`, { method: 'DELETE' }),
    pending: (wsId: number) => req<ApprovalRequest | null>(`/api/v1/workspaces/${wsId}/egress/pending`),
    answer: (token: string, allow: boolean) =>
      req<{ allow: boolean }>(`/api/v1/egress/approvals/${encodeURIComponent(token)}`, {
        method: 'POST',
        body: JSON.stringify({ allow }),
      }),
    log: (wsId: number, limit = 100) =>
      req<SearchRecord[]>(`/api/v1/workspaces/${wsId}/egress/log?limit=${limit}`),
  },
  // Management, all of it behind the same authentication and the same access
  // rule as the workspace the inlet belongs to. Delivery is the other half and
  // is not reachable from here: it lives at /i/… and proves itself with the
  // inlet's own key rather than with this operator's token.
  // What is waiting and what is running in one workspace, and the button that
  // stops either. A queue nobody can see is discovered by being refused by it.
  queue: {
    show: (wsId: number) => req<QueueView>(`/api/v1/workspaces/${wsId}/queue`),
    // Stops a unit whether it is waiting or already running: the row AND the
    // work. A cancel that only relabelled the row would leave the model
    // answering for a job somebody had stopped.
    cancel: (unitId: number) => req<void>(`/api/v1/queue/${unitId}`, { method: 'DELETE' }),
  },

  schedules: {
    list: (wsId: number) => req<Schedule[]>(`/api/v1/workspaces/${wsId}/schedules`),
    create: (wsId: number, body: NewSchedule) =>
      req<Schedule>(`/api/v1/workspaces/${wsId}/schedules`, { method: 'POST', body: JSON.stringify(body) }),
    setEnabled: (id: number, enabled: boolean) =>
      req<Schedule>(`/api/v1/schedules/${id}`, { method: 'PATCH', body: JSON.stringify({ enabled }) }),
    remove: (id: number) => req<void>(`/api/v1/schedules/${id}`, { method: 'DELETE' }),
    // Fires it by hand without moving its clock — the first thing anybody wants
    // after writing one is to know whether it works, and waiting until 02:00 to
    // find out is how a broken job stays broken for a day.
    runNow: (id: number) => req<{ unit: number }>(`/api/v1/schedules/${id}/run`, { method: 'POST' }),
  },

  inlets: {
    list: (wsId: number) => req<Inlet[]>(`/api/v1/workspaces/${wsId}/inlets`),
    // Opening a door issues its first key in the same call, and this response
    // is the only place that key will ever exist in full.
    create: (wsId: number, address: string, description: string) =>
      req<IssuedInletKey & { inlet: Inlet }>(`/api/v1/workspaces/${wsId}/inlets`, {
        method: 'POST',
        body: JSON.stringify({ address, description }),
      }),
    remove: (id: number) => req<void>(`/api/v1/inlets/${id}`, { method: 'DELETE' }),
    // Issuing again is how a leaked key is closed: the previous string stops
    // working the moment this returns, and the door keeps its tasks.
    issueKey: (id: number) => req<IssuedInletKey>(`/api/v1/inlets/${id}/key`, { method: 'POST' }),
    addTask: (inletId: number, t: InletTaskInput) =>
      req<InletTask>(`/api/v1/inlets/${inletId}/tasks`, { method: 'POST', body: JSON.stringify(t) }),
    // PUT, not PATCH: the body is the whole task. Sending only what changed
    // would leave the server deciding what an absent schema means, and the
    // honest answer — "accept anything" — is not something that may happen
    // because a field was left out of a request.
    updateTask: (id: number, t: InletTaskInput) =>
      req<InletTask>(`/api/v1/inlet-tasks/${id}`, { method: 'PUT', body: JSON.stringify(t) }),
    removeTask: (id: number) => req<void>(`/api/v1/inlet-tasks/${id}`, { method: 'DELETE' }),
    // The same endpoint answers the plain listing and the questions the record
    // exists for. With no filter it IS the listing.
    runs: (wsId: number, limit = 50, q: RunQuery = {}) => {
      const p = new URLSearchParams({ limit: String(limit) })
      for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v))
      return req<InletRun[]>(`/api/v1/workspaces/${wsId}/inlet-runs?${p.toString()}`)
    },
    // One run by number. The list only reaches back so far, and the number a
    // caller quotes is often older than that.
    run: (id: number) => req<InletRun>(`/api/v1/inlet-runs/${id}`),
  },
  files: {
    list: (wsId: number, path: string) =>
      req<FileEntry[]>(`/api/v1/workspaces/${wsId}/files?path=${encodeURIComponent(path)}`),
    read: (wsId: number, path: string) =>
      req<{ path: string; content: string; mtime: string }>(
        `/api/v1/workspaces/${wsId}/file?path=${encodeURIComponent(path)}`,
      ),
    // Sent as text/plain rather than JSON so the file's bytes are the body,
    // not a string escaped inside an envelope.
    write: (wsId: number, path: string, content: string) =>
      fetch(session.url(`/api/v1/workspaces/${wsId}/file?path=${encodeURIComponent(path)}`), {
        method: 'PUT',
        headers: session.headers({ 'Content-Type': 'text/plain' }),
        body: content,
      }).then(async (r) => {
        const body = await r.json().catch(() => null)
        if (!r.ok) throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
        return body as { path: string; size: number; mtime: string }
      }),
  },
  context: {
    status: () => req<ContextStatus>('/api/v1/context/status'),
    files: () => req<ContextFile[]>('/api/v1/context/files'),
    // version travels with the body: the editor hands it back on save, which
    // is what lets a save be refused instead of quietly overwriting somebody
    // else's work. Empty means the version could not be determined, and an
    // empty version saves unguarded rather than pretending to be guarded.
    get: (path: string) =>
      req<{ path: string; content: string; version: string }>(
        `/api/v1/context/file?path=${encodeURIComponent(path)}`,
      ),
    // A soft delete: contextd keeps every version and can restore it. The
    // version is the one the editor read, so removing a document somebody has
    // just rewritten is refused the same way overwriting it is.
    remove: (path: string, version = '') =>
      req<{ path: string; status: string }>(
        `/api/v1/context/file?path=${encodeURIComponent(path)}` +
          (version ? `&version=${encodeURIComponent(version)}` : ''),
        { method: 'DELETE' },
      ),
    search: (q: string, pathGlob = '', limit = 100) => {
      const p = new URLSearchParams({ q })
      if (pathGlob) p.set('path', pathGlob)
      p.set('limit', String(limit))
      return req<ContextSearch>(`/api/v1/context/search?${p.toString()}`)
    },
    put: (path: string, content: string, version = '') =>
      fetch(session.url(
        `/api/v1/context/file?path=${encodeURIComponent(path)}` +
          (version ? `&version=${encodeURIComponent(version)}` : ''),
      ), {
        method: 'PUT',
        headers: session.headers({ 'Content-Type': 'text/plain' }),
        body: content,
      }).then(async (r) => {
        const body = await r.json().catch(() => null)
        if (!r.ok) throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
        return body as { path: string; status: string }
      }),
    bindings: (wsId: number) => req<ContextBinding[]>(`/api/v1/workspaces/${wsId}/context`),
    bind: (wsId: number, path: string, agentId: number | null) =>
      req<ContextBinding>(`/api/v1/workspaces/${wsId}/context`, {
        method: 'POST',
        body: JSON.stringify({ path, agent_id: agentId }),
      }),
    unbind: (bindingId: number) => req<void>(`/api/v1/context-bindings/${bindingId}`, { method: 'DELETE' }),
  },
}

// wsChatStream sends one operator message into a workspace and feeds every
// engine event (messages, deltas, statuses) back until done/error.
export async function wsChatStream(
  workspaceId: number,
  text: string,
  // Workspace-relative paths of files already uploaded with api.workspaces
  // .attach. The bytes are in the workspace, not in this request — the
  // message points at them.
  attachments: string[],
  onEvent: (ev: WSEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const r = await fetch(session.url(`/api/v1/workspaces/${workspaceId}/chat`), {
    method: 'POST',
    headers: session.headers({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ text, attachments }),
    signal,
  })
  if (!r.ok || !r.body) {
    const body = await r.json().catch(() => null)
    throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
  }

  const reader = r.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) return
    buf += decoder.decode(value, { stream: true })
    for (;;) {
      const sep = buf.indexOf('\n\n')
      if (sep === -1) break
      const block = buf.slice(0, sep)
      buf = buf.slice(sep + 2)
      const data = block
        .split('\n')
        .filter((l) => l.startsWith('data: '))
        .map((l) => l.slice(6))
        .join('\n')
      if (!data) continue
      const ev = JSON.parse(data) as WSEvent
      onEvent(ev)
      if (ev.type === 'error' && ev.error) throw new Error(ev.error)
      if (ev.type === 'done') return
    }
  }
}

import { session } from './session'

export type Workspace = {
  id: number
  name: string
  description: string
  branch: string
  shared_branch: string
  owner_id: number | null
  team_ids: number[]
  // The colour somebody gave this workspace, in degrees, or null when nobody
  // has. Null is not "grey" — only an uncoloured workspace may be given one
  // derived from its id, and this is what says it is allowed to be.
  hue: number | null
}

// A portable workspace: agents, wiring, and optionally the source of its gears
// and its context documents. Only the parts the client actually reads before
// an import are declared — the name it offers as a default, and how much of
// each kind is in the file. Every other field travels to the server untouched,
// because the server is the only thing that decides what a bundle means, and a
// second definition of the format here is a second one to keep in step.
export type WorkspaceBundle = {
  workspace?: { name?: string }
  agents?: unknown[]
  wires?: unknown[]
  gears?: unknown[]
  context?: unknown[]
}

// ImportReport is what an import actually did. unresolved_models and
// gears_skipped are the reason it exists: neither one fails the import, and
// both change what the new workspace can do, so an operator who is not shown
// them finds out when an agent turns out to have no model bound.
export type ImportReport = {
  workspace: Workspace
  agents: number
  wires: number
  gears_imported: string[]
  gears_skipped: { name: string; why: string }[]
  context_files: number
  unresolved_models: { agent: string; provider_type: string; model_name: string }[]
}

export type Agent = {
  id: number
  workspace_id: number
  name: string
  kind: string
  role: string
  // avoid is the operator's standing prohibitions, one rule per line. It is
  // the last section of the agent's system prompt.
  avoid: string
  model_id: number | null
  model_label: string
  is_orchestrator: boolean
  pos_x: number | null
  pos_y: number | null
  branch: string
}

export type Wire = { id: number; workspace_id: number; from_agent_id: number; to_agent_id: number; label: string }

export type WSMessage = {
  id: number
  workspace_id: number
  agent_id: number | null
  kind: 'user' | 'assistant' | 'tool_call' | 'tool_result' | 'delegation' | 'error'
  content: string
  meta: string
  created_at: string
}

export type AgentStatus = { agent_id: number; state: string; detail: string; since?: string }

// A file attached to a message: where it is in the workspace, and what became
// of it. kind is empty when the model was not shown the bytes, and skipped
// then says why — a zip nobody's model can look inside, or a file past the
// size a model may be sent. Either way the agent has the path.
export type Attachment = {
  path: string
  media_type: string
  bytes: number
  kind?: string
  skipped?: string
  // Set only on a freshly uploaded file, and only when the model this
  // workspace answers with was never declared able to take this kind: the
  // message would be refused, and this says so while it can still be changed.
  warning?: string
}

export type ContextFile = { path: string; version: string }

export type ContextStatus = {
  available: boolean
  space_root?: string
  mode?: string
  error?: string
}

export type ContextBinding = { id: number; workspace_id: number; path: string; agent_id: number | null }

// What a search found. `truncated` matters: a cut answer that does not say it
// was cut reads as "there is nothing else", which is the one wrong answer a
// search can give.
export type ContextSearch = {
  query: string
  matches: { path: string; line: number; text: string }[]
  files_matched: number
  files_scanned: number
  truncated: boolean
}

// One decision about one gear: who said this code may run, when, to which
// version, and what they granted it in the same breath. The status column says
// what a gear IS; this says how it got that way.
export type GearApproval = {
  id: number
  gear_id: number
  gear_name: string
  version: number
  status: 'approved' | 'disabled' | 'pending'
  user_id: number | null
  user_name: string
  env_names: string
  network: string
  created_at: string
}

export type Gear = {
  id: number
  name: string
  description: string
  tags: string[]
  origin_workspace_id: number | null
  origin_workspace: string
  created_by_agent_id: number | null
  created_by_agent: string
  version: number
  runtime: string
  entrypoint: string
  args_schema: string
  // The names this gear asks to be given at run time. Names only — a value
  // never travels in this direction, which is what makes a gear safe to show.
  env_names: string[]
  // What the operator granted at approval: whether this code may reach out,
  // and where. An empty hosts list with the grant on means anywhere.
  network_granted: boolean
  network_hosts: string[]
  status: 'pending' | 'approved' | 'disabled'
  timeout_seconds: number
  updated_at: string
}

export type GearNetwork = { granted: boolean; hosts: string[] }

// What one declared name would resolve to on this install: whether anything
// supplies it, which kind it would be, and which source would win. Never a
// value — the type has nowhere to put one, and that is deliberate.
export type EnvStatus = { name: string; found: boolean; kind: string; source: string }

// One named value as the interface sees it. `value` carries a variable's value
// and is ALWAYS empty for a secret: a secret is shown once, when it is set, and
// never again. It is not optional, so "this is a secret" and "the server did
// not send this field" stay tellable apart.
export type EnvRecord = {
  id: number
  workspace_id: number | null
  name: string
  kind: 'variable' | 'secret'
  value: string
  description: string
  created_at: string
  updated_at: string
}

// Whether a secret can be stored here at all, and which directories are also
// being read. An operator looking at a value that is not the one they set has
// to know a directory exists before they can suspect it.
export type EnvSources = { can_store_secrets: boolean; variables_dir: string; secrets_dir: string }

// One connection a granted gear opened, whatever became of it. There is no
// path, no query string and no header here on purpose: a gear that put a
// credential in a URL would otherwise have written it into the audit log.
export type GearConnection = {
  id: number
  gear_name: string
  version: number
  workspace_id: number | null
  agent_name: string
  host: string
  port: number
  method: string
  // The grant as it was AT THE TIME, not as it is now — the gear's list will
  // change, and the question afterwards is what the rule was.
  allowed: string[]
  state:
    | 'open'
    | 'closed'
    | 'failed'
    | 'interrupted'
    | 'refused_destination'
    | 'refused_local'
    | 'refused_no_grant'
    | 'refused_invalid'
  bytes_sent: number
  bytes_received: number
  error: string
  created_at: string
}

export type GearRun = {
  id: number
  gear_id: number
  version: number
  agent_id: number | null
  agent_name: string
  args: string
  exit_code: number
  timed_out: boolean
  duration_ms: number
  stdout: string
  stderr: string
  created_at: string
}

export type MemoryItem = {
  kind: 'role' | 'private' | 'shared' | 'bound' | 'instruction'
  source: string
  content: string
  editable: boolean
  removable: boolean
  binding_id?: number
  description: string
}

export type Instruction = {
  id: number
  name: string
  description: string
  tags: string[]
  path: string
  origin_workspace: string
  created_by_agent: string
  updated_at: string
}

export type TerminalStatus = {
  available: boolean
  global_available: boolean
  reason: string
  global_reason: string
  backend: string
}

export type GearRunResult = {
  stdout: string
  stderr: string
  exit_code: number
  timed_out: boolean
  error?: string
}

export type GearFile = { path: string; content: string; encoding?: 'utf8' | 'base64' }

export type GearBinding = {
  id: number
  gear_id: number
  gear_name: string
  workspace_id: number
  agent_id: number | null
}

export type WSEvent = {
  type: 'message' | 'delta' | 'status' | 'done' | 'error'
  message?: WSMessage
  agent_id?: number
  text?: string
  status?: AgentStatus
  error?: string
}

export type ChatStreamResult = { truncated: boolean; stopReason: string }

/**
 * A gear run, reported as it happens.
 *
 * The same shape as the chat stream and for the same reason: the run lives
 * inside the request that started it, so there is no separate subscription to
 * get wrong and nothing to clean up if the operator navigates away — dropping
 * the connection is dropping the watch.
 *
 * The final `result` event carries exactly what the buffered endpoint returns,
 * so a caller can ignore every chunk and still be correct.
 */
export type GearRunEvent =
  | { type: 'output'; stream: 'stdout' | 'stderr'; text: string }
  | ({ type: 'result' } & GearRunResult)

export async function gearRunStream(
  gearId: number,
  args: unknown,
  onEvent: (ev: GearRunEvent) => void,
  signal?: AbortSignal,
  // The grant being considered, so the run the operator judges is the run they
  // are about to allow. Left out, the gear runs with no network, which is what
  // every dry run did before there was anything to grant.
  network?: GearNetwork,
): Promise<void> {
  const r = await fetch(session.url(`/api/v1/gears/${gearId}/run?stream=1`), {
    method: 'POST',
    headers: session.headers({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ args, network }),
    signal,
  })
  if (!r.ok || !r.body) {
    const body = await r.json().catch(() => null)
    throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
  }

  const reader = r.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) return
    buf += decoder.decode(value, { stream: true })
    for (;;) {
      const sep = buf.indexOf('\n\n')
      if (sep === -1) break
      const block = buf.slice(0, sep)
      buf = buf.slice(sep + 2)
      const data = block
        .split('\n')
        .filter((l) => l.startsWith('data: '))
        .map((l) => l.slice(6))
        .join('\n')
      if (!data) continue
      onEvent(JSON.parse(data) as GearRunEvent)
    }
  }
}
