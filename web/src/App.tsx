import { useEffect, useState } from 'react'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    fetch('/health')
      .then((r) => r.json())
      .then(setHealth)
      .catch((e: unknown) => setError(String(e)))
  }, [])

  return (
    <main className="shell">
      <h1>Cogitorium</h1>
      <p className="tagline">A workbench for agentic development.</p>
      <p className="health">
        {health && (
          <>
            server <code>{health.version}</code> — {health.status}
          </>
        )}
        {error && <>server unreachable: {error}</>}
        {!health && !error && <>checking server…</>}
      </p>
    </main>
  )
}
