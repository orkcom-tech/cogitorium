// Syntax highlighting, written here rather than installed.
//
// The alternatives were a highlighter library and a full editor component.
// Both are large, both pull a dependency tree into a product that ships as one
// binary with its UI embedded, and neither is needed to answer the actual
// question — "is this code, and where does one thing end and the next begin".
//
// So: one tokenizer, parameterised per language. It is a lexer, not a parser.
// It will not understand that a `<` is a generic here and a comparison there,
// and it does not try. What it gets right is the part that carries the reading:
// comments recede, strings are one object, keywords and numbers stand out.

export type TokenKind = 'plain' | 'comment' | 'string' | 'keyword' | 'number' | 'punct' | 'heading' | 'meta'

export type Token = { kind: TokenKind; text: string }

type Lang = {
  /** Everything up to end of line is a comment after one of these. */
  line: string[]
  /** Opening and closing of a block comment, if the language has one. */
  block?: [string, string]
  /** Characters that open a string. A backslash escapes inside all of them. */
  quotes: string[]
  /** Reserved words. Matched whole, never inside an identifier. */
  keywords: Set<string>
  /** Lines starting with this are metadata rather than code (a shebang, a
   *  preprocessor directive, a YAML key). */
  meta?: RegExp
}

const kw = (s: string) => new Set(s.split(' '))

// Deliberately not exhaustive. A keyword list that includes every builtin
// paints half the file and stops distinguishing anything.
const GO = kw(
  'break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false iota',
)
const JS = kw(
  'async await break case catch class const continue default delete do else export extends finally for from function if import in instanceof let new of return static super switch this throw try typeof var void while yield null true false undefined interface type enum as satisfies readonly implements private public protected',
)
const PY = kw(
  'and as assert async await break class continue def del elif else except finally for from global if import in is lambda none nonlocal not or pass raise return true false try while with yield self',
)
const SH = kw('if then else elif fi case esac for while until do done function return local export readonly declare in')
const SQL = kw(
  'select from where insert into values update set delete create table drop alter index join left right inner outer on group by order limit offset union all as and or not null primary key foreign references distinct having',
)

const PLAIN: Lang = { line: [], quotes: [], keywords: new Set() }

