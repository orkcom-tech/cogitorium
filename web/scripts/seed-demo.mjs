// Build the install the documentation screenshots are taken from.
//
// This exists because re-shooting was a manual afternoon: somebody had to have
// a populated install lying around, and the one they had was theirs, with
// their workspace names in it. So the pictures went stale, and the last
// redesign shipped thirteen of them showing software that had been deleted.
//
// The shooter names what it expects — workspace 1 has a blueprint worth
// drawing, workspace 4 has a receiver with two tasks, workspace 5 has a
// schedule and three variables — and this is the other half of that contract.
// Together they make re-shooting one command anybody can run.
//
//   COGITORIUM_TOKEN=... node scripts/seed-demo.mjs http://127.0.0.1:8894
//
// Everything here goes through the API, not the database. A fixture written
// straight into SQLite is a fixture that can be shaped in ways the product
// cannot produce, and a screenshot of one is a picture of software that does
// not exist.

const base = process.argv[2] || 'http://127.0.0.1:8894'
const token = process.env.COGITORIUM_TOKEN
if (!token) {
  console.error('Set COGITORIUM_TOKEN to an admin token for the install being seeded.')
  process.exit(2)
}

async function api(method, path, body) {
  const res = await fetch(`${base}/api/v1${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  if (!res.ok) {
    throw new Error(`${method} ${path} -> ${res.status} ${text.slice(0, 300)}`)
  }
  return text ? JSON.parse(text) : null
}

const post = (p, b) => api('POST', p, b)
const patch = (p, b) => api('PATCH', p, b)
const put = (p, b) => api('PUT', p, b)
const get = (p) => api('GET', p)

const step = (what) => console.log('  ' + what)

async function main() {
  // A fresh install, and this refuses rather than adding to one that is not.
  //
  // Half-seeding is worse than not seeding: the second run fails partway,
  // leaves a provider without its models and a workspace without its wires,
  // and the screenshots taken afterwards picture that. A fixture for pictures
  // has to be disposable and identical every time.
  const existing = await get('/workspaces')
  if (existing.length > 0) {
    throw new Error(
      `${base} already has ${existing.length} workspace(s) in it.\n\n` +
        'This builds a demonstration install from nothing and will not add itself to\n' +
        'yours. Point it at an empty data directory:\n\n' +
        '  cogitorium serve --data $(mktemp -d) --listen 127.0.0.1:8894',
    )
  }

  console.log(`Seeding ${base}`)

  // A provider with an unreachable address, on purpose. Every screenshot the
  // documentation uses is a structural one — a catalogue, a blueprint, an
  // approval trail — and none of them needs a model to answer. Requiring a
  // real key here would mean nobody without one could re-shoot, which is how
  // the pictures went stale in the first place.
  // Colour is a hue in degrees rather than a swatch: the palette mixes every
  // neutral towards it, so what is stored is the angle.
  step('a provider and three models')
  const provider = await post('/providers', {
    name: 'Anthropic', type: 'anthropic',
    base_url: 'https://api.anthropic.com', api_key: 'not-a-real-key',
  })
  const local = await post('/providers', {
    name: 'Ollama (this machine)', type: 'openai-compatible',
    base_url: 'http://127.0.0.1:11434/v1', api_key: 'ollama',
  })
  const big = await post('/models', {
    provider_id: provider.id, model_name: 'claude-opus-4', label: 'Opus — the one that thinks',
  })
  const small = await post('/models', {
    provider_id: local.id, model_name: 'qwen2.5-coder:14b', label: 'Qwen Coder — local, free',
  })
  await post('/models', {
    provider_id: local.id, model_name: 'llama3.1:8b', label: 'Llama — local, for the cheap jobs',
  })

  step('people and two teams')
  const platform = await post('/teams', { name: 'Platform' })
  const research = await post('/teams', { name: 'Research' })
  for (const [name, team] of [['dana', platform], ['ravi', platform], ['mira', research]]) {
    // The response wraps the user beside the one-time token, because a token
    // shown twice is a token stored somewhere.
    const created = await post('/users', {
      name, password: 'correct-horse-battery-staple', role: 'member',
    })
    await post(`/teams/${team.id}/members`, { user_id: created.user.id })
  }

  // Workspace 1 is the one every structural shot is taken over.
  step('workspace 1 — the one with a blueprint worth drawing')
  const one = await post('/workspaces', {
    name: 'Release engineering',
    description: 'Cuts releases, checks them, and files what it found',
    orchestrator_model_id: big.id,
  })
  await patch(`/workspaces/${one.id}`, { hue: 225 })
  await post(`/workspaces/${one.id}/teams`, { team_id: platform.id })
  await post(`/workspaces/${one.id}/teams`, { team_id: research.id })

  const agents = {}
  for (const [key, spec] of Object.entries({
    builder: { name: 'Builder', role: 'Builds the artifact and nothing else', model_id: small.id },
    reviewer: { name: 'Reviewer', role: 'Reads the diff and refuses it out loud', model_id: big.id },
    notes: { name: 'Release notes', role: 'Writes what changed, from the commits', model_id: small.id },
    ops: { name: 'Ops', role: 'Tells the rest of the company a build is signed', model_id: small.id },
  })) {
    agents[key] = await post(`/workspaces/${one.id}/agents`, spec)
  }

  // Wires, which ARE the permission rather than a picture of one.
  const orchestrator = (await get(`/workspaces/${one.id}/agents`)).find((a) => a.is_orchestrator)
  for (const [from, to] of [
    [orchestrator, agents.builder],
    [orchestrator, agents.notes],
    [agents.builder, agents.reviewer],
    [agents.reviewer, agents.ops],
  ]) {
    await post(`/workspaces/${one.id}/wires`, { from_agent_id: from.id, to_agent_id: to.id })
  }

  step('workspaces 2 and 3 — so the list and the map have a shape')
  const two = await post('/workspaces', {
    name: 'Support triage', description: 'Reads what arrives and files it',
    orchestrator_model_id: small.id,
  })
  await patch(`/workspaces/${two.id}`, { hue: 25 })
  const three = await post('/workspaces', {
    name: 'Docs', description: 'Keeps the guide true', orchestrator_model_id: small.id,
  })
  await patch(`/workspaces/${three.id}`, { hue: 155 })
  await post(`/workspaces/${three.id}/teams`, { team_id: research.id })

  step('workspace 4 — a receiver with two tasks')
  const four = await post('/workspaces', {
    name: 'Ticket intake', description: 'A door for the helpdesk',
    orchestrator_model_id: small.id,
  })
  await patch(`/workspaces/${four.id}`, { hue: 285 })
  // Created, then read back rather than taken from the create response: that
  // response carries the inlet's one-time key beside it, and its shape is
  // about handing over a secret rather than about identifying a row.
  await post(`/workspaces/${four.id}/inlets`, {
    address: 'helpdesk', description: 'Where the helpdesk posts a ticket',
  })
  const inlet = (await get(`/workspaces/${four.id}/inlets`))[0]
  // A task names its agent by NAME, not by id: the pair is what an operator
  // reads on the screen, and an id would make the fixture depend on insertion
  // order.
  const triage = (await get(`/workspaces/${four.id}/agents`)).find((a) => a.is_orchestrator)
  await post(`/inlets/${inlet.id}/tasks`, {
    name: 'classify',
    accepts: 'json',
    schema: { type: 'object', required: ['subject', 'body'],
      properties: { subject: { type: 'string' }, body: { type: 'string' } } },
    agent: triage.name,
    instruction: 'Answer with one of: platform, research, billing. Nothing else.',
  })
  await post(`/inlets/${inlet.id}/tasks`, {
    name: 'summarise',
    accepts: 'json',
    schema: { type: 'object', required: ['body'], properties: { body: { type: 'string' } } },
    agent: triage.name,
    instruction: 'Summarise the ticket in under sixty words.',
  })

  step('workspace 5 — a schedule at 03:00 and three variables')
  const five = await post('/workspaces', {
    name: 'Nightly', description: 'Work that happens while nobody is watching',
    orchestrator_model_id: small.id,
  })
  await patch(`/workspaces/${five.id}`, { hue: 50 })
  // PUT on the name, because setting a variable is idempotent: writing REGION
  // twice is one variable, not two.
  await put(`/workspaces/${five.id}/env/REGION`, {
    kind: 'variable', value: 'eu-central-1', description: 'Where the nightly build publishes to',
  })
  await put(`/workspaces/${five.id}/env/REPORT_TO`, {
    kind: 'variable', value: 'ops@example.com', description: 'Who hears when it fails',
  })
  // A secret, so the variables screen has one to show being withheld — which
  // is the thing that picture is actually about.
  await put(`/workspaces/${five.id}/env/DEPLOY_TOKEN`, {
    kind: 'secret', value: 'this is never shown again',
    description: 'Handed to a granted gear as a stand-in the gate swaps at the edge',
  })

  const nightly = (await get(`/workspaces/${five.id}/agents`)).find((a) => a.is_orchestrator)
  await post(`/workspaces/${five.id}/schedules`, {
    target_kind: 'agent',
    name: 'Nightly reconcile',
    spec: '0 3 * * *',
    tz: 'Europe/Berlin',
    target_agent_id: nightly.id,
    instruction: 'Compare last night’s build against the manifest and report what drifted.',
  })

  // The library. Text lives in Contextverse and is versioned there; what this
  // writes is the index entry beside it.
  step('instructions')
  for (const [name, description, tags, text] of [
    ['never-invent-a-version', 'Say you do not know instead of guessing', ['discipline'],
      'If you do not know the version, say so and stop.\n\n' +
        'A plausible wrong version is worse than no answer: it is the kind of mistake ' +
        'that survives review, because it looks like knowledge.'],
    ['refuse-an-empty-diff', 'A review of nothing is not a review', ['review'],
      'If the diff is empty, say so and refuse.\n\n' +
        'Reviewing nothing produces a confident approval of nothing, which is the ' +
        'worst possible thing to have on the record.'],
    ['house-voice', 'How anything written here should read', ['writing'],
      'Short sentences. No adjective you cannot defend.\n\n' +
        'Never describe your own output as comprehensive, thorough or robust. ' +
        'If it is, that will be visible; if it is not, you have said something untrue.'],
  ]) {
    await post('/instructions', { name, description, tags, text })
  }

  // Gears, and one of them approved.
  //
  // Three shots are about this: the catalogue, what approving grants before
  // you agree to it, and who let this code run. All three need a gear whose
  // source is worth reading and a trail that exists — an approval screen over
  // an empty catalogue illustrates nothing.
  step('gears, one approved with a trail')
  const changelog = await post('/gears', {
    name: 'changelog',
    description: 'Turns a range of commits into release notes',
    tags: ['release', 'git'],
    runtime: 'python',
    entrypoint: 'main.py',
    args_schema: JSON.stringify({
      type: 'object',
      required: ['from', 'to'],
      properties: {
        from: { type: 'string', description: 'The earlier tag' },
        to: { type: 'string', description: 'The later tag' },
      },
    }),
    files: [{
      path: 'main.py',
      content: [
        'import json, subprocess, sys',
        '',
        'args = json.load(sys.stdin)',
        'span = "%s..%s" % (args["from"], args["to"])',
        'log = subprocess.run(["git", "log", "--oneline", span],',
        '                     capture_output=True, text=True, check=True)',
        'print(json.dumps({"notes": log.stdout.strip().split("\\n")}))',
        '',
      ].join('\n'),
    }],
  })
  await patch(`/gears/${changelog.id}`, { status: 'approved' })

  const fetcher = await post('/gears', {
    name: 'fetch_manifest',
    description: 'Reads the published manifest for a release',
    tags: ['release', 'http'],
    runtime: 'python',
    entrypoint: 'main.py',
    args_schema: JSON.stringify({
      type: 'object', required: ['tag'], properties: { tag: { type: 'string' } },
    }),
    files: [{
      path: 'main.py',
      content: [
        'import json, sys, urllib.request',
        '',
        'args = json.load(sys.stdin)',
        'url = "https://api.github.com/repos/orkcom-tech/cogitorium/releases/tags/" + args["tag"]',
        'with urllib.request.urlopen(url) as r:',
        '    print(json.dumps({"published": json.load(r)["published_at"]}))',
        '',
      ].join('\n'),
    }],
  })
  // Left PENDING on purpose. The review screen is about what approving would
  // grant, so it needs a gear that is asking for something and has not been
  // given it yet — the network allowlist is set in the same act as the
  // approval, beside the source.
  void fetcher

  console.log('\nSeeded. Now shoot it:')
  console.log(`  COGITORIUM_TOKEN=$COGITORIUM_TOKEN node scripts/shoot-docs.mjs ${base}`)
}

main().catch((err) => {
  console.error('\n' + err.message)
  process.exit(1)
})
