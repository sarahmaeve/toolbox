// Package schema implements a narrow JSON Schema 2020-12 subset focused
// on validating payloads at API and storage boundaries. Sufficient for:
//
//   - type:object
//   - properties (with type, optional minimum/maximum)
//   - required
//   - additionalProperties:false
//
// No $ref, no allOf, no recursive schemas — those are out of scope by
// design. The package exists to give LLM-driven callers a fast,
// actionable "unknown field X; valid fields are [...]" error so they
// can self-correct without a round-trip to the developer.
//
// This package was extracted from signatory's internal/mcp/validation.go;
// it is intentionally protocol-agnostic so the same machinery can validate
// MCP tool inputs, persisted message payloads, and any other JSON
// boundary that needs strict-reject semantics.
package schema

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Schema is the parsed form of a JSON Schema. Construct via Parse.
// The zero value is not usable.
type Schema struct {
	// properties maps field name → property constraints. A nil entry
	// means "property is declared but no constraints we parse" — we
	// still treat it as a known field for additionalProperties.
	properties map[string]*property
	required   map[string]bool
	// strictReject is true when additionalProperties:false was set.
	strictReject bool
}

// property carries the subset of JSON Schema constraints we actually
// enforce for a single property: type, optional numeric bounds, and
// an optional string-valued enum.
type property struct {
	// typ is the JSON Schema type: "string", "boolean", "number",
	// "integer", "object", "array", or "" if unspecified.
	typ string
	// minimum / maximum are the bounds for numeric types. They are
	// silently ignored on non-numeric types (consistent with the rest
	// of this parser's permissiveness). The hasX flags distinguish
	// "not declared" from "declared as 0."
	minimum    float64
	hasMinimum bool
	maximum    float64
	hasMaximum bool
	// enum is the closed set of acceptable string values. Only
	// string-typed properties are enforced today; declaring enum on
	// a non-string type is silently ignored, consistent with the
	// rest of this parser's permissiveness. Nil = no constraint.
	enum []string
}

// Violation describes one schema-validation failure. It satisfies
// error so callers can return it directly, but it also carries
// structured fields (Field, Type, ValidFields) so protocol-specific
// layers can map it into their own error envelopes.
type Violation struct {
	// Field names the offending field when the error is field-specific
	// (unknown field, missing required field, type mismatch, bounds
	// violation). Empty when the input as a whole is rejected (e.g.
	// "not a JSON object").
	Field string
	// Type names the declared JSON Schema type involved in a
	// type-mismatch or bounds error. Empty otherwise.
	Type string
	// ValidFields lists the acceptable alternatives to the offending
	// input, sorted. Populated for:
	//   - "unknown field" / "missing required" violations: every
	//     property name the schema declared.
	//   - "value not in enum" violations: every value the declared
	//     enum accepts.
	// Empty for other violation kinds.
	ValidFields []string
	// Message is the human-readable description suitable for surfacing
	// directly to an LLM or user.
	Message string
}

// Error makes Violation satisfy the error interface so callers can use
// errors.Is / errors.As if they wrap it.
func (v *Violation) Error() string { return v.Message }

// Parse parses raw JSON Schema bytes into a *Schema. Unknown top-level
// schema keywords are silently ignored; callers that need a particular
// keyword honored should check StrictReject and the other accessors.
//
// Returns an error only when raw is not a JSON object or has malformed
// properties/required arrays — i.e., when the schema itself is
// structurally broken. Empty input parses to an empty schema (every
// input passes).
func Parse(raw json.RawMessage) (*Schema, error) {
	if len(raw) == 0 {
		return &Schema{}, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("schema must be a JSON object: %w", err)
	}

	s := &Schema{
		properties: make(map[string]*property),
		required:   make(map[string]bool),
	}

	// additionalProperties: false
	if ap, ok := top["additionalProperties"]; ok {
		var v bool
		if err := json.Unmarshal(ap, &v); err == nil {
			s.strictReject = !v // false → reject additionals
		}
		// additionalProperties: {} (schema) is ignored for now; we
		// treat any non-false value as "allow additional", which is
		// safe-default for forward compat.
	}

	// properties: { fieldName: { "type": "string", "minimum": 0, … }, … }
	if props, ok := top["properties"]; ok {
		var propMap map[string]json.RawMessage
		if err := json.Unmarshal(props, &propMap); err != nil {
			return nil, fmt.Errorf("properties must be an object: %w", err)
		}
		for name, def := range propMap {
			s.properties[name] = parseProperty(def)
		}
	}

	// required: ["field1", "field2"]
	if req, ok := top["required"]; ok {
		var reqList []string
		if err := json.Unmarshal(req, &reqList); err != nil {
			return nil, fmt.Errorf("required must be an array: %w", err)
		}
		for _, f := range reqList {
			s.required[f] = true
		}
	}

	return s, nil
}

