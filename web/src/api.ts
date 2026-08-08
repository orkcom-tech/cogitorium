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

async function req<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(url, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (r.status === 204) return undefined as T
  const body = await r.json().catch(() => null)
  if (!r.ok) {
    throw new Error(body?.error?.message ?? `${r.status} ${r.statusText}`)
  }
  return body as T
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
  const r = await fetch('/api/v1/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
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
