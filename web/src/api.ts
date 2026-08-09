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
    clone: (id: number, name: string) =>
      req<Workspace>(`/api/v1/workspaces/${id}/clone`, { method: 'POST', body: JSON.stringify({ name }) }),
    share: (id: number, teamId: number) =>
      req<Workspace>(`/api/v1/workspaces/${id}/teams`, {
        method: 'POST',
        body: JSON.stringify({ team_id: teamId }),
      }),
    unshare: (id: number, teamId: number) =>
      req<Workspace>(`/api/v1/workspaces/${id}/teams/${teamId}`, { method: 'DELETE' }),
    agents: (id: number) => req<Agent[]>(`/api/v1/workspaces/${id}/agents`),
    createAgent: (id: number, a: { name: string; role: string; model_id: number }) =>
      req<Agent>(`/api/v1/workspaces/${id}/agents`, { method: 'POST', body: JSON.stringify(a) }),
    messages: (id: number, agentId?: number) =>
      req<WSMessage[]>(`/api/v1/workspaces/${id}/messages${agentId ? `?agent_id=${agentId}` : ''}`),
    status: (id: number) => req<AgentStatus[]>(`/api/v1/workspaces/${id}/status`),
  },
  agents: {
    update: (id: number, patch: { name?: string; role?: string; model_id?: number; pos_x?: number; pos_y?: number }) =>
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
    get: (id: number) => req<{ gear: Gear; files: GearFile[] }>(`/api/v1/gears/${id}`),
    create: (g: {
      name: string
      description: string
      runtime: string
      code?: string
      entrypoint?: string
      files?: GearFile[]
      tags?: string[]
      args_schema?: string
    }) => req<Gear>('/api/v1/gears', { method: 'POST', body: JSON.stringify(g) }),
    setStatus: (id: number, status: 'pending' | 'approved' | 'disabled') =>
      req<Gear>(`/api/v1/gears/${id}`, { method: 'PATCH', body: JSON.stringify({ status }) }),
    setTimeout: (id: number, timeout_seconds: number) =>
      req<Gear>(`/api/v1/gears/${id}`, { method: 'PATCH', body: JSON.stringify({ timeout_seconds }) }),
    run: (id: number, args: unknown) =>
      req<GearRunResult>(`/api/v1/gears/${id}/run`, { method: 'POST', body: JSON.stringify({ args }) }),
    runs: (id: number) => req<GearRun[]>(`/api/v1/gears/${id}/runs`),
    remove: (id: number) => req<void>(`/api/v1/gears/${id}`, { method: 'DELETE' }),
    bindings: (wsId: number) => req<GearBinding[]>(`/api/v1/workspaces/${wsId}/gears`),
    bind: (wsId: number, gearId: number, agentId: number | null) =>
      req<GearBinding>(`/api/v1/workspaces/${wsId}/gears`, {
        method: 'POST',
        body: JSON.stringify({ gear_id: gearId, agent_id: agentId }),
      }),
    unbind: (bindingId: number) => req<void>(`/api/v1/gear-bindings/${bindingId}`, { method: 'DELETE' }),
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
    get: (path: string) => req<{ path: string; content: string }>(`/api/v1/context/file?path=${encodeURIComponent(path)}`),
    put: (path: string, content: string) =>
      fetch(session.url(`/api/v1/context/file?path=${encodeURIComponent(path)}`), {
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
  onEvent: (ev: WSEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const r = await fetch(session.url(`/api/v1/workspaces/${workspaceId}/chat`), {
    method: 'POST',
    headers: session.headers({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ text }),
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
}

export type Agent = {
  id: number
  workspace_id: number
  name: string
  kind: string
  role: string
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

export type ContextFile = { path: string; version: string }

export type ContextStatus = {
  available: boolean
  space_root?: string
  mode?: string
  error?: string
}

export type ContextBinding = { id: number; workspace_id: number; path: string; agent_id: number | null }

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
  status: 'pending' | 'approved' | 'disabled'
  timeout_seconds: number
  updated_at: string
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
): Promise<void> {
  const r = await fetch(session.url(`/api/v1/gears/${gearId}/run?stream=1`), {
    method: 'POST',
    headers: session.headers({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ args }),
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
