import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { api, type User, type Workspace } from "./api";
import { COG_MARK, DOCS_URL, ORKCOM_URL, ORK_MARK } from "./styles/brand";
import ThemeMenu from "./pages/ThemeMenu";
import UpdateNotice from "./pages/UpdateNotice";
import ChangePassword from "./pages/ChangePassword";
import { useShell } from "./shell";

/**
 * The rail: everything the operator commands, standing on the frame.
 *
 * The rule this exists to enforce is that the cavity holds only content. A
 * stage tab, a drawer button, the workspace's name, a count, the account —
 * every one of those used to sit in a header row across the top of the work,
 * and putting them there is what made the interface read as a page rather
 * than as an instrument around a window.
 *
 * The grouping is by the question a control answers, not by kind:
 *
 *   brand        — whose this is
 *   destinations — where in the install you are
 *   (spacer)     — the workspace and its state, rotated, and a menu of the rest
 *   bottom       — the install-wide pages, appearance, and who you are
 *
 * Counts ride on the buttons as badges rather than as a strip over the work,
 * the way a tray icon carries a dot.
 */

/* Icons are drawn here rather than imported: the product fetches nothing at
   runtime, and an icon font would be a network request or a 60KB payload for
   nine glyphs. 1.5px stroke, 24px box, inherited colour. */
const I = {
  workspaces: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <rect x="3" y="3" width="7" height="7" rx="2" />
      <rect x="14" y="3" width="7" height="7" rx="2" />
      <rect x="3" y="14" width="7" height="7" rx="2" />
      <rect x="14" y="14" width="7" height="7" rx="2" />
    </svg>
  ),
  map: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M9 4 3 6.5v13L9 17l6 3 6-2.5v-13L15 7 9 4Z" />
      <path d="M9 4v13M15 7v13" />
    </svg>
  ),
  gears: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <circle cx="12" cy="12" r="3.2" />
      <path d="M12 3.4v3M12 17.6v3M3.4 12h3M17.6 12h3M5.9 5.9l2.1 2.1M16 16l2.1 2.1M18.1 5.9 16 8M8 16l-2.1 2.1" />
    </svg>
  ),
  instructions: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <rect x="4.5" y="3.5" width="15" height="17" rx="2.5" />
      <path d="M8.5 8.5h7M8.5 12h7M8.5 15.5h4" />
    </svg>
  ),
  models: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <rect x="7.5" y="7.5" width="9" height="9" rx="2" />
      <path d="M10 3.5v4M14 3.5v4M10 16.5v4M14 16.5v4M3.5 10h4M3.5 14h4M16.5 10h4M16.5 14h4" />
    </svg>
  ),
  context: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <ellipse cx="12" cy="6.5" rx="7.5" ry="3" />
      <path d="M4.5 6.5v11c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3v-11" />
      <path d="M4.5 12c0 1.7 3.4 3 7.5 3s7.5-1.3 7.5-3" />
    </svg>
  ),
  people: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <circle cx="9" cy="8.5" r="3.2" />
      <path d="M3.5 19.5c0-3 2.5-5 5.5-5s5.5 2 5.5 5" />
      <path d="M16 5.6a3.2 3.2 0 0 1 0 5.8M17.5 14.9c2 .6 3.3 2.3 3.3 4.6" />
    </svg>
  ),
  back: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M15 5.5 8.5 12l6.5 6.5" />
    </svg>
  ),
  action: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <path d="M12 3.8v10.4M8.2 10.6 12 14.4l3.8-3.8" />
      <path d="M4.6 15.6v2.6a2 2 0 0 0 2 2h10.8a2 2 0 0 0 2-2v-2.6" />
    </svg>
  ),
  more: (
    <svg viewBox="0 0 24 24" aria-hidden>
      <circle cx="12" cy="5.5" r="1.4" fill="currentColor" stroke="none" />
      <circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" />
      <circle cx="12" cy="18.5" r="1.4" fill="currentColor" stroke="none" />
    </svg>
  ),
};

