// Line diff, written here rather than installed.
//
// Same reasoning as the highlighter next door: a diff library is a dependency
// tree in a product that ships as one binary with its UI embedded, and the
// question being asked is small — which lines are the same, which are gone,
// which are new. This answers exactly that and nothing else. No word-level
// refinement, no rename detection, no hunk headers: those are a diff TOOL, and
// what is wanted here is a diff VIEW.
//
// The algorithm is the classic Myers longest-common-subsequence, computed on
// hashed lines so the comparison is integer equality rather than string
// equality down a long file.

export type Op = 'same' | 'del' | 'add'

export type Row = {
  op: Op
  /** 1-based line number on the left, null for an added line. */
  left: number | null
  /** 1-based line number on the right, null for a deleted line. */
  right: number | null
  text: string
}

export type Stat = { added: number; removed: number }

/**
 * The size above which a diff is refused rather than attempted.
 *
 * Myers is O(ND): fine for an edit, quadratic for two unrelated files. A
 * generated file pasted over another one would freeze the panel for seconds,
 * and a frozen panel is a worse answer than "this is too big to show".
 */
const MAX_LINES = 4000

export function tooBig(a: string, b: string): boolean {
  return lines(a).length > MAX_LINES || lines(b).length > MAX_LINES
}

function lines(s: string): string[] {
  if (s === '') return []
  // A trailing newline is a terminator, not an empty last line — otherwise
  // every file appears to end with one blank line that is never edited.
  const out = s.split('\n')
  if (out.length > 0 && out[out.length - 1] === '') out.pop()
  return out
}

/**
 * Myers' middle-snake, iterated: the shortest edit script between two
 * sequences. Implemented over the trace of forward paths, which is the version
 * that needs one array and a stack of its snapshots rather than recursion.
 */
export function diff(oldText: string, newText: string): Row[] {
  const a = lines(oldText)
  const b = lines(newText)

  // Hashing means the inner loop compares numbers. On a file where most lines
  // repeat — indentation, closing braces — string comparison dominates.
  const ids = new Map<string, number>()
  const id = (s: string) => {
    let v = ids.get(s)
    if (v === undefined) {
      v = ids.size
      ids.set(s, v)
    }
    return v
  }
  const x = a.map(id)
  const y = b.map(id)

  const n = x.length
  const m = y.length
  const max = n + m
  const v = new Int32Array(2 * max + 1)
  const offset = max
  const trace: Int32Array[] = []

  outer: for (let d = 0; d <= max; d++) {
    trace.push(v.slice())
    for (let k = -d; k <= d; k += 2) {
      // Which of the two neighbours to extend from: down when we are on the
      // lower edge or the path above reached further.
      let i: number
      if (k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1])) {
        i = v[offset + k + 1]
      } else {
        i = v[offset + k - 1] + 1
      }
      let j = i - k
      // The snake: every matching line costs nothing, so run them all out.
      while (i < n && j < m && x[i] === y[j]) {
        i++
        j++
      }
      v[offset + k] = i
      if (i >= n && j >= m) {
        trace.push(v.slice())
        break outer
      }
    }
  }

  // Walk the trace backwards to recover the path, then reverse it.
  const rows: Row[] = []
  let i = n
  let j = m
  for (let d = trace.length - 1; d > 0 && (i > 0 || j > 0); d--) {
    const vd = trace[d - 1]
    const k = i - j
    let prevK: number
    if (k === -(d - 1) || (k !== d - 1 && vd[offset + k - 1] < vd[offset + k + 1])) {
      prevK = k + 1
    } else {
      prevK = k - 1
    }
    const prevI = vd[offset + prevK]
    const prevJ = prevI - prevK

    while (i > prevI && j > prevJ) {
      rows.push({ op: 'same', left: i, right: j, text: a[i - 1] })
      i--
      j--
    }
    if (d === 1 && i === 0 && j === 0) break
    if (i > prevI) {
      rows.push({ op: 'del', left: i, right: null, text: a[i - 1] })
      i--
    } else if (j > prevJ) {
      rows.push({ op: 'add', left: null, right: j, text: b[j - 1] })
      j--
    }
  }
  // Anything left is a common prefix the trace already consumed.
  while (i > 0 && j > 0) {
    rows.push({ op: 'same', left: i, right: j, text: a[i - 1] })
    i--
    j--
  }
  while (i > 0) {
    rows.push({ op: 'del', left: i, right: null, text: a[i - 1] })
    i--
  }
  while (j > 0) {
    rows.push({ op: 'add', left: null, right: j, text: b[j - 1] })
    j--
  }

  rows.reverse()
  return rows
}

export function stat(rows: Row[]): Stat {
  let added = 0
  let removed = 0
  for (const r of rows) {
    if (r.op === 'add') added++
    if (r.op === 'del') removed++
  }
  return { added, removed }
}

/**
 * Drop long runs of unchanged lines, keeping `context` on each side of a
 * change. A thousand identical lines above the edit are a thousand lines
 * nobody scrolls past to reach it.
 *
 * A gap is reported as a row with op 'same' and a null text, so the view can
 * draw the break rather than silently joining two distant parts of the file.
 */
export type Collapsed = { rows: Row[]; gapAfter: Set<number> }

export function collapse(rows: Row[], context = 3): Collapsed {
  const keep = new Array<boolean>(rows.length).fill(false)
  rows.forEach((r, i) => {
    if (r.op === 'same') return
    for (let k = Math.max(0, i - context); k <= Math.min(rows.length - 1, i + context); k++) keep[k] = true
  })
  const out: Row[] = []
  const gapAfter = new Set<number>()
  let skipping = false
  for (let i = 0; i < rows.length; i++) {
    if (keep[i]) {
      out.push(rows[i])
      skipping = false
    } else if (!skipping) {
      skipping = true
      if (out.length > 0) gapAfter.add(out.length - 1)
    }
  }
  return { rows: out, gapAfter }
}
