package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schema fixtures used across validation tests.
var (
	// strictSchema has additionalProperties:false and two declared fields.
	strictSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {"type": "string"},
			"refresh": {"type": "boolean"}
		},
		"required": ["target"],
		"additionalProperties": false
	}`)

	// permissiveSchema has no additionalProperties constraint.
	permissiveSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"target": {"type": "string"}
		},
		"required": ["target"]
	}`)
)

// TestValidation_HappyPath verifies that valid input passes with no error.
func TestValidation_HappyPath(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"target":"github/foo/bar"}`))
	assert.Nil(t, v, "valid input should produce nil violation")
}

// TestValidation_UnknownField_StrictReject verifies that
// additionalProperties:false causes an unknown field to be rejected with
// the field named in the message. THIS IS THE CORE SECURITY INVARIANT:
// unknown fields must be rejected, not silently ignored, to catch typos
// and schema drift.
func TestValidation_UnknownField_StrictReject(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool",
		json.RawMessage(`{"target":"repo:github/foo/bar","hypothetical_flag":true}`))
	require.NotNil(t, v, "unknown field should produce a violation")
	// Message must name the offending field.
	assert.Contains(t, v.Message, "hypothetical_flag")
	// Message must list valid fields.
	assert.Contains(t, v.Message, "target")
	// Structured field must carry the offending name.
	assert.Equal(t, "hypothetical_flag", v.Field)
}

// TestValidation_UnknownField_ValidFieldsListed verifies that the
// violation includes the complete list of valid fields.
func TestValidation_UnknownField_ValidFieldsListed(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"target":"repo:x","bogus":1}`))
	require.NotNil(t, v)
	assert.ElementsMatch(t, []string{"target", "refresh"}, v.ValidFields)
}

// TestValidation_MissingRequired verifies that a missing required field
// is rejected with a message naming the missing field.
func TestValidation_MissingRequired(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"refresh":true}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "target")
	assert.Equal(t, "target", v.Field)
}

// TestValidation_TypeMismatch_String verifies that a field declared as
// "string" rejects a number value.
func TestValidation_TypeMismatch_String(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"target": 42}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "target")
	assert.Equal(t, "string", v.Type)
}

// TestValidation_TypeMismatch_Boolean verifies that a field declared as
// "boolean" rejects a string value.
func TestValidation_TypeMismatch_Boolean(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool",
		json.RawMessage(`{"target":"repo:x","refresh":"yes"}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "refresh")
	assert.Equal(t, "boolean", v.Type)
}

// TestValidation_PermissiveSchema_UnknownFieldAllowed verifies that when
// additionalProperties is not set to false, unknown fields pass through.
func TestValidation_PermissiveSchema_UnknownFieldAllowed(t *testing.T) {
	t.Parallel()
	s, err := Parse(permissiveSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool",
		json.RawMessage(`{"target":"repo:x","extra_field":"ignored"}`))
	assert.Nil(t, v, "permissive schema should allow unknown fields")
}

// TestValidation_EmptyInput_WithRequired verifies that nil/empty input
// is treated as {} and triggers required-field validation.
func TestValidation_EmptyInput_WithRequired(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(nil))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "target")
}

// TestValidation_NullInput_WithRequired verifies that JSON null is
// treated as {} and triggers required-field validation.
func TestValidation_NullInput_WithRequired(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`null`))
	require.NotNil(t, v)
}

// TestValidation_NotAnObject verifies that a non-object JSON value
// (e.g. an array) is rejected.
func TestValidation_NotAnObject(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`["array"]`))
	require.NotNil(t, v)
}

// TestValidation_ErrorMessageNamesContextAndField verifies that error
// messages reference both the context name and the offending field —
// this is the "user-readable" property LLM callers rely on for
// self-correction.
func TestValidation_ErrorMessageNamesContextAndField(t *testing.T) {
	t.Parallel()
	s, err := Parse(strictSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool",
		json.RawMessage(`{"target":"x","misspelled_refresh":true}`))
	require.NotNil(t, v)
	msg := v.Message
	assert.True(t, strings.Contains(msg, "my_tool"), "message must name the context")
	assert.True(t, strings.Contains(msg, "misspelled_refresh"), "message must name the field")
}

// TestParse_MissingAdditionalProperties verifies that a schema without
// additionalProperties does not enable StrictReject.
func TestParse_MissingAdditionalProperties(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.False(t, s.StrictReject())
}

// TestParse_AdditionalPropertiesFalse verifies that
// additionalProperties:false enables StrictReject.
func TestParse_AdditionalPropertiesFalse(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}`)
	s, err := Parse(raw)
	require.NoError(t, err)
	assert.True(t, s.StrictReject())
}

// TestParse_EmptySchema verifies that an empty schema parses without
// error and enables no constraints.
func TestParse_EmptySchema(t *testing.T) {
	t.Parallel()
	s, err := Parse(json.RawMessage(nil))
	require.NoError(t, err)
	assert.False(t, s.StrictReject())
	assert.Empty(t, s.Required())
}

