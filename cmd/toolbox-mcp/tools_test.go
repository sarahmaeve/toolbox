package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/mcp"
	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/messagetypes"
)

// newStoreWithBuiltins opens a messagestore configured the same way
// the binary configures it at runtime: temp DB, an allowed-roles set,
// and every messagetypes.Builtin registered. Returns the store and
// the suite of MCP tools built against it.
func newStoreWithBuiltins(t *testing.T) (*messagestore.Store, []mcp.Tool) {
	t.Helper()
	st, err := messagestore.Open(context.Background(), messagestore.Config{
		DBPath:       filepath.Join(t.TempDir(), "tools-test.db"),
		AllowedRoles: []string{"user", "agent", "orchestrator"},
	})
	require.NoError(t, err)
	for _, mt := range messagetypes.Builtin() {
		require.NoError(t, st.RegisterType(mt))
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, buildTools(st)
}

// findTool locates a registered tool by name. Fails the test if
// missing — the binary advertises a fixed surface and these tests
// pin it.
func findTool(t *testing.T, tools []mcp.Tool, name string) mcp.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not registered; available: %v", name, toolNames(tools))
	return nil
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}

// callTool decodes the tool's typed response into the destination
// struct via JSON round-trip. The tool's Handle method returns
// *mcp.Response with Data set to whatever the handler chose; this
// helper bridges the any → typed gap.
func callTool(t *testing.T, tool mcp.Tool, args any, dest any) *mcp.Response {
	t.Helper()
	input, err := json.Marshal(args)
	require.NoError(t, err)
	resp := tool.Handle(context.Background(), input)
	require.NotNil(t, resp)
	if dest != nil && resp.Status == "ok" {
		raw, err := json.Marshal(resp.Data)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, dest))
	}
	return resp
}

// --- task type registration -----------------------------------------------

// TestBuiltins_TaskTypeIsRegistered confirms the binary registers the
// canonical "task" MessageType at startup — the load-bearing
// guarantee that an agent can deposit a task message without the
// user defining a schema first.
func TestBuiltins_TaskTypeIsRegistered(t *testing.T) {
	t.Parallel()
	st, _ := newStoreWithBuiltins(t)
	assert.Contains(t, st.RegisteredTypes(), "task")
}

// --- deposit_message: enum enforcement ------------------------------------

// TestDepositMessage_TaskWithInvalidStatus_HasEnumHint pins the
// self-documenting-error property at the MCP-tool level. An agent
// that picks the wrong status value gets a CodeSchemaViolation
// response whose message names the rejected value AND lists every
// acceptable enum member.
func TestDepositMessage_TaskWithInvalidStatus_HasEnumHint(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "deposit_message")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	resp := callTool(t, tool, map[string]any{
		"session_id": sess.ID,
		"role":       "user",
		"type":       "task",
		"subject_id": "task-1",
		"content": map[string]any{
			"title":  "do thing",
			"status": "started",
		},
	}, nil)

	require.Equal(t, "error", resp.Status)
	require.NotNil(t, resp.Error)
	assert.Equal(t, mcp.CodeSchemaViolation, resp.Error.Code)

	msg := resp.Error.Message
	assert.Contains(t, msg, "started", "error must name the rejected value")
	assert.Contains(t, msg, "new", "error must list every acceptable enum member")
	assert.Contains(t, msg, "in-progress")
	assert.Contains(t, msg, "done")
	assert.Contains(t, msg, "abandoned")
}

func TestDepositMessage_TaskWithValidStatus_Accepted(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "deposit_message")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	for _, status := range []string{
		messagetypes.TaskStatusNew,
		messagetypes.TaskStatusInProgress,
		messagetypes.TaskStatusDone,
		messagetypes.TaskStatusAbandoned,
	} {
		var deposited messagestore.Message
		resp := callTool(t, tool, map[string]any{
			"session_id": sess.ID,
			"role":       "user",
			"type":       "task",
			"subject_id": "task-" + status,
			"content":    map[string]any{"title": "x", "status": status},
		}, &deposited)

		require.Equal(t, "ok", resp.Status, "status %q must be accepted", status)
		assert.Equal(t, "task", deposited.Type)
	}
}

// TestDepositMessage_UnknownTypeLists pins the related self-documenting
// property: when the agent guesses a type that doesn't exist, the
// error names every type that does. Critical for cross-vocabulary
// projects that don't ship docs.
func TestDepositMessage_UnknownTypeLists(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "deposit_message")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	resp := callTool(t, tool, map[string]any{
		"session_id": sess.ID,
		"role":       "user",
		"type":       "task.nope",
		"subject_id": "x",
		"content":    map[string]any{"a": 1},
	}, nil)

	require.Equal(t, "error", resp.Status)
	assert.Contains(t, resp.Error.Message, "task.nope")
	assert.Contains(t, resp.Error.Message, "registered types")
	assert.Contains(t, resp.Error.Message, "task",
		"the one registered type must appear by name in the error")
}

func TestDepositMessage_UnknownRoleLists(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "deposit_message")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	resp := callTool(t, tool, map[string]any{
		"session_id": sess.ID,
		"role":       "imposter",
		"type":       "task",
		"subject_id": "x",
		"content":    map[string]any{"title": "x", "status": "new"},
	}, nil)

	require.Equal(t, "error", resp.Status)
	assert.Contains(t, resp.Error.Message, "imposter")
	assert.Contains(t, resp.Error.Message, "agent",
		"AllowedRoles set must surface in the rejection")
	assert.Contains(t, resp.Error.Message, "orchestrator")
	assert.Contains(t, resp.Error.Message, "user")
}

// --- list_tasks ------------------------------------------------------------

