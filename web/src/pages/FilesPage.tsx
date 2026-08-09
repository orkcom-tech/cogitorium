import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type FileEntry } from '../api'

// A tree and an editor, so a quick look or a small correction does not mean
// leaving for an IDE while agents are working. Directories load a level at a
// time: a deep node_modules would turn opening the panel into a stall, and
// the tree is expanded one node at a time anyway.

type Node = { entry: FileEntry; children?: FileEntry[]; open: boolean }

/**
 * How the two halves share the panel.
 *
 * The split used to be `minmax(12rem, 18rem) 1fr`, which is a rule about the
 * TREE and says nothing about the editor. In a narrow panel the tree took its
 * 18rem ceiling and the editor got whatever was left — at 365px that is about
 * fifty pixels, and the file's text wrapped to one character per line. Both
 * halves need a floor, and the operator needs to be able to move the seam,
 * because which half matters depends on what they are doing.
 */
const MIN_TREE = 150
const MIN_EDITOR = 260
const DEFAULT_TREE_W = 260
const TREE_W_KEY = 'cogitorium.files.tree'

/** Never let either half fall under its floor — and when the container is too
 *  narrow to honour both, the stacked layout takes over and this stops being
 *  the question being asked. */
function clampTree(px: number, containerW: number) {
  const ceiling = Math.max(MIN_TREE, containerW - MIN_EDITOR)
  return Math.round(Math.min(Math.max(px, MIN_TREE), ceiling))
}

function storeTreeW(px: number, set: (n: number) => void) {
  set(px)
  try {
    localStorage.setItem(TREE_W_KEY, String(px))
  } catch {
    /* private mode: the split simply does not survive the session */
  }
}

function loadTreeW() {
  const n = Number(localStorage.getItem(TREE_W_KEY))
  return Number.isFinite(n) && n >= MIN_TREE ? n : DEFAULT_TREE_W
}

