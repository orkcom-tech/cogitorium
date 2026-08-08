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

export type ChatMessage = { role: 'system' | 'user' | 'assistant'; content: string }

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
    setTeam: (id: number, teamId: number | null) =>
      req<Workspace>(`/api/v1/workspaces/${id}/team`, {
        method: 'PUT',
        body: JSON.stringify({ team_id: teamId }),
      }),
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
  team_id: number | null
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

// chatStream POSTs to the SSE chat endpoint and feeds parsed events back.
// Resolves on the server's "done" event (carrying truncation info); rejects
// on "error" or transport failure.
export async function chatStream(
  modelId: number,
  messages: ChatMessage[],
  onDelta: (text: string) => void,
  signal?: AbortSignal,
): Promise<ChatStreamResult> {
  const r = await fetch(session.url('/api/v1/chat'), {
    method: 'POST',
    headers: session.headers({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ model_id: modelId, messages }),
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
    if (done) break
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
      const ev = JSON.parse(data) as {
        type: string
        text?: string
        message?: string
        truncated?: boolean
        stop_reason?: string
      }
      if (ev.type === 'delta' && ev.text) onDelta(ev.text)
      if (ev.type === 'error') throw new Error(ev.message ?? 'stream error')
      if (ev.type === 'done') return { truncated: !!ev.truncated, stopReason: ev.stop_reason ?? '' }
    }
  }
  throw new Error('stream ended without done event')
}