// parseProperty extracts the constraints we recognize from one property
// definition. It never returns nil — an unparseable or feature-thin
// property still round-trips as a zero-valued *property so
// additionalProperties checks know the field is declared. Constraints
// we don't recognize are silently ignored.
func parseProperty(def json.RawMessage) *property {
	var raw struct {
		Type    string          `json:"type"`
		Minimum *json.Number    `json:"minimum"`
		Maximum *json.Number    `json:"maximum"`
		Enum    json.RawMessage `json:"enum"`
	}
	if err := json.Unmarshal(def, &raw); err != nil {
		return &property{}
	}
	p := &property{typ: raw.Type}
	if raw.Minimum != nil {
		if f, err := raw.Minimum.Float64(); err == nil {
			p.minimum = f
			p.hasMinimum = true
		}
	}
	if raw.Maximum != nil {
		if f, err := raw.Maximum.Float64(); err == nil {
			p.maximum = f
			p.hasMaximum = true
		}
	}
	if len(raw.Enum) > 0 {
		var values []string
		// Only string enums are supported today; an enum that contains
		// non-string values is silently ignored (the unmarshal into
		// []string will fail). A future extension could parse mixed
		// enums via []json.RawMessage.
		if err := json.Unmarshal(raw.Enum, &values); err == nil && len(values) > 0 {
			p.enum = values
		}
	}
	return p
}

// StrictReject reports whether the schema had additionalProperties:false.
// Callers that want to enforce strict-reject at registration time
// (e.g. an MCP tool registry) can check this and refuse permissive
// schemas.
func (s *Schema) StrictReject() bool { return s.strictReject }

// Required returns the sorted list of fields the schema declared as
// required.
func (s *Schema) Required() []string {
	names := make([]string, 0, len(s.required))
	for name := range s.required {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ValidFields returns the sorted list of every property name declared
// in the schema. Used internally for error messages; exposed for
// callers that want to render a permissive remediation hint.
func (s *Schema) ValidFields() []string {
	names := make([]string, 0, len(s.properties))
	for name := range s.properties {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Validate checks that input conforms to the schema. contextName is
// included in error messages so LLM callers see what they were trying
// to validate (a tool name, a message type, a route — whatever the
// caller chooses). Returns nil if input is valid.
//
// Validation rules:
//  1. Input must be a JSON object (or null/absent → treated as {}).
//  2. If additionalProperties:false, any field not in properties is
//     rejected with the field named in the message and ValidFields
//     populated.
//  3. Every required field must be present.
//  4. Each present field's value must match the declared type (string,
//     bool, number/integer, object, array). Type mismatch is an error.
//  5. Numeric bounds are enforced after the type check passes; values
//     that overflow float64 are rejected as unverifiable rather than
//     silently accepted.
func (s *Schema) Validate(contextName string, raw json.RawMessage) *Violation {
	// Null or missing input → treat as empty object.
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return &Violation{
			Message: fmt.Sprintf("input for %s must be a JSON object: %s", contextName, err.Error()),
		}
	}

	validFieldsList := s.ValidFields()

	// 1. Check for additional (unknown) properties.
	if s.strictReject {
		for name := range fields {
			if _, ok := s.properties[name]; !ok {
				return &Violation{
					Field:       name,
					ValidFields: validFieldsList,
					Message: fmt.Sprintf("unknown field %q in input for %s. Valid fields: [%s]",
						name, contextName, joinFields(validFieldsList)),
				}
			}
		}
	}

	// 2. Check required fields are present.
	for field := range s.required {
		if _, ok := fields[field]; !ok {
			return &Violation{
				Field:       field,
				ValidFields: validFieldsList,
				Message:     fmt.Sprintf("required field %q missing in input for %s", field, contextName),
			}
		}
	}

	// 3. Type-check and bounds-check present fields with a declared
	//    type. Type is checked first: "limit":1.5 against an integer
	//    schema reports "not an integer" rather than a bounds violation
	//    — the type error is the more fundamental one.
	for name, raw := range fields {
		prop, ok := s.properties[name]
		if !ok || prop == nil || prop.typ == "" {
			continue
		}
		if err := checkType(prop.typ, raw); err != nil {
			return &Violation{
				Field:   name,
				Type:    prop.typ,
				Message: fmt.Sprintf("field %q in input for %s: %s", name, contextName, err.Error()),
			}
		}
		if v := checkEnum(prop, name, contextName, raw); v != nil {
			return v
		}
		if err := checkBounds(prop, raw); err != nil {
			return &Violation{
				Field:   name,
				Type:    prop.typ,
				Message: fmt.Sprintf("field %q in input for %s: %s", name, contextName, err.Error()),
			}
		}
	}

	return nil
}

// joinFields renders a slice of field names as a comma-separated list
// for error messages. Lives here (rather than importing strings) only
// to keep the package's stdlib surface narrow; an inlineable helper.
func joinFields(fields []string) string {
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0]
	}
	n := len(fields) - 1
	for _, f := range fields {
		n += len(f)
	}
	out := make([]byte, 0, n)
	for i, f := range fields {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, f...)
	}
	return string(out)
}