const LANGS: Record<string, Lang> = {
  go: { line: ['//'], block: ['/*', '*/'], quotes: ['"', '`'], keywords: GO },
  js: { line: ['//'], block: ['/*', '*/'], quotes: ['"', "'", '`'], keywords: JS },
  ts: { line: ['//'], block: ['/*', '*/'], quotes: ['"', "'", '`'], keywords: JS },
  css: { line: [], block: ['/*', '*/'], quotes: ['"', "'"], keywords: new Set() },
  json: { line: [], quotes: ['"'], keywords: kw('true false null') },
  py: { line: ['#'], quotes: ['"', "'"], keywords: PY, meta: /^#!/ },
  sh: { line: ['#'], quotes: ['"', "'"], keywords: SH, meta: /^#!/ },
  sql: { line: ['--'], block: ['/*', '*/'], quotes: ["'", '"'], keywords: SQL },
  yaml: { line: ['#'], quotes: ['"', "'"], keywords: kw('true false null yes no'), meta: /^\s*[\w.-]+:/ },
  toml: { line: ['#'], quotes: ['"', "'"], keywords: kw('true false'), meta: /^\s*\[.*\]\s*$/ },
  html: { line: [], block: ['<!--', '-->'], quotes: ['"', "'"], keywords: new Set() },
}

/** Which lexer a path gets. Unknown extensions are rendered as plain text
 *  rather than guessed at — a wrong highlighter is worse than none. */
export function langOf(path: string): string {
  const name = path.slice(path.lastIndexOf('/') + 1).toLowerCase()
  const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : ''
  switch (ext) {
    case 'go':
      return 'go'
    case 'js':
    case 'jsx':
    case 'mjs':
    case 'cjs':
      return 'js'
    case 'ts':
    case 'tsx':
      return 'ts'
    case 'css':
      return 'css'
    case 'json':
      return 'json'
    case 'py':
      return 'py'
    case 'sh':
    case 'bash':
    case 'zsh':
      return 'sh'
    case 'sql':
      return 'sql'
    case 'yaml':
    case 'yml':
      return 'yaml'
    case 'toml':
      return 'toml'
    case 'html':
    case 'htm':
    case 'svg':
    case 'xml':
      return 'html'
    case 'md':
    case 'markdown':
      return 'md'
    default:
      // Files that are their own name rather than their own extension.
      if (name === 'dockerfile' || name === 'makefile') return 'sh'
      return ''
  }
}

const IDENT = /[A-Za-z_$][A-Za-z0-9_$]*/y
const NUMBER = /0[xXbBoO][0-9a-fA-F_]+|\d[\d_]*(\.[\d_]+)?([eE][+-]?\d+)?/y
const PUNCT = /[{}()[\].,;:+\-*/%=<>!&|^~?@#]/y

/**
 * Markdown gets its own pass.
 *
 * Running it through the generic lexer produces nothing useful — prose has no
 * keywords — and markdown's structure is per line, which the generic path is
 * not built for. It is also the format most likely to be in a workspace.
 */
function markdown(src: string): Token[] {
  const out: Token[] = []
  let fenced = false
  for (const line of src.split(/(?<=\n)/)) {
    const bare = line.replace(/\n$/, '')
    if (/^\s*(```|~~~)/.test(bare)) {
      fenced = !fenced
      out.push({ kind: 'meta', text: line })
      continue
    }
    if (fenced) {
      out.push({ kind: 'plain', text: line })
      continue
    }
    if (/^\s{0,3}#{1,6}\s/.test(bare)) {
      out.push({ kind: 'heading', text: line })
      continue
    }
    if (/^\s{0,3}>/.test(bare)) {
      out.push({ kind: 'comment', text: line })
      continue
    }
    // Inline: code spans, then bold, then links. Everything else is prose.
    let last = 0
    const re = /`[^`\n]+`|\*\*[^*\n]+\*\*|\[[^\]\n]*\]\([^)\n]*\)|^\s{0,3}([-*+]|\d+\.)\s/gm
    for (let m = re.exec(line); m; m = re.exec(line)) {
      if (m.index > last) out.push({ kind: 'plain', text: line.slice(last, m.index) })
      out.push({ kind: m[0].startsWith('`') ? 'string' : m[1] ? 'punct' : 'keyword', text: m[0] })
      last = m.index + m[0].length
    }
    if (last < line.length) out.push({ kind: 'plain', text: line.slice(last) })
  }
  return out
}

/**
 * Tokenize.
 *
 * The contract that matters: concatenating every token's text reproduces the
 * input exactly, byte for byte. The highlight layer sits underneath a real
 * textarea, so a tokenizer that drops or reorders a character does not merely
 * mis-colour — it shifts every glyph after it out from under the caret.
 */
export function tokenize(src: string, lang: string): Token[] {
  if (!lang) return [{ kind: 'plain', text: src }]
  if (lang === 'md') return markdown(src)
  const L = LANGS[lang] ?? PLAIN
  const out: Token[] = []
  let i = 0
  let plain = ''
  const flush = () => {
    if (plain) {
      out.push({ kind: 'plain', text: plain })
      plain = ''
    }
  }

  while (i < src.length) {
    const c = src[i]

    // A metadata line is claimed whole, before anything else looks at it.
    if (L.meta && (i === 0 || src[i - 1] === '\n')) {
      const end = src.indexOf('\n', i)
      const line = src.slice(i, end === -1 ? src.length : end)
      if (L.meta.test(line)) {
        // YAML and TOML mark the KEY, not the value, or every line goes flat.
        const key = /^(\s*[\w.-]+\s*:|\s*\[.*\]\s*|#!.*)/.exec(line)
        if (key) {
          flush()
          out.push({ kind: 'meta', text: key[0] })
          i += key[0].length
          continue
        }
      }
    }

    const line = L.line.find((p) => src.startsWith(p, i))
    if (line) {
      flush()
      const end = src.indexOf('\n', i)
      const stop = end === -1 ? src.length : end
      out.push({ kind: 'comment', text: src.slice(i, stop) })
      i = stop
      continue
    }

    if (L.block && src.startsWith(L.block[0], i)) {
      flush()
      const end = src.indexOf(L.block[1], i + L.block[0].length)
      const stop = end === -1 ? src.length : end + L.block[1].length
      out.push({ kind: 'comment', text: src.slice(i, stop) })
      i = stop
      continue
    }

    if (L.quotes.includes(c)) {
      flush()
      let j = i + 1
      while (j < src.length) {
        if (src[j] === '\\') {
          j += 2
          continue
        }
        if (src[j] === c) {
          j++
          break
        }
        // Only a template literal may span lines; anything else that reaches a
        // newline is an unterminated string, and stopping there keeps one typo
        // from painting the rest of the file.
        if (src[j] === '\n' && c !== '`') break
        j++
      }
      out.push({ kind: 'string', text: src.slice(i, j) })
      i = j
      continue
    }

    IDENT.lastIndex = i
    const id = IDENT.exec(src)
    if (id) {
      const word = id[0]
      if (L.keywords.has(word) || L.keywords.has(word.toLowerCase())) {
        flush()
        out.push({ kind: 'keyword', text: word })
      } else {
        plain += word
      }
      i += word.length
      continue
    }

    NUMBER.lastIndex = i
    const num = NUMBER.exec(src)
    if (num) {
      flush()
      out.push({ kind: 'number', text: num[0] })
      i += num[0].length
      continue
    }

    PUNCT.lastIndex = i
    const p = PUNCT.exec(src)
    if (p) {
      flush()
      out.push({ kind: 'punct', text: p[0] })
      i += p[0].length
      continue
    }

    plain += c
    i++
  }
  flush()
  return out
}