export default function Rail({
  user,
  health,
  onSignOut,
}: {
  user: User;
  health: { version: string; status: string } | null;
  onSignOut: () => void;
}) {
  const [menu, setMenu] = useState<null | "more" | "account" | "dests">(null);
  const [changingPassword, setChangingPassword] = useState(false);
  const [menuTop, setMenuTop] = useState(0);
  const box = useRef<HTMLElement>(null);
  const loc = useLocation();
  const nav = useNavigate();
  const shell = useShell();
  // The rail is icons, so it has to be able to say what each one is. One
  // element for all of them, moved to whichever button the pointer is over —
  // cheaper than a node per button, and it cannot leave two on screen at once.
  const [tip, setTip] = useState<{ text: string; top: number } | null>(null);
  // The rail owns this list because "which workspace am I in, and what are the
  // others" is a question about the frame rather than about any screen. Fetched
  // once; the menu that shows it is opened rarely and a stale name is a smaller
  // problem than a request on every render.
  const [WORKSPACES, setWorkspaces] = useState<Workspace[]>([]);
  useEffect(() => {
    api.workspaces
      .list()
      .then(setWorkspaces)
      .catch(() => setWorkspaces([]));
  }, []);
  const hover = (text: string) => (e: React.MouseEvent<HTMLElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    setTip({ text, top: Math.round(r.top + r.height / 2) });
  };
  const unhover = () => setTip(null);

  // A menu closes on an outside press and on Escape. Deferred by a tick, or
  // the press that opened it closes it again on the same one.
  useEffect(() => {
    if (!menu) return;
    const away = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setMenu(null);
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMenu(null);
    };
    const t = setTimeout(() => document.addEventListener("mousedown", away), 0);
    document.addEventListener("keydown", key);
    return () => {
      clearTimeout(t);
      document.removeEventListener("mousedown", away);
      document.removeEventListener("keydown", key);
    };
  }, [menu]);

  // Route changes close whatever is open, so a menu never outlives the screen
  // it was opened from.
  useEffect(() => setMenu(null), [loc.pathname]);

  const open = (
    which: "more" | "account" | "dests",
    e: React.MouseEvent<HTMLElement>,
  ) => {
    const r = e.currentTarget.getBoundingClientRect();
    // Vertically centred on its button, clamped so a menu opened near the floor
    // does not hang off it.
    setMenuTop(Math.min(Math.max(8, r.top - 10), window.innerHeight - 320));
    setMenu((v) => (v === which ? null : which));
  };

  return (
    <nav
      className={`rail ${menu ? "joined" : ""}`}
      ref={box}
      aria-label="Cogitorium"
    >
      <Link className="rail-brand" to="/workspaces" title="Cogitorium">
        <img src={COG_MARK} alt="Cogitorium" width={26} height={26} />
        <span className="by">
          <span>by</span>
          <span
            className="ork"
            aria-hidden
            style={{
              maskImage: `url("${ORK_MARK}")`,
              WebkitMaskImage: `url("${ORK_MARK}")`,
            }}
          />
        </span>
      </Link>

      {/* CAPSULE 1 — WHERE AM I.
          The workspace you are in, and a press opens the list of the others
          beside it. The seven install-wide destinations used to sit here as
          buttons that each replaced the whole cavity, which threw the operator
          out of their work to look something up. They are drawers and menu
          items now: the cavity holds the work and nothing takes it away. */}
      {/* Only while a workspace is open. On the list itself the question "where
          am I" is already answered by the screen, and the button repeated the
          word Workspaces one row above the stage of the same name. */}
      {shell?.back && (
        <div className="rail-group">
          <button
            data-own
            className={`rail-btn ${menu === "dests" ? "on" : ""}`}
            onMouseEnter={hover(shell?.here?.label ?? "Workspaces")}
            onMouseLeave={unhover}
            onClick={(e) => open("dests", e)}
          >
            {I.workspaces}
            <span className="sr-only">Workspaces</span>
          </button>
        </div>
      )}

      {/* What the screen in the cavity asked the frame to offer. The rail knows
          nothing about workspaces: it renders whatever was published, so a new
          screen with stages and drawers gets them without touching this file.
          See shell.tsx for which way the wire points. */}
      {shell?.stages && (
        <div className="rail-group">
          {shell.stages.items.map((s) => {
            const on = shell.stages!.current === s.id;
            return (
              <button
                key={s.id}
                data-own
                className={`rail-btn ${on ? "on" : ""}`}
                aria-current={on ? "true" : undefined}
                onMouseEnter={hover(s.title)}
                onMouseLeave={unhover}
                onClick={() => shell.stages!.go(s.id)}
              >
                {s.icon}
                <span className="sr-only">{s.title}</span>
              </button>
            );
          })}
        </div>
      )}

      {shell?.drawers && (
        <div className="rail-group">
          {shell.drawers.items.map((d) => {
            const on = shell.drawers!.open === d.id;
            return (
              <button
                key={d.id}
                data-own
                className={`rail-btn ${on ? "on" : ""}`}
                aria-expanded={on}
                onMouseEnter={hover(d.title)}
                onMouseLeave={unhover}
                onClick={() => shell.drawers!.toggle(on ? null : d.id)}
              >
                {d.icon}
                <span className="sr-only">{d.title}</span>
                {d.badge ? (
                  <span className="rail-badge quiet">{d.badge}</span>
                ) : null}
              </button>
            );
          })}
        </div>
      )}

      {/* The gap that pushes the last capsule to the floor.
          It used to carry the workspace's name as rotated text. With eighteen
          buttons on the rail that label cost 118px the column does not have —
          it overlapped four of them — and it was saying a second time what
          capsule 1 already says: that button IS the workspace, its name is its
          tooltip, and pressing it lists the others. */}
      <div className="rail-spacer" />

      {/* The way out of the screen, and its one action, both on the frame. */}
      {(shell?.back || shell?.action) && (
        <div className="rail-group">
          {shell.back && (
            <button
              data-own
              className="rail-btn"
              onMouseEnter={hover("Back")}
              onMouseLeave={unhover}
              onClick={() => nav(shell.back!)}
            >
              {I.back}
              <span className="sr-only">Back</span>
            </button>
          )}
          {shell.action && (
            <button
              data-own
              className="rail-btn"
              onMouseEnter={hover(shell.action.label)}
              onMouseLeave={unhover}
              onClick={shell.action.run}
            >
              {I.action}
              <span className="sr-only">{shell.action.label}</span>
            </button>
          )}
        </div>
      )}

      <div className="rail-group">
        <button
          data-own
          className="rail-btn"
          onMouseEnter={hover("More")}
          onMouseLeave={unhover}
          onClick={(e) => open("more", e)}
        >
          {I.more}
          <span className="sr-only">More</span>
        </button>
        {/* Beside the appearance bead and the account, where the product's own
            state already lives. It draws nothing at all unless there is
            something to say or — for an administrator on an install nobody has
            answered for yet — something to ask. */}
        <UpdateNotice me={user} />
        <ThemeMenu />
        <button
          data-own
          className={`rail-btn ${menu === "account" ? "on" : ""}`}
          onMouseEnter={hover(user.name)}
          onMouseLeave={unhover}
          onClick={(e) => open("account", e)}
        >
          <span style={{ font: "650 13px/1 var(--mono)" }}>
            {user.name.slice(0, 1).toUpperCase()}
          </span>
        </button>
      </div>

      {tip && !menu && (
        <span
          className="rail-tip"
          role="tooltip"
          style={{
            top: tip.top,
            left: `calc(var(--rail-w) + var(--bezel))`,
            transform: "translateY(-50%)",
          }}
        >
          {tip.text}
        </span>
      )}

      {menu === "dests" && (
        <div className="rail-menu" style={{ top: menuTop }} role="menu">
          <span className="rail-menu-label">workspaces</span>
          {WORKSPACES.map((w) => (
            <button key={w.id} onClick={() => nav(`/workspaces/${w.id}`)}>
              {w.name}
              <span className="sub">{w.description}</span>
            </button>
          ))}
          <hr />
          <button onClick={() => nav("/workspaces")}>All workspaces…</button>
        </div>
      )}

      {menu === "more" && (
        <div className="rail-menu" style={{ top: menuTop }} role="menu">
          <span className="rail-menu-label">the install</span>
          <Link to="/models">Models</Link>
          {user.role === "admin" && <Link to="/people">People</Link>}
          {user.role === "admin" && <Link to="/context">Context</Link>}
          <hr />
          <a href={DOCS_URL} target="_blank" rel="noreferrer">
            Documentation
          </a>
          <a href={ORKCOM_URL} target="_blank" rel="noreferrer">
            ORKCOM
          </a>
        </div>
      )}

      {menu === "account" && (
        <div className="rail-menu" style={{ top: menuTop }} role="menu">
          <span className="rail-menu-label">{user.role}</span>
          <span
            className="rail-menu-label"
            style={{ textTransform: "none", letterSpacing: 0 }}
          >
            <strong style={{ color: "var(--text)", fontSize: "var(--t-ui)" }}>
              {user.name}
            </strong>
          </span>
          <hr />
          <span
            className="rail-menu-label"
            style={{ textTransform: "none", letterSpacing: 0 }}
          >
            {health
              ? `${health.version} · ${health.status}`
              : "server unreachable"}
          </span>
          <button
            onClick={() => {
              setMenu(null);
              setChangingPassword(true);
            }}
          >
            change password
          </button>
          <button onClick={onSignOut}>sign out</button>
        </div>
      )}

      {changingPassword && (
        <ChangePassword me={user} onDone={() => setChangingPassword(false)} />
      )}
    </nav>
  );
}
