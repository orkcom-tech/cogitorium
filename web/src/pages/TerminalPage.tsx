import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, type TerminalStatus } from '../api'
import { session } from '../session'

// A shell, running in the same sandbox gears run in — not on the server.
// The server refuses to open one without that sandbox, so what you get here
// cannot read the database or the provider keys in it.
export default function TerminalPage({ workspaceId }: { workspaceId?: number }) {
  const [status, setStatus] = useState<TerminalStatus | null>(null)
  const [connected, setConnected] = useState(false)
  const holder = useRef<HTMLDivElement>(null)
  const socket = useRef<WebSocket | null>(null)

  useEffect(() => {
    api.terminal.status().then(setStatus).catch(() => setStatus(null))
  }, [])

  // The server-wide shell is an administrator's; a workspace shell belongs
  // to whoever may use that workspace.
  const allowed = workspaceId ? status?.available : status?.global_available
  const blockedReason = workspaceId ? status?.reason : status?.global_reason

  useEffect(() => {
    if (!allowed || !holder.current) return

    const term = new Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: { background: '#00000000' },
      allowTransparency: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(holder.current)
    fit.fit()

    const base = session.server() || window.location.origin
    const path = workspaceId ? `/api/v1/workspaces/${workspaceId}/terminal` : '/api/v1/terminal'
    const wsUrl = new URL(path, base)
    wsUrl.protocol = wsUrl.protocol === 'https:' ? 'wss:' : 'ws:'
    wsUrl.searchParams.set('rows', String(term.rows))
    wsUrl.searchParams.set('cols', String(term.cols))
    // Browsers cannot set headers on a WebSocket, so the token rides as a
    // subprotocol — the server reads it there when there is no header.
    const token = session.token()
    const ws = token
      ? new WebSocket(wsUrl.toString(), ['bearer', token])
      : new WebSocket(wsUrl.toString())
    socket.current = ws

    ws.onopen = () => {
      setConnected(true)
      term.focus()
    }
    ws.onmessage = (e) => term.write(typeof e.data === 'string' ? e.data : '')
    ws.onclose = () => {
      setConnected(false)
      term.write('\r\n\x1b[2m[disconnected]\x1b[0m\r\n')
    }
    ws.onerror = () => term.write('\r\n\x1b[31m[connection failed]\x1b[0m\r\n')

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })

    const onResize = () => {
      fit.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }))
      }
    }
    window.addEventListener('resize', onResize)

    return () => {
      window.removeEventListener('resize', onResize)
      ws.close()
      term.dispose()
    }
  }, [allowed, workspaceId])

  if (status && !allowed) {
    return (
      <div className="page">
        <h2>Terminal</h2>
        <div className="card">
          <p>A terminal is not available here.</p>
          <p className="error">{blockedReason}</p>
          <p className="hint">
            Gear execution backend: <code>{status.backend}</code>. The terminal runs in that same sandbox — the
            server refuses to open a shell that would hold its own file access.
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className={workspaceId ? 'terminal-embedded' : 'page terminal-page'}>
      <div className="row">
        {!workspaceId && <h2>Terminal</h2>}
        <span className="muted">
          {connected ? 'connected' : 'connecting…'} · sandboxed, no network, nothing of the server's mounted
          {/* "a copy of", not "this workspace's files". The container gets a
              copy and nothing is carried back out, so a file written here is
              gone when the session ends. Saying otherwise is how someone loses
              an hour's work and only finds out afterwards. */}
          {workspaceId ? " · a copy of this workspace's files, not carried back" : ''}
        </span>
      </div>
      <div className="terminal-holder" ref={holder} />
    </div>
  )
}
