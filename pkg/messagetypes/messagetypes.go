// Package messagetypes provides the canonical MessageTypes that every
// toolbox binary registers at startup.
//
// These types are defined entirely in Go code — schema bytes baked
// into a const, semantic rules compiled in — so an agent cannot
// bypass them by editing a JSON file. The schema validator at
// pkg/schema produces self-documenting errors that list the
// acceptable inputs; combined with the controlled-vocabulary
// guarantee these types provide, an LLM client learns the contract
// from rejections, without needing access to this source.
//
// To consume:
//
//	for _, mt := range messagetypes.Builtin() {
//	    if err := store.RegisterType(mt); err != nil { ... }
//	}
//
// Callers that want only a subset can pick individual constructors
// (Task() etc.) directly.
package messagetypes

import (
	"encoding/json"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// Builtin returns the canonical MessageTypes every toolbox binary
// registers at startup. Schemas and OnIngest hooks are compiled in;
// the user cannot override them through --schemas-dir (additional
// types loaded from there layer on top but cannot redefine these
// names — RegisterType returns ErrTypeAlreadyRegistered for the
// collision).
func Builtin() []messagestore.MessageType {
	return []messagestore.MessageType{
		Task(),
	}
}

// must wraps schema.Parse and panics on error. Used for the const
// schema strings below — if one of them fails to parse, that's a
// program bug and the binary should refuse to start.
func mustParseSchema(raw string) *schema.Schema {
	s, err := schema.Parse(json.RawMessage(raw))
	if err != nil {
		panic("messagetypes: invalid built-in schema: " + err.Error())
	}
	return s
}