// checkType validates that raw JSON matches the expected JSON Schema
// type. The narrowed subset covers: string, boolean, number, integer,
// object, array. "integer" is strictly stricter than "number" —
// 1.5 passes "number" but fails "integer", which is the spec's
// behavior.
func checkType(declaredType string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	switch declaredType {
	case "string":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected string, got %s", describeJSON(raw))
		}
	case "boolean":
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected boolean, got %s", describeJSON(raw))
		}
	case "number":
		var v json.Number
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected number, got %s", describeJSON(raw))
		}
	case "integer":
		// json.Number accepts both integers and floats — we have to
		// round-trip through Int64 to reject 1.5 against an integer
		// schema.
		var v json.Number
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected integer, got %s", describeJSON(raw))
		}
		if _, err := v.Int64(); err != nil {
			return fmt.Errorf("expected integer, got %s (%s)", v.String(), describeJSON(raw))
		}
	case "object":
		var v map[string]json.RawMessage
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected object, got %s", describeJSON(raw))
		}
	case "array":
		var v []json.RawMessage
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("expected array, got %s", describeJSON(raw))
		}
	}
	return nil
}

// checkEnum enforces that a string-typed field's value lies in the
// declared enum set. Returns nil when no enum is declared, when the
// property isn't a string, or when the value matches a declared
// member. The Violation's Message and ValidFields list the
// acceptable values so an LLM client sees them inline and
// self-corrects without a docs lookup.
//
// Non-string types with declared enum are silently skipped (consistent
// with checkBounds-on-non-numeric being a no-op). A future extension
// could broaden this.
func checkEnum(prop *property, field, contextName string, raw json.RawMessage) *Violation {
	if len(prop.enum) == 0 {
		return nil
	}
	if prop.typ != "string" {
		return nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		// checkType already ran and passed, so getting here means a
		// caller reordered the pipeline. Don't second-guess the type
		// system in a defensive check.
		return nil
	}
	if slices.Contains(prop.enum, v) {
		return nil
	}
	// Sort a copy for stable error text without mutating the schema.
	allowed := append([]string(nil), prop.enum...)
	slices.Sort(allowed)
	return &Violation{
		Field:       field,
		Type:        prop.typ,
		ValidFields: allowed,
		Message: fmt.Sprintf("field %q in input for %s: value %q not in enum [%s]",
			field, contextName, v, joinFields(allowed)),
	}
}

// checkBounds enforces minimum/maximum on numeric types after the
// type check has passed. Silently no-ops for non-numeric types and
// for properties that declared no bounds. Any numeric value that
// can't be represented as float64 (e.g., 1e400 overflowing to
// infinity) is rejected rather than silently accepted: an
// unverifiable value must not bypass bounds.
func checkBounds(prop *property, raw json.RawMessage) error {
	if !prop.hasMinimum && !prop.hasMaximum {
		return nil
	}
	if prop.typ != "number" && prop.typ != "integer" {
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		// checkType already ran and passed, so getting here means a
		// caller reordered the pipeline and skipped the type check.
		return fmt.Errorf("internal error: bounds check on non-numeric JSON value %s", describeJSON(raw))
	}
	v, err := n.Float64()
	if err != nil {
		return fmt.Errorf("value %s is outside the representable numeric range", n.String())
	}
	if prop.hasMinimum && v < prop.minimum {
		return fmt.Errorf("value %s is below minimum %s",
			n.String(), formatBound(prop.minimum))
	}
	if prop.hasMaximum && v > prop.maximum {
		return fmt.Errorf("value %s is above maximum %s",
			n.String(), formatBound(prop.maximum))
	}
	return nil
}

// formatBound renders a numeric bound without trailing ".0" when the
// bound is a whole number — so minimum:0 on an integer field reports
// "below minimum 0" rather than "below minimum 0.000000".
func formatBound(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// describeJSON returns a human-readable type label for the leading
// byte of a raw JSON value, for use in error messages. The label is
// a first-byte approximation — "looks like a number" means "starts
// with '-' or a digit," not "parses as a valid JSON number." Good
// enough for error messages the LLM client reads; not suitable for
// validation decisions (those use json.Unmarshal on the full value).
func describeJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	switch raw[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return "number"
	default:
		return "invalid"
	}
}
