package schemaload

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

const strictSchema = `{
	"type": "object",
	"properties": {"title": {"type": "string"}},
	"required": ["title"],
	"additionalProperties": false
}`

func newStore(t *testing.T) *messagestore.Store {
	t.Helper()
	st, err := messagestore.Open(context.Background(), messagestore.Config{
		DBPath: filepath.Join(t.TempDir(), "schemaload.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

// TestLoad_RegistersJSONSchemas: <name>.json registers a type named
// <name>; non-.json files and subdirectories are skipped.
func TestLoad_RegistersJSONSchemas(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	dir := t.TempDir()

	writeFile(t, dir, "note.json", strictSchema)
	writeFile(t, dir, "digest.json", strictSchema)
	writeFile(t, dir, "README.txt", "not a schema")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o700))

	n, err := Load(st, dir)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.ElementsMatch(t, []string{"digest", "note"}, st.RegisteredTypes())
}

// TestLoad_RejectsMalformedSchema: a broken schema aborts with an error
// naming the offending file so the operator can fix it.
func TestLoad_RejectsMalformedSchema(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	dir := t.TempDir()
	writeFile(t, dir, "broken.json", `{"properties": "not an object"}`)

	_, err := Load(st, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken.json")
}

// TestLoad_RejectsPermissiveSchema: the store refuses schemas without
// additionalProperties:false; Load surfaces that with the type name.
func TestLoad_RejectsPermissiveSchema(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	dir := t.TempDir()
	writeFile(t, dir, "loose.json", `{"type":"object","properties":{"x":{"type":"string"}}}`)

	_, err := Load(st, dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, messagestore.ErrSchemaNotStrict)
	assert.Contains(t, err.Error(), "loose")
}

// TestLoad_MissingDirErrors: a nonexistent directory is an error, not
// an empty success — the operator pointed at the wrong place.
func TestLoad_MissingDirErrors(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	_, err := Load(st, filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
}
