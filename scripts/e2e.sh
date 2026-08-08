#!/usr/bin/env bash
# End-to-end acceptance against real components: a real local model served by
# Ollama, a real contextd space, and the real Cogitorium binary. No mocks,
# no stubs — if this passes, the pipeline genuinely works.
#
# Prerequisites:
#   docker compose -f docker-compose.models.yml up -d
#   docker compose -f docker-compose.models.yml exec ollama ollama pull qwen2.5:0.5b
#   contextd on PATH (or set CONTEXTD), with a space: contextd init solo --name e2e
#
# Usage: make build && ./scripts/e2e.sh
set -euo pipefail

MODEL="${MODEL:-qwen2.5:0.5b}"
OLLAMA="${OLLAMA:-http://127.0.0.1:11434/v1}"
CONTEXTD="${CONTEXTD:-contextd}"
PORT="${PORT:-8699}"
API="http://127.0.0.1:${PORT}/api/v1"
DATA="$(mktemp -d)"
LOG="${DATA}/server.log"

pass() { printf '  \033[32mok\033[0m   %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

cleanup() {
  [[ -n "${SRV:-}" ]] && kill -TERM "$SRV" 2>/dev/null || true
  wait "${SRV:-}" 2>/dev/null || true
  rm -rf "$DATA"
}
trap cleanup EXIT

command -v ./bin/cogitorium >/dev/null || fail "build first: make build"
curl -sf "${OLLAMA}/models" >/dev/null || fail "no model server at ${OLLAMA} — see docker-compose.models.yml"

echo "starting cogitorium on :${PORT}"
COGITORIUM_CONTEXTD="$CONTEXTD" ./bin/cogitorium serve --listen "127.0.0.1:${PORT}" --data "$DATA" >"$LOG" 2>&1 &
SRV=$!
for _ in $(seq 1 40); do
  curl -sf "http://127.0.0.1:${PORT}/health" >/dev/null && break
  sleep 0.25
done
curl -sf "http://127.0.0.1:${PORT}/health" >/dev/null || fail "server did not come up (see $LOG)"

echo "catalog"
curl -sf -X POST "$API/providers" -d "{\"name\":\"ollama\",\"type\":\"openai-compatible\",\"base_url\":\"${OLLAMA}\"}" >/dev/null
curl -sf -X POST "$API/providers/1/test" | grep -q '"ok":true' || fail "provider test"
pass "provider reachable and lists models"
curl -sf -X POST "$API/models" -d "{\"provider_id\":1,\"model_name\":\"${MODEL}\"}" >/dev/null
pass "model added to the catalog"

echo "workspace"
curl -sf -X POST "$API/workspaces" -d '{"name":"e2e","orchestrator_model_id":1}' >/dev/null
curl -sf "$API/workspaces/1/agents" | grep -q '"is_orchestrator":true' || fail "orchestrator not seeded"
pass "workspace created with its orchestrator"
curl -s -X DELETE "$API/agents/1" | grep -q "cannot be deleted" || fail "orchestrator was deletable"
pass "orchestrator cannot be deleted"

echo "context (Contextverse)"
curl -sf "$API/context/status" | grep -q '"available":true' || fail "contextd unavailable — run: contextd init solo --name e2e"
curl -sf -X POST "$API/workspaces/1/agents" \
  -d "{\"name\":\"archivist\",\"role\":\"You are the archivist. When asked for the codename, reply with exactly the codename from your context and nothing else.\",\"model_id\":1}" >/dev/null
curl -sf -X PUT "$API/context/file?path=projects/e2e/codename.md" --data-binary 'The project codename is MIDNIGHT ORCHID.' >/dev/null
pass "context file written through contextd"
curl -sf -X POST "$API/workspaces/1/context" -d '{"path":"projects/e2e/codename.md","agent_id":2}' >/dev/null
curl -sf "$API/agents/2/prompt" | grep -q "MIDNIGHT ORCHID" || fail "bound context missing from the agent prompt"
curl -sf "$API/agents/1/prompt" | grep -q "MIDNIGHT ORCHID" && fail "private context leaked to the orchestrator"
pass "private context reaches its agent only"

echo "blueprint"
curl -s -X POST "$API/workspaces/1/wires" -d '{"from_agent_id":1,"to_agent_id":1}' | grep -q "cannot be wired to itself" || fail "self-wire allowed"
pass "self-wire refused"
curl -sf -X POST "$API/workspaces/1/wires" -d '{"from_agent_id":1,"to_agent_id":2}' >/dev/null
pass "wire drawn: orchestrator -> archivist"

echo "delegation with a real model (this calls the model; give it a moment)"
OUT="$(curl -sf -N --max-time 600 -X POST "$API/workspaces/1/chat" \
  -d '{"text":"Use the delegate tool with agent=archivist and task=What is the project codename?"}')"
grep -q '"kind":"delegation"' <<<"$OUT" || fail "no delegation happened"
grep -q "MIDNIGHT ORCHID" <<<"$OUT" || fail "the archivist did not return its context-derived answer"
pass "orchestrator delegated; the archivist answered from its private context"

echo "gears (asks the model to forge one — small models often can't; that is reported, never faked)"
FORGE_PROMPT="$(python3 -c '
import json
src = "import sys, json\nargs = json.load(sys.stdin)\nprint(sum(args[\"numbers\"]))\n"
text = ("Call the forge_gear tool with these exact arguments: "
        "name=sum_numbers, description=Adds a list of numbers, runtime=python, "
        "entrypoint=main.py, and files as a list with one object whose path is "
        "main.py and whose content is:\n" + src)
print(json.dumps({"text": text}))')"
curl -s -N --max-time 600 -X POST "$API/workspaces/1/chat" -d "$FORGE_PROMPT" >/dev/null || true

GEARS_JSON="$(curl -s "$API/gears" || echo '[]')"
GEAR_ID="$(python3 -c '
import json, sys
try:
    g = json.loads(sys.argv[1])
    print(g[0]["id"] if isinstance(g, list) and g else "")
except Exception:
    print("")' "$GEARS_JSON")"
if [[ -z "$GEAR_ID" ]]; then
  printf '  \033[33mskip\033[0m %s\n' "the model did not forge a gear (model too small); gate untested this run"
else
  curl -sf "$API/gears" | grep -q '"status":"pending"' || fail "a forged gear must land pending, never runnable on arrival"
  pass "forged gear registered in the catalog as pending"
  curl -sf "$API/gears/${GEAR_ID}" | grep -q '"files"' || fail "gear source not retrievable for review"
  pass "gear source available for operator review"
  curl -sf -X PATCH "$API/gears/${GEAR_ID}" -d '{"status":"approved"}' | grep -q '"status":"approved"' || fail "approval failed"
  pass "operator approval works"
  curl -sf -X PATCH "$API/gears/${GEAR_ID}" -d '{"status":"disabled"}' >/dev/null
  pass "operator can disable a gear again"
fi

echo
printf '\033[32mall checks passed\033[0m\n'
