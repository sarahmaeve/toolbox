package messagetypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

// TaskStatus values — the controlled vocabulary enforced by the
// task schema's enum constraint. Mirrored as Go constants so callers
// constructing task messages don't have to remember the strings.
const (
	TaskStatusNew        = "new"
	TaskStatusInProgress = "in-progress"
	TaskStatusDone       = "done"
	TaskStatusAbandoned  = "abandoned"
)

// taskSchemaJSON is the canonical schema for the "task" MessageType.
// Baked in as a Go const so it cannot be overridden by a user's
// --schemas-dir or by editing a file at runtime — the binary IS the
// authority for what a task looks like.
//
// status is the load-bearing field: enum-validated by pkg/schema, so
// an agent that deposits status="started" gets an error listing the
// acceptable values and self-corrects in one turn.
//
// Title is required so a task always has a human-readable summary.
// Notes is optional and lets the same MessageType serve as the
// "status update with commentary" carrier — there's no separate
// "task.note" type, because the entire history of a task IS its
// message stream by subject_id.
const taskSchemaJSON = `{
	"type": "object",
	"properties": {
		"title":    {"type": "string"},
		"status":   {"type": "string", "enum": ["new", "in-progress", "done", "abandoned"]},
		"priority": {"type": "integer", "minimum": 0, "maximum": 5},
		"assignee": {"type": "string"},
		"notes":    {"type": "string"},
		"blocker":  {"type": "string"}
	},
	"required": ["title", "status"],
	"additionalProperties": false
}`

// Task returns the canonical "task" MessageType. Status is enforced
// twice — by the schema enum (structural) and by the OnIngest hook
// (semantic, redundant belt-and-braces). The redundant check exists
// so that if pkg/schema's enum support ever regresses or the schema
// const drifts, semantic enforcement still catches it.
func Task() messagestore.MessageType {
	return messagestore.MessageType{
		Name:     "task",
		Schema:   mustParseSchema(taskSchemaJSON),
		OnIngest: validateTaskContent,
	}
}

// validTaskStatuses is the closed set the OnIngest hook enforces.
// Mirrors the schema enum. Sorted for determistic error output.
var validTaskStatuses = []string{
	TaskStatusAbandoned,
	TaskStatusDone,
	TaskStatusInProgress,
	TaskStatusNew,
}

// validateTaskContent is the semantic-validation hook. The schema
// already enforces the enum at the structural layer; this is the
// belt-and-braces check.
func validateTaskContent(content json.RawMessage) error {
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(content, &p); err != nil {
		return fmt.Errorf("decode task content: %w", err)
	}
	if slices.Contains(validTaskStatuses, p.Status) {
		return nil
	}
	// This branch is unreachable if the schema validator ran first,
	// which is how DepositMessage is wired today. Kept as a defense
	// against future refactors that might bypass schema validation.
	return errors.New("task.status must be one of [" +
		strings.Join(validTaskStatuses, ", ") + "]")
}