// -------------------------------------------------------------------
// Integer vs number discrimination and minimum/maximum enforcement.

// integerLimitSchema: a single optional integer field with minimum 0.
var integerLimitSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"limit": {"type": "integer", "minimum": 0}
	},
	"additionalProperties": false
}`)

// numericRangeSchema exercises maximum enforcement alongside minimum
// for both numeric types in one schema.
var numericRangeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"percent":  {"type": "number",  "minimum": 0, "maximum": 100},
		"attempts": {"type": "integer", "minimum": 1, "maximum": 5}
	},
	"additionalProperties": false
}`)

// TestValidation_Integer_RejectsFloat: 1.5 must not pass an integer schema.
func TestValidation_Integer_RejectsFloat(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"limit": 1.5}`))
	require.NotNil(t, v, "1.5 must not pass an integer schema")
	assert.Contains(t, v.Message, "integer")
	assert.Contains(t, v.Message, "limit")
}

// TestValidation_Integer_AcceptsInteger proves the reject-float change
// didn't regress legitimate integer values.
func TestValidation_Integer_AcceptsInteger(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"limit": 42}`))
	assert.Nil(t, v, "an in-range integer must pass")
}

// TestValidation_Number_AcceptsFloat verifies that "number" accepts
// fractional values — the discrimination is one-way (integer strict,
// number permissive).
func TestValidation_Number_AcceptsFloat(t *testing.T) {
	t.Parallel()
	s, err := Parse(numericRangeSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"percent": 12.5}`))
	assert.Nil(t, v, "a fractional value must pass a number schema")
}

// TestValidation_Number_AcceptsInteger verifies that integer values pass
// "number" schemas.
func TestValidation_Number_AcceptsInteger(t *testing.T) {
	t.Parallel()
	s, err := Parse(numericRangeSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"percent": 12}`))
	assert.Nil(t, v, "an integer value must pass a number schema")
}

// TestValidation_Minimum_RejectsBelowBound: minimum:0 must reject -1.
func TestValidation_Minimum_RejectsBelowBound(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("my_tool", json.RawMessage(`{"limit": -1}`))
	require.NotNil(t, v, "-1 must not pass a minimum:0 schema")
	assert.Contains(t, v.Message, "minimum")
	assert.Contains(t, v.Message, "limit")
	assert.Contains(t, v.Message, " 0")
	assert.NotContains(t, v.Message, "0.0")
}

// TestValidation_Minimum_AcceptsAtBound: minimum:0 must accept 0.
func TestValidation_Minimum_AcceptsAtBound(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"limit": 0}`))
	assert.Nil(t, v, "value equal to minimum must be accepted")
}

// TestValidation_Maximum_RejectsAboveBound: symmetric upper bound.
func TestValidation_Maximum_RejectsAboveBound(t *testing.T) {
	t.Parallel()
	s, err := Parse(numericRangeSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"attempts": 10}`))
	require.NotNil(t, v, "10 must not pass a maximum:5 schema")
	assert.Contains(t, v.Message, "maximum")
	assert.Contains(t, v.Message, " 5")
}

// TestValidation_Maximum_AcceptsAtBound: inclusive upper bound.
func TestValidation_Maximum_AcceptsAtBound(t *testing.T) {
	t.Parallel()
	s, err := Parse(numericRangeSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"attempts": 5}`))
	assert.Nil(t, v, "value equal to maximum must be accepted")
}

// TestValidation_TypeErrorTakesPrecedenceOverBounds verifies the
// order-of-operations property: a value that's both wrong type AND
// out of bounds reports the type error.
func TestValidation_TypeErrorTakesPrecedenceOverBounds(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"limit": -1.5}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "integer",
		"type error must take precedence over bounds error")
	assert.NotContains(t, v.Message, "minimum",
		"bounds error must not be reported when the type is wrong")
}

// TestValidation_BoundsIgnoredOnNonNumericTypes verifies that a minimum
// declared on a string type (a schema bug, but possible) does not fire.
func TestValidation_BoundsIgnoredOnNonNumericTypes(t *testing.T) {
	t.Parallel()
	stringWithMin := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "minimum": 10}
		},
		"additionalProperties": false
	}`)
	s, err := Parse(stringWithMin)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"name": "x"}`))
	assert.Nil(t, v, "minimum on a string type must be silently ignored")
}

// TestValidation_MinimumZero_HasMinimumFlagDistinguished is the
// "declared vs zero-default" distinction test: minimum:0 in the schema
// must be enforced, not mistaken for "no minimum declared."
func TestValidation_MinimumZero_HasMinimumFlagDistinguished(t *testing.T) {
	t.Parallel()
	s, err := Parse(integerLimitSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"limit": -1}`))
	require.NotNil(t, v, "minimum:0 must be distinguishable from 'no minimum'")
	assert.Contains(t, v.Message, "minimum")
}

