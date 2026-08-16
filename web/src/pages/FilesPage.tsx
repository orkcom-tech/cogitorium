import { useCallback, useEffect, useState } from 'react'
import { Field } from './Field'
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

  return (
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
          This workspace's directory is empty. Files created here — by you, by a gear, or from the terminal — show up
          in this tree.
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
              onOpen(path)
            })
            .catch((err: Error) => onError(err.message))
        }}
      />
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
      <Field label="New file" wide hint="a path inside this workspace; folders are made as needed">
        <input value={path} placeholder="docs/notes.md" onChange={(e) => setPath(e.target.value)} />
      </Field>
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