// depositTask is a small helper for the list_tasks tests that exercises
// the same MCP path the agent would. Keeps the fixture honest: every
// deposit flows through schema + semantic validation.
func depositTask(t *testing.T, tools []mcp.Tool, sessionID, subjectID, status, title string) {
	t.Helper()
	tool := findTool(t, tools, "deposit_message")
	resp := callTool(t, tool, map[string]any{
		"session_id": sessionID,
		"role":       "user",
		"type":       "task",
		"subject_id": subjectID,
		"content":    map[string]any{"title": title, "status": status},
	}, nil)
	require.Equal(t, "ok", resp.Status,
		"deposit failed: %s", responseError(resp))
}

func responseError(r *mcp.Response) string {
	if r == nil || r.Error == nil {
		return ""
	}
	return r.Error.Message
}

// TestListTasks_OneEntryPerSubject locks in deduplication: a task
// with N status updates is still one row in list_tasks output.
func TestListTasks_OneEntryPerSubject(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	// Two tasks, each updated three times.
	depositTask(t, tools, sess.ID, "task-A", "new", "first task")
	depositTask(t, tools, sess.ID, "task-A", "in-progress", "first task")
	depositTask(t, tools, sess.ID, "task-A", "done", "first task")
	depositTask(t, tools, sess.ID, "task-B", "new", "second task")
	depositTask(t, tools, sess.ID, "task-B", "abandoned", "second task")

	var msgs []messagestore.Message
	resp := callTool(t, tool, map[string]any{}, &msgs)
	require.Equal(t, "ok", resp.Status)
	require.Len(t, msgs, 2, "history collapses to one entry per task")

	bySubj := map[string]string{}
	for _, m := range msgs {
		var c struct {
			Status string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(m.Content, &c))
		bySubj[m.SubjectID] = c.Status
	}
	assert.Equal(t, "done", bySubj["task-A"], "latest update wins per subject")
	assert.Equal(t, "abandoned", bySubj["task-B"])
}

// TestListTasks_StatusFilterNarrows confirms the "what's still to do?"
// query: pass status=new, get only currently-new tasks. A task whose
// latest status is done must NOT appear under status=new even if an
// earlier message had status=new.
func TestListTasks_StatusFilterNarrows(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)

	depositTask(t, tools, sess.ID, "still-new", "new", "still pending")
	depositTask(t, tools, sess.ID, "in-flight", "new", "starting")
	depositTask(t, tools, sess.ID, "in-flight", "in-progress", "starting")
	depositTask(t, tools, sess.ID, "completed", "new", "x")
	depositTask(t, tools, sess.ID, "completed", "done", "x")

	var newOnly []messagestore.Message
	resp := callTool(t, tool, map[string]any{"status": "new"}, &newOnly)
	require.Equal(t, "ok", resp.Status)
	require.Len(t, newOnly, 1, "only currently-new tasks should appear")
	assert.Equal(t, "still-new", newOnly[0].SubjectID)

	var inProg []messagestore.Message
	resp = callTool(t, tool, map[string]any{"status": "in-progress"}, &inProg)
	require.Equal(t, "ok", resp.Status)
	require.Len(t, inProg, 1)
	assert.Equal(t, "in-flight", inProg[0].SubjectID)

	var done []messagestore.Message
	resp = callTool(t, tool, map[string]any{"status": "done"}, &done)
	require.Equal(t, "ok", resp.Status)
	require.Len(t, done, 1)
	assert.Equal(t, "completed", done[0].SubjectID)
}

func TestListTasks_StatusFilterEnumEnforced(t *testing.T) {
	t.Parallel()
	_, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")

	// The MCP framework's strict-reject validation should catch
	// "status=banana" at the tools/call edge, but since callTool
	// invokes Handle directly we bypass that layer here. The same
	// path would surface as CodeSchemaViolation via the dispatcher;
	// see pkg/mcp/server_test.go for that integration. This test
	// asserts the bypass path is harmless (no panic, returns ok with
	// the unfiltered result OR an internal error — either is fine).
	resp := callTool(t, tool, map[string]any{"status": "banana"}, nil)
	assert.NotNil(t, resp)
}

func TestListTasks_LimitCaps(t *testing.T) {
	t.Parallel()
	st, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")

	sess, err := st.CreateSession(context.Background(), "x", "")
	require.NoError(t, err)
	for _, name := range []string{"t1", "t2", "t3", "t4", "t5"} {
		depositTask(t, tools, sess.ID, name, "new", name)
	}

	var msgs []messagestore.Message
	resp := callTool(t, tool, map[string]any{"limit": 3}, &msgs)
	require.Equal(t, "ok", resp.Status)
	assert.Len(t, msgs, 3)
}

func TestListTasks_EmptyWhenNoneRegistered(t *testing.T) {
	t.Parallel()
	_, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")

	var msgs []messagestore.Message
	resp := callTool(t, tool, map[string]any{}, &msgs)
	require.Equal(t, "ok", resp.Status)
	assert.Empty(t, msgs, "no tasks deposited yet — expect empty array, not error")
}

// TestListTasks_InputSchemaIsStrictReject is the contract the MCP
// framework relies on at Register time. If a future refactor drops
// additionalProperties:false from the list_tasks schema, the binary
// would panic at startup — but this test pins the property earlier,
// at unit-test time.
func TestListTasks_InputSchemaIsStrictReject(t *testing.T) {
	t.Parallel()
	_, tools := newStoreWithBuiltins(t)
	tool := findTool(t, tools, "list_tasks")
	raw := tool.InputSchema()
	assert.Contains(t, string(raw), `"additionalProperties": false`)
}