// TestValidation_Number_RejectsOverflow verifies that a number that
// overflows float64 is rejected when bounds are declared, rather than
// silently bypassing the bounds check.
func TestValidation_Number_RejectsOverflow(t *testing.T) {
	t.Parallel()
	s, err := Parse(numericRangeSchema)
	require.NoError(t, err)

	v := s.Validate("tool", json.RawMessage(`{"percent": 1e400}`))
	require.NotNil(t, v, "unrepresentable number must be rejected when bounds are declared")
	assert.Contains(t, v.Message, "representable")
}

// TestDescribeJSON_LabelCoverage locks in the "honest default" property:
// the default fallback is "invalid", not "number". A leading byte that
// isn't a legal JSON start (say '?' from truncated input) must not be
// misclassified, since the label goes into user-facing error messages.
func TestDescribeJSON_LabelCoverage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "null"},
		{"string", `"hello"`, "string"},
		{"object", `{}`, "object"},
		{"array", `[]`, "array"},
		{"boolean true", `true`, "boolean"},
		{"boolean false", `false`, "boolean"},
		{"null literal", `null`, "null"},
		{"positive digit", `42`, "number"},
		{"leading zero", `0.5`, "number"},
		{"negative", `-1`, "number"},
		{"stray question mark", `?garbage`, "invalid"},
		{"stray comma", `,`, "invalid"},
		{"whitespace only", ` `, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := describeJSON(json.RawMessage(tc.raw))
			assert.Equal(t, tc.want, got,
				"describeJSON(%q) must label the leading byte honestly", tc.raw)
		})
	}
}

// TestViolation_ImplementsError ensures Violation can be returned as an
// error value — callers should be able to wrap it and use errors.As.
func TestViolation_ImplementsError(t *testing.T) {
	t.Parallel()
	var _ error = (*Violation)(nil)
}

// -------------------------------------------------------------------
// Enum constraint.
//
// Locks in the load-bearing property: the error message names the
// rejected value AND lists the acceptable alternatives, so an LLM
// client can self-correct from the response without a docs lookup.

var statusEnumSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"status": {"type": "string", "enum": ["new", "in-progress", "done", "abandoned"]}
	},
	"required": ["status"],
	"additionalProperties": false
}`)

func TestEnum_AcceptsDeclaredMember(t *testing.T) {
	t.Parallel()
	s, err := Parse(statusEnumSchema)
	require.NoError(t, err)

	for _, value := range []string{"new", "in-progress", "done", "abandoned"} {
		v := s.Validate("task", json.RawMessage(`{"status":"`+value+`"}`))
		assert.Nil(t, v, "enum member %q must pass", value)
	}
}

func TestEnum_RejectsNonMember(t *testing.T) {
	t.Parallel()
	s, err := Parse(statusEnumSchema)
	require.NoError(t, err)

	v := s.Validate("task", json.RawMessage(`{"status":"started"}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "started", "error must name the rejected value")
	assert.Contains(t, v.Message, "new", "error must list the acceptable members")
	assert.Contains(t, v.Message, "abandoned")
	assert.Equal(t, "status", v.Field)
}

func TestEnum_ValidFieldsCarriesAcceptableValues(t *testing.T) {
	t.Parallel()
	s, err := Parse(statusEnumSchema)
	require.NoError(t, err)

	v := s.Validate("task", json.RawMessage(`{"status":"banana"}`))
	require.NotNil(t, v)
	assert.ElementsMatch(t,
		[]string{"abandoned", "done", "in-progress", "new"},
		v.ValidFields,
		"structured ValidFields must carry every acceptable enum value")
}

// TestEnum_OnNonStringTypeIsIgnored verifies the "permissive on the
// edges" stance: an enum declared on a number-typed property is
// silently skipped, matching how minimum/maximum behave on string
// types.
func TestEnum_OnNonStringTypeIsIgnored(t *testing.T) {
	t.Parallel()
	numericEnum := json.RawMessage(`{
		"type": "object",
		"properties": {
			"n": {"type": "integer", "enum": ["only", "strings", "supported"]}
		},
		"additionalProperties": false
	}`)
	s, err := Parse(numericEnum)
	require.NoError(t, err)
	v := s.Validate("tool", json.RawMessage(`{"n": 42}`))
	assert.Nil(t, v, "enum on non-string is a no-op, not a failure")
}

func TestEnum_TypeErrorTakesPrecedenceOverEnum(t *testing.T) {
	t.Parallel()
	s, err := Parse(statusEnumSchema)
	require.NoError(t, err)

	// 42 is neither a string nor an enum member; the type violation
	// is the more fundamental error and must win.
	v := s.Validate("task", json.RawMessage(`{"status": 42}`))
	require.NotNil(t, v)
	assert.Contains(t, v.Message, "string",
		"type error must take precedence over enum error")
	assert.NotContains(t, v.Message, "enum",
		"enum error must not be reported when the type is wrong")
}
