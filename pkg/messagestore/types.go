// Package messagestore implements a SQLite-backed typed-message store
// for agent coordination. Sessions group related messages; messages
// carry a role (who emitted), a type (what shape, validated against a
// registered MessageType schema), and an opaque payload.
//
// The store is pull-based: producers Deposit messages, consumers
// GetMessages / GetLatestMessage. No event handlers, no fan-out — that
// model trades simplicity for fewer delivery-semantics surprises.
//
// Two-stage validation on every deposit:
//   - Stage 1, structural: the message Content is validated against
//     the schema registered for its Type via pkg/schema. This catches
//     unknown fields, missing required fields, type mismatches, and
//     bounds violations before any business logic runs.
//   - Stage 2, semantic (optional): if the registered MessageType has
//     an OnIngest hook, it runs against the decoded Content. Hooks
//     enforce cross-field rules that schemas can't express
//     (discriminated unions, paired-field invariants, role-conditional
//     allowances).
//
// This package was modeled on signatory's internal/pipeline, with the
// hard-coded role / msg_type whitelists replaced by Config.AllowedRoles
// and a MessageType registry.
package messagestore

import (
	"encoding/json"
	"time"

	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// Session represents one coordinated unit of work — typically one agent
// dispatch, one /command invocation, or one orchestrator run. Sessions
// group their messages by SessionID; Target is a free-form identifier
// the calling project gives meaning to (a task ID, a URI, an issue
// number, whatever).
type Session struct {
	ID        string    `json:"id"`
	Target    string    `json:"target"`
	Status    string    `json:"status"` // active, complete, failed
	CreatedAt time.Time `json:"created_at"`
	Metadata  string    `json:"metadata,omitempty"`
}

// Message is one typed, role-scoped piece of content within a session.
//
//   - Role identifies the coarse who (controlled vocab from the store
//     config) — e.g. "agent", "user", "orchestrator".
//   - SenderID identifies the precise producer (modeled on signatory's
//     analyst_id) — e.g. "agent.code-reviewer.v2". Multiple distinct
//     SenderIDs may share a Role.
//   - Type names the schema the Content was validated against at ingest.
//   - SubjectID is an opaque external reference the calling project gives
//     meaning to (a ticket, a file path, a URL, a topic). Independent of
//     SessionID — SessionID groups by run, SubjectID groups by topic
//     across runs.
//
// SenderID and SubjectID are optional (empty = unset). All other fields
// except Metadata are required at deposit.
type Message struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"`
	SenderID  string          `json:"sender_id,omitempty"`
	Type      string          `json:"type"`
	SubjectID string          `json:"subject_id,omitempty"`
	Content   json.RawMessage `json:"content"`
	CreatedAt time.Time       `json:"created_at"`
	Metadata  string          `json:"metadata,omitempty"`
}

// MessageFilter narrows GetMessages / GetLatestMessage queries.
// At least one of SessionID / Role / SenderID / Type / SubjectID must
// be set; an empty MessageFilter is rejected with ErrFilterRequired
// (the contract is "you must be looking for something specific").
// Set fields are combined conjunctively (AND).
//
// SessionID-empty is the cross-session search mode — query by
// SubjectID for "all messages about ticket-1234 across every run",
// or by SenderID for "everything that producer ever deposited."
// Pair with Limit to bound the response.
type MessageFilter struct {
	SessionID string
	Role      string
	SenderID  string
	Type      string
	SubjectID string

	// Limit caps the number of rows returned, newest matches preferred
	// when the query orders DESC. Zero means unlimited (the historical
	// behavior, preserved for SessionID-only queries that expect the
	// full session log).
	Limit int
}

// MessageType is a registered payload shape. Name is the discriminator
// stored in Message.Type; Schema validates Content structurally at
// ingest; OnIngest is an optional Go callback for semantic rules the
// schema can't express.
type MessageType struct {
	// Name is the discriminator. Must be unique within a Store.
	// Naming convention is the caller's choice — dotted names like
	// "task.completed" / "agent.handoff" are conventional but not
	// enforced.
	Name string

	// Schema is the parsed JSON Schema for Content. Required.
	// Must have additionalProperties:false; the store enforces this
	// at RegisterType time so a permissive schema can't accidentally
	// slip into production.
	Schema *schema.Schema

	// OnIngest is an optional semantic validator run after the
	// schema check passes. It receives the raw Content bytes (the
	// same bytes that will be persisted) and should return a non-nil
	// error to reject the deposit.
	//
	// Typical uses: discriminated-union gating (e.g. "field X is only
	// allowed when type is Y"), cross-field invariants (e.g. "if A is
	// set, B must also be set"), controlled-vocab checks beyond what
	// JSON Schema enum covers.
	OnIngest func(content json.RawMessage) error
}
