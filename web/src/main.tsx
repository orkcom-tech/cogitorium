import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import RailOnly from './RailOnly'
import './index.css'

/**
 * Two ways in, because this product has two kinds of screen.
 *
 * `#root` is the application's own document, from index.html: everything
 * mounts. A screen the SERVER rendered has no root — it is a finished
 * document — and there the application mounts ONE thing, the rail, over the
 * one the template drew. That is what makes the frame the same on every screen
 * in the product rather than two frames that drift apart.
 *
 * Nothing else of the application runs on a server-rendered page. The page is
 * the server's, and it stays the server's.
 */
const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
} else {
  const rail = document.querySelector('nav.rail')
  if (rail) {
    // Replaced rather than hydrated: the template's rail is markup, not a
    // React tree, and pretending otherwise is how hydration mismatches start.
    // What the template drew is what a plugin overriding it changes and what a
    // browser with no JavaScript keeps — it has already done its job by now.
    rail.replaceChildren()
    createRoot(rail as HTMLElement).render(
      <StrictMode>
        <RailOnly />
      </StrictMode>,
    )
  }
}
