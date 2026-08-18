package server

import "encoding/json"

// The request bodies, named.
//
// These were declared inline inside the handlers that read them, which works
// perfectly well for a server and not at all for anything that has to describe
// one: a type with no name cannot be reflected over, referred to, or generated
// from. Naming them is what lets docs/openapi.yaml carry schemas that are the
// parser's own definition rather than a second description of it.
//
// They stay in package server rather than moving to an importable one on
// purpose. An integrator generates a client from the OpenAPI document; a Go
// caller importing these would be coupled to this server's internals, which is
// the opposite of what a described API is for.

// CreateInletBody opens a receiver.
type CreateInletBody struct {
	Address     string `json:"address"`
	Description string `json:"description"`
}

// InletTaskBody defines a task behind a receiver, on the way in and on the way
// back in when it is corrected. One shape for both routes, which is what makes
// it impossible to create a task an edit could not produce.
type InletTaskBody = inletTaskBody

// CreateGearBody forges a gear.
//
// An ALIAS, not a copy. It was a copy, and the copy drifted: it declared
// args_schema and files as json.RawMessage — "any JSON value" in the published
// document — while the handler had always wanted a string and an array of
// {path, content, encoding}. Anyone generating a client from the document sent
// args_schema as the JSON object its name invites and got 400. The test that
// exists to stop the document and the code disagreeing compared the document
// against this copy, so it stayed green the whole time.
type CreateGearBody = createGearInput

// RunGearBody is the dry run: arguments, and the network grant the operator is
// considering rather than one that has been made. An alias, for the reason
// given on CreateGearBody.
type RunGearBody = runGearInput

// CreateScheduleBody writes a clock, and its shape is a union: which fields
// matter depends on target_kind.
//
// One body for three targets rather than three routes, because they are one
// noun — "a thing that fires on a spec" — and three creates would be three
// places for the spec, the timezone and the miss rule to drift. Which fields
// are required for which kind is checked in the handler, where the operator is
// still on the other end of the error.
type CreateScheduleBody struct {
	// TargetKind is "task", "agent" or "gear". Empty means task, because every
	// caller written before a clock could dial anything else says so by saying
	// nothing.
	TargetKind string `json:"target_kind"`
	Name       string `json:"name"`
	Spec       string `json:"spec"`
	TZ         string `json:"tz"`
	OnMiss     string `json:"on_miss"`

	// A receiver task, and the body handed to it — exactly what an HTTP caller
	// would have sent.
	TaskID  *int64          `json:"task_id"`
	Payload json.RawMessage `json:"payload"`

	// An agent, and the sentence it is given. A clock wired to an agent with
	// nothing to say produces a turn with an empty prompt.
	TargetAgentID *int64 `json:"target_agent_id"`
	Instruction   string `json:"instruction"`

	// A gear, and the arguments it is called with, held against that gear's own
	// schema when this is saved rather than at 03:00 every night.
	TargetGearID *int64          `json:"target_gear_id"`
	Args         json.RawMessage `json:"args"`
}

// UpdateModeBody is the answer to the one question this product asks on its
// own behalf: may this install ask whether a newer release exists. "ask", "on"
// or "off" — see internal/update for why the unanswered state is a value of its
// own rather than a false.
type UpdateModeBody struct {
	Mode string `json:"mode"`
}

// InvokeGearBody runs an approved gear. No network field: the grant is the
// one the operator already made, and offering to override it here would make
// the approval a suggestion.
type InvokeGearBody struct {
	Args json.RawMessage `json:"args"`
}

// SetGearStatusBody is the approval decision and what comes with it.
type SetGearStatusBody struct {
	Status         *string       `json:"status"`
	TimeoutSeconds *int          `json:"timeout_seconds"`
	Network        *networkInput `json:"network"`
	// Environment is "browser" or empty. Beside the network because it is the
	// same kind of decision, made in the same act: what this code is allowed to
	// have, settled while its source is on the screen.
	Environment *string `json:"environment"`
}

// CreateWorkspaceBody makes a workspace and its orchestrator. An alias, for
// the reason given on CreateGearBody.
type CreateWorkspaceBody = createWorkspaceInput

// CreateAgentBody hires one.
type CreateAgentBody struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	ModelID int64  `json:"model_id"`
}

// CreateMCPServerBody installs an external MCP server. It arrives pending:
// installing is not approving.
type CreateMCPServerBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Command and Args are separate, and never one string: one string means a
	// shell, and a shell means the arguments are parsed by something with its
	// own opinion about quoting.
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	Dir            string   `json:"cwd"`
	EnvNames       []string `json:"env_names"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// UpdateMCPServerBody edits one, or changes its status — never both in one
// request, because approving what you just changed is approving something you
// have not seen.
type UpdateMCPServerBody struct {
	Status         *string   `json:"status"`
	Description    *string   `json:"description"`
	Command        *string   `json:"command"`
	Args           *[]string `json:"args"`
	Dir            *string   `json:"cwd"`
	EnvNames       *[]string `json:"env_names"`
	TimeoutSeconds *int      `json:"timeout_seconds"`
}

// ApproveMCPToolBody approves one tool, which is the granularity that matters:
// a server that grows a tool after approval does not thereby acquire it.
type ApproveMCPToolBody struct {
	Approved bool `json:"approved"`
}

// CreateMCPBindingBody grants a server to a whole workspace (agent_id absent)
// or to one agent in it.
type CreateMCPBindingBody struct {
	ServerID int64  `json:"server_id"`
	AgentID  *int64 `json:"agent_id"`
}
