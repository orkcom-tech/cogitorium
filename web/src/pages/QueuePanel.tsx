import { useCallback, useEffect, useState } from 'react'
import { api, type QueueEntry, type QueueView, type Schedule, type InletTask } from '../api'

// What this workspace is doing, what is waiting, and what starts on its own.
//
// The two live together because they answer one question — "why is nothing
// happening, or why is everything happening" — and separating them would put
// the schedule that fired at 02:00 on a different screen from the queue it
// filled.
//
// A queue nobody can see is discovered by being refused by it, and a queue that
// can be seen but not stopped is worse than one that cannot be seen at all:
// fifty jobs visibly waiting behind a run that is wedged, and no button.

// How often this refreshes while it is on screen. Two seconds is short enough
// that pressing cancel and watching the row go feels immediate, and the query
// behind it is two indexed reads.
const REFRESH_MS = 2000

export default function QueuePanel({
  wsId,
  shown,
  onError,
}: {
  wsId: number
  shown: boolean
  onError: (m: string) => void
}) {
  const [view, setView] = useState<QueueView | null>(null)
  const [schedules, setSchedules] = useState<Schedule[]>([])
  // The tasks a schedule can point at. Loaded here rather than passed in: a
  // schedule has no job of its own — it fires an inlet task — so the list of
  // what can be scheduled is this panel's own question.
  const [tasks, setTasks] = useState<InletTask[]>([])
  const [adding, setAdding] = useState(false)

  const reload = useCallback(() => {
    Promise.all([api.queue.show(wsId), api.schedules.list(wsId), api.inlets.list(wsId)])
      .then(([q, s, doors]) => {
        setView(q)
        setSchedules(s)
        setTasks(doors.flatMap((d) => d.tasks))
      })
      .catch((e: Error) => onError(e.message))
  }, [wsId, onError])

  // Only while it is on the bench, and only while the tab is in front. A panel
  // nobody is looking at polling every two seconds forever is how a laptop
  // finds out this product is running.
  useEffect(() => {
    if (!shown) return
    reload()
    const t = setInterval(() => {
      if (document.visibilityState === 'visible') reload()
    }, REFRESH_MS)
    return () => clearInterval(t)
  }, [shown, reload])

  if (!shown) return null

  const cancel = (e: QueueEntry) =>
    api.queue
      .cancel(e.unit)
      .then(reload)
      .catch((err: Error) => onError(err.message))

  return (
    <div className="panel-body queue-panel">
      <header className="row spread">
        <h3>
          Queue
          {view && (
            <span className="muted">
              {' '}
              — {view.running} running, {view.queued} waiting
            </span>
          )}
        </h3>
      </header>

      {view?.entries.length === 0 && (
        <p className="muted">
          Nothing running and nothing waiting. A workspace runs one thing at a time; anything that
          arrives while it is busy queues here rather than being turned away.
        </p>
      )}

      <ul className="queue-list">
        {view?.entries.map((e) => (
          <li key={e.unit} className={e.state === 'claimed' ? 'running' : ''}>
            <span className="what">
              {e.state === 'claimed' ? 'running' : `#${e.position} waiting`}
              <span className="muted"> · {KIND[e.kind]}</span>
            </span>
            {e.run != null && <span className="muted">run {e.run}</span>}
            <time dateTime={e.since}>{since(e.since)}</time>
            <button className="danger" onClick={() => cancel(e)} title="Stop this — the work, not just the row">
              Stop
            </button>
          </li>
        ))}
      </ul>

      <header className="row spread">
        <h3>Schedules</h3>
        <button onClick={() => setAdding((v) => !v)} disabled={tasks.length === 0}>
          {adding ? 'Cancel' : '+ New schedule'}
        </button>
      </header>

      {tasks.length === 0 && (
        <p className="muted">
          A schedule fires an inlet task, so there is nothing to schedule until this workspace has
          one. That is deliberate: the task already says which agent, what to tell it and what
          counts as success, and a schedule with its own copy of all that would be a second
          definition to keep in step.
        </p>
      )}

      {adding && <NewSchedule wsId={wsId} tasks={tasks} onDone={() => { setAdding(false); reload() }} onError={onError} />}

      <ul className="schedule-list">
        {schedules.map((s) => (
          <li key={s.id} className={s.enabled ? '' : 'off'}>
            <div className="row spread">
              <strong>{s.name}</strong>
              <code>{s.spec}</code>
            </div>
            <div className="muted small">
              next {new Date(s.next_at).toLocaleString()}
              {s.tz && ` (${s.tz})`} · {s.fires} fired, {s.skips} skipped
              {s.last_outcome && ` · last: ${s.last_outcome}`}
            </div>
            <div className="row">
              <button
                onClick={() =>
                  api.schedules
                    .setEnabled(s.id, !s.enabled)
                    .then(reload)
                    .catch((e: Error) => onError(e.message))
                }
              >
                {s.enabled ? 'Pause' : 'Resume'}
              </button>
              <button
                onClick={() =>
                  api.schedules
                    .runNow(s.id)
                    .then(reload)
                    .catch((e: Error) => onError(e.message))
                }
                title="Fire it now without moving its clock"
              >
                Run now
              </button>
              <button
                className="danger"
                onClick={() =>
                  api.schedules
                    .remove(s.id)
                    .then(reload)
                    .catch((e: Error) => onError(e.message))
                }
              >
                Delete
              </button>
            </div>
          </li>
        ))}
      </ul>
    </div>
  )
}