export default function FilesPage({ wsId, onError }: { wsId: number; onError: (m: string) => void }) {
  const [nodes, setNodes] = useState<Map<string, Node>>(new Map())
  const [roots, setRoots] = useState<FileEntry[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [text, setText] = useState('')
  const [saved, setSaved] = useState('')
  const [note, setNote] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [treeW, setTreeW] = useState(loadTreeW)
  const filesRef = useRef<HTMLDivElement>(null)
  const dragging = useRef(false)

  const loadRoot = useCallback(() => {
    api.files
      .list(wsId, '')
      .then(setRoots)
      .catch((e: Error) => onError(e.message))
  }, [wsId, onError])

  useEffect(loadRoot, [loadRoot])

  // After a write, the directory holding the file is re-listed so its size
  // and modified time stop lying. Refreshing only the root would leave an
  // expanded subdirectory showing the file as it was before the save.
  const refreshDirOf = useCallback(
    (filePath: string) => {
      const slash = filePath.lastIndexOf('/')
      if (slash === -1) {
        loadRoot()
        return
      }
      const dir = filePath.slice(0, slash)
      api.files
        .list(wsId, dir)
        .then((children) =>
          setNodes((p) => {
            const cur = p.get(dir)
            if (!cur) return p
            return new Map(p).set(dir, { ...cur, children })
          }),
        )
        .catch((e: Error) => onError(e.message))
    },
    [wsId, loadRoot, onError],
  )

  const toggle = (e: FileEntry) => {
    const cur = nodes.get(e.path)
    if (cur?.open) {
      setNodes(new Map(nodes).set(e.path, { ...cur, open: false }))
      return
    }
    if (cur?.children) {
      setNodes(new Map(nodes).set(e.path, { ...cur, open: true }))
      return
    }
    api.files
      .list(wsId, e.path)
      .then((children) => setNodes((p) => new Map(p).set(e.path, { entry: e, children, open: true })))
      .catch((err: Error) => onError(err.message))
  }

  const open = (e: FileEntry) => {
    setLoading(true)
    setNote(null)
    api.files
      .read(wsId, e.path)
      .then((f) => {
        setSelected(f.path)
        setText(f.content)
        setSaved(f.content)
      })
      .catch((err: Error) => {
        // Binary and oversized files are refused by the server on purpose;
        // that reason is worth showing in place rather than as a red banner.
        setSelected(null)
        setNote(err.message)
      })
      .finally(() => setLoading(false))
  }

  const save = () => {
    if (!selected) return
    api.files
      .write(wsId, selected, text)
      .then(() => {
        setSaved(text)
        setNote('saved')
        refreshDirOf(selected)
      })
      .catch((err: Error) => onError(err.message))
  }

  const dirty = selected !== null && text !== saved

  const row = (e: FileEntry, depth: number) => {
    const n = nodes.get(e.path)
    return (
      <div key={e.path}>
        <button
          className={`tree-row ${selected === e.path ? 'selected' : ''}`}
          style={{ paddingLeft: `${0.4 + depth * 0.8}rem` }}
          onClick={() => (e.dir ? toggle(e) : open(e))}
          title={e.path}
        >
          <span className="tree-icon">{e.dir ? (n?.open ? '▾' : '▸') : '·'}</span>
          <span className="tree-name">{e.name}</span>
          {!e.dir && <span className="muted tree-size">{bytes(e.size)}</span>}
        </button>
        {e.dir && n?.open && n.children?.map((c) => row(c, depth + 1))}
      </div>
    )
  }

  return (
    <div className="files" ref={filesRef} style={{ ['--tree-w' as string]: `${treeW}px` }}>
      <div className="file-tree">
        <div className="row tree-head">
          <strong>Workspace files</strong>
          <span className="spacer" />
          <button onClick={loadRoot} title="Reload the tree">
            refresh
          </button>
        </div>
        {roots.length === 0 ? (
          <p className="hint">
            This workspace's directory is empty. Files created here — by you, by a gear, or from the terminal — show
            up in this tree.
          </p>
        ) : (
          roots.map((e) => row(e, 0))
        )}
        <NewFile
          onCreate={(path) => {
            api.files
              .write(wsId, path, '')
              .then(() => {
                loadRoot()
                refreshDirOf(path)
                setSelected(path)
                setText('')
                setSaved('')
                setNote(null)
              })
              .catch((err: Error) => onError(err.message))
          }}
        />
      </div>

      {/* The seam between the tree and the editor.
       *
       * Absolute, not incremental: the width is the pointer's distance from
       * the container's own left edge, so a drag can never accumulate error
       * or run away from the cursor. The ceiling is measured against the
       * container rather than the window — a panel is not the screen.
       *
       * Below the stacking width this is display:none, because at that size
       * there is no ratio worth choosing. */}
      <div
        className="files-seam"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the file tree"
        tabIndex={0}
        onPointerDown={(e) => {
          e.currentTarget.setPointerCapture(e.pointerId)
          dragging.current = true
        }}
        onPointerMove={(e) => {
          if (!dragging.current || !filesRef.current) return
          const box = filesRef.current.getBoundingClientRect()
          filesRef.current.style.setProperty('--tree-w', `${clampTree(e.clientX - box.left, box.width)}px`)
        }}
        onPointerUp={(e) => {
          e.currentTarget.releasePointerCapture(e.pointerId)
          dragging.current = false
          const px = filesRef.current?.style.getPropertyValue('--tree-w')
          if (px) storeTreeW(parseFloat(px), setTreeW)
        }}
        onKeyDown={(e) => {
          const step = e.shiftKey ? 48 : 16
          if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
          e.preventDefault()
          const box = filesRef.current?.getBoundingClientRect()
          storeTreeW(clampTree(treeW + (e.key === 'ArrowRight' ? step : -step), box?.width ?? 0), setTreeW)
        }}
        onDoubleClick={() => storeTreeW(DEFAULT_TREE_W, setTreeW)}
      />

      <div className="file-editor">
        {loading && <p className="hint">opening…</p>}
        {!loading && !selected && <p className="hint">{note ?? 'Pick a file to view or edit it.'}</p>}
        {!loading && selected && (
          <>
            <div className="row tree-head">
              <strong>{selected}</strong>
              {dirty && <span className="warn">unsaved</span>}
              {!dirty && note && <span className="muted">{note}</span>}
              <span className="spacer" />
              <button
                onClick={() => {
                  setText(saved)
                  setNote(null)
                }}
                disabled={!dirty}
              >
                revert
              </button>
              <button className="primary" onClick={save} disabled={!dirty}>
                save
              </button>
            </div>
            <textarea
              className="code-edit"
              value={text}
              spellCheck={false}
              onChange={(e) => {
                setText(e.target.value)
                setNote(null)
              }}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === 's') {
                  e.preventDefault()
                  save()
                }
              }}
            />
          </>
        )}
      </div>
    </div>
  )
}

function NewFile({ onCreate }: { onCreate: (path: string) => void }) {
  const [path, setPath] = useState('')
  return (
    <form
      className="row new-file"
      onSubmit={(e) => {
        e.preventDefault()
        const p = path.trim()
        if (!p) return
        onCreate(p)
        setPath('')
      }}
    >
      <input
        className="grow"
        value={path}
        placeholder="new file, e.g. docs/notes.md"
        onChange={(e) => setPath(e.target.value)}
      />
      <button type="submit" disabled={!path.trim()}>
        create
      </button>
    </form>
  )
}

function bytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
