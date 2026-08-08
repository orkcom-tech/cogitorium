import { useEffect, useState } from 'react'
import { BrowserRouter, NavLink, Navigate, Route, Routes } from 'react-router-dom'
import ModelsPage from './pages/ModelsPage'
import ChatPage from './pages/ChatPage'
import WorkspacesPage from './pages/WorkspacesPage'
import WorkspacePage from './pages/WorkspacePage'
import ContextPage from './pages/ContextPage'
import GearsPage from './pages/GearsPage'

type Health = { status: string; version: string }

export default function App() {
  const [health, setHealth] = useState<Health | null>(null)

  useEffect(() => {
    fetch('/health')
      .then((r) => r.json())
      .then(setHealth)
      .catch(() => setHealth(null))
  }, [])

  return (
    <BrowserRouter>
      <div className="layout">
        <aside className="sidebar">
          <h1 className="brand">Cogitorium</h1>
          <nav>
            <NavLink to="/workspaces">Workspaces</NavLink>
            <NavLink to="/context">Context</NavLink>
            <NavLink to="/gears">Gears</NavLink>
            <NavLink to="/models">Models</NavLink>
            <NavLink to="/chat">Scratch chat</NavLink>
          </nav>
          <footer className="sidebar-footer">
            {health ? `${health.version} · ${health.status}` : 'server unreachable'}
          </footer>
        </aside>
        <main className="content">
          <Routes>
            <Route path="/" element={<Navigate to="/workspaces" replace />} />
            <Route path="/workspaces" element={<WorkspacesPage />} />
            <Route path="/workspaces/:id" element={<WorkspacePage />} />
            <Route path="/context" element={<ContextPage />} />
            <Route path="/gears" element={<GearsPage />} />
            <Route path="/models" element={<ModelsPage />} />
            <Route path="/chat" element={<ChatPage />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}
