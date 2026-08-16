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
type CreateGearBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Tags        []string        `json:"tags"`
	Runtime     string          `json:"runtime"`
	Code        string          `json:"code"`
	Entrypoint  string          `json:"entrypoint"`
	Files       json.RawMessage `json:"files"`
	ArgsSchema  json.RawMessage `json:"args_schema"`
	EnvNames    []string        `json:"env_names"`
}

// RunGearBody is the dry run: arguments, and the network grant the operator is
// considering rather than one that has been made.
type RunGearBody struct {
	Args    json.RawMessage `json:"args"`
	Network *networkInput   `json:"network"`
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
}

// CreateWorkspaceBody makes a workspace and its orchestrator.
type CreateWorkspaceBody struct {
	Name                string `json:"name"`
	Description         string `json:"description"`
	OrchestratorModelID int64  `json:"orchestrator_model_id"`
}

// CreateAgentBody hires one.
type CreateAgentBody struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	ModelID int64  `json:"model_id"`
}
