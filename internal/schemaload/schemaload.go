// Package schemaload registers MessageTypes from a directory of JSON
// Schema files: each <name>.json registers a MessageType named <name>.
// Shared by toolbox-bridge and toolbox-mcp so both binaries accept the
// same --schemas-dir layout (the two copies it replaced had already
// been byte-identical — and untested).
package schemaload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sarahmaeve/toolbox/internal/cliutil"
	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// Load registers every <name>.json under dir on store and returns the
// number registered. dir may start with ~. Non-.json files and
// subdirectories are skipped; a schema that fails to parse or register
// (including permissive schemas, which the store refuses) aborts with
// an error naming the file.
func Load(store *messagestore.Store, dir string) (int, error) {
	resolved, err := cliutil.ExpandHome(dir)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", resolved, err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		typeName := strings.TrimSuffix(name, ".json")
		path := filepath.Join(resolved, name)
		raw, err := os.ReadFile(path) //nolint:gosec // G304: operator-supplied schemas dir
		if err != nil {
			return n, fmt.Errorf("read %s: %w", path, err)
		}
		sch, err := schema.Parse(json.RawMessage(raw))
		if err != nil {
			return n, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := store.RegisterType(messagestore.MessageType{
			Name:   typeName,
			Schema: sch,
		}); err != nil {
			return n, fmt.Errorf("register %s: %w", typeName, err)
		}
		n++
	}
	return n, nil
}
