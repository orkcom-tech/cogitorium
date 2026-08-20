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
  const drawn = document.querySelector('nav.rail')
  if (drawn) {
    // The template's rail STAYS until this one is ready to paint.
    //
    // Emptying it first and mounting into it left the column blank for as long
    // as whoami took — the frame disappearing and coming back, which reads as
    // the page loading twice. So the new rail is mounted beside it, hidden,
    // and RailOnly removes the old one at the moment it has something to show.
    //
    // Not hydration: the template's rail is markup, not a React tree, and
    // pretending otherwise is how mismatches start. It has already done its
    // job — it is what a browser with no JavaScript keeps, and what a plugin
    // overriding cog.shell.rail changes.
    const mine = document.createElement('div')
    mine.hidden = true
    drawn.after(mine)
    createRoot(mine).render(
      <StrictMode>
        <RailOnly onReady={() => {
          drawn.remove()
          mine.hidden = false
        }} />
      </StrictMode>,
    )
  }
}
