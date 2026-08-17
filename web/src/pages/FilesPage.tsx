import { useCallback, useEffect, useState } from 'react'
import { api, type FileEntry } from '../api'

// The workspace's file tree.
//
// Only the tree. It used to carry the editor in its right-hand half, which
// made the tree's width and the file's width one number — in a narrow panel
// the file got about fifty pixels and every word broke to one character per
// line. A tree is a narrow thing and a file is a wide one, so they are two
// panels now and this one opens the other.
//
// Directories load a level at a time: a deep node_modules would turn opening
// the panel into a stall, and the tree is expanded one node at a time anyway.

type Node = { entry: FileEntry; children?: FileEntry[]; open: boolean }

export default function FilesPage({
  wsId,
  openPath,
  savedTick,
  onOpen,
  onError,
}: {
  wsId: number
  /** The file the editor panel currently holds, so the tree can mark it. */
  openPath: string | null
  /** Changes on every save, so a file's size in the tree stops lying. */
  savedTick: number
  onOpen: (path: string) => void
  onError: (m: string) => void
}) {
  const [adding, setAdding] = useState(false)
  const [nodes, setNodes] = useState<Map<string, Node>>(new Map())
  const [roots, setRoots] = useState<FileEntry[]>([])

  const loadRoot = useCallback(() => {
    api.files
      .list(wsId, '')
      .then(setRoots)
      .catch((e: Error) => onError(e.message))
  }, [wsId, onError])

  useEffect(loadRoot, [loadRoot])

  // A save in the editor changes a size in the tree, and the two are different
  // panels now, so the tree has to be told.
  useEffect(() => {
    if (savedTick > 0 && openPath) refreshDirOf(openPath)
    // refreshDirOf is stable per workspace; re-running on openPath alone would
    // re-list the directory on every click rather than on every save.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedTick])

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

  const row = (e: FileEntry, depth: number) => {
    const n = nodes.get(e.path)
    return (
      <div key={e.path}>
        <button
          className={`tree-row ${openPath === e.path ? 'selected' : ''}`}
          style={{ paddingLeft: `${0.4 + depth * 0.8}rem` }}
          onClick={() => (e.dir ? toggle(e) : onOpen(e.path))}
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

  const create = (path: string) =>
    api.files
      .write(wsId, path, '')
      .then(() => {
        loadRoot()
        refreshDirOf(path)
        onOpen(path)
      })
      .catch((err: Error) => onError(err.message))

  return (
    /* A file tree the way an editor draws one: a thin strip of label, actions
       as icons on it, and then rows. What was here read as a settings form —
       a bold title, a text button, two sentences of prose explaining what a
       directory is, and a labelled field with a hint under it — which took
       more than half the panel before the first filename. */
    <div className="file-tree">
      <div className="tree-bar">
        <span className="tree-title">Files</span>
        <span className="spacer" />
        <button className="tree-act" data-own onClick={() => setAdding((v) => !v)} title="New file">
          +
        </button>
        <button className="tree-act" data-own onClick={loadRoot} title="Reload">
          ⟳
        </button>
      </div>

      {adding && (
        <NewFile
          onCancel={() => setAdding(false)}
          onCreate={(path) => {
            setAdding(false)
            void create(path)
          }}
        />
      )}

      {roots.length === 0 ? (
        <p className="tree-empty">No files yet</p>
      ) : (
        <div className="tree-rows">{roots.map((e) => row(e, 0))}</div>
      )}
    </div>
  )
}

/* One row, appearing where the file will: type a path and press Enter. The
   label and the hint that used to sit around it said what a path is, which the
   placeholder shows and the operator already knows. Escape backs out. */
function NewFile({ onCreate, onCancel }: { onCreate: (path: string) => void; onCancel: () => void }) {
  const [path, setPath] = useState('')
  return (
    <form
      className="tree-new"
      onSubmit={(e) => {
        e.preventDefault()
        const p = path.trim()
        if (p) onCreate(p)
      }}
    >
      <input
        autoFocus
        value={path}
        placeholder="docs/notes.md"
        onChange={(e) => setPath(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') onCancel()
        }}
        onBlur={() => !path.trim() && onCancel()}
      />
    </form>
  )
}

function bytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