const KIND: Record<QueueEntry['kind'], string> = {
  delivery: 'a delivery through a door',
  chat: 'your own turn',
  callback: 'telling a listener',
}

// Elapsed rather than a clock time: the only thing anybody wants from a queue
// is how long this has been going on.
function since(at: string): string {
  const secs = Math.max(0, Math.round((Date.now() - new Date(at).getTime()) / 1000))
  if (secs < 60) return `${secs}s`
  if (secs < 3600) return `${Math.round(secs / 60)}m`
  return `${Math.round(secs / 3600)}h`
}

function NewSchedule({
  wsId,
  tasks,
  onDone,
  onError,
}: {
  wsId: number
  tasks: InletTask[]
  onDone: () => void
  onError: (m: string) => void
}) {
  const [taskID, setTaskID] = useState(tasks[0]?.id ?? 0)
  const [name, setName] = useState('')
  const [spec, setSpec] = useState('every 1h')
  const [tz, setTZ] = useState(Intl.DateTimeFormat().resolvedOptions().timeZone || '')
  const [payload, setPayload] = useState('{}')
  const [onMiss, setOnMiss] = useState<'skip' | 'run'>('skip')

  const save = () => {
    let body: unknown
    try {
      body = JSON.parse(payload || '{}')
    } catch {
      onError('the payload is not JSON')
      return
    }
    api.schedules
      .create(wsId, { task_id: taskID, name, spec, tz, payload: body, on_miss: onMiss })
      .then(onDone)
      // Everything the server refuses here is something typed into this form,
      // and its refusals are written to be read by whoever typed them.
      .catch((e: Error) => onError(e.message))
  }

  return (
    <div className="card">
      <div className="row">
        <select value={taskID} onChange={(e) => setTaskID(Number(e.target.value))}>
          {tasks.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name} → {t.agent_name}
            </option>
          ))}
        </select>
        <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="row">
        <input
          className="grow"
          placeholder="every 15m — or 0 7 * * 1-5"
          value={spec}
          onChange={(e) => setSpec(e.target.value)}
        />
        <input placeholder="Europe/Berlin" value={tz} onChange={(e) => setTZ(e.target.value)} />
      </div>
      <textarea
        rows={3}
        value={payload}
        onChange={(e) => setPayload(e.target.value)}
        placeholder="the body this task is given, as JSON"
      />
      <div className="row spread">
        <label>
          <input
            type="checkbox"
            checked={onMiss === 'run'}
            onChange={(e) => setOnMiss(e.target.checked ? 'run' : 'skip')}
          />{' '}
          run even if the previous one has not finished
        </label>
        <button onClick={save} disabled={!name.trim() || !spec.trim()}>
          Create
        </button>
      </div>
      <p className="muted small">
        Two forms: <code>every 15m</code>, or five cron fields — minute, hour, day of month, month,
        day of week. A scheduled run never gets web search: every search waits for a person to
        approve that exact query, and on a schedule there is nobody to ask.
      </p>
    </div>
  )
}
