package messagetypes

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
)

// newStoreWithBuiltins opens a temp messagestore with every Builtin
// MessageType registered — the exact wiring a toolbox binary uses.
func newStoreWithBuiltins(t *testing.T) *messagestore.Store {
	t.Helper()
	st, err := messagestore.Open(context.Background(), messagestore.Config{
		DBPath:       filepath.Join(t.TempDir(), "task-test.db"),
		AllowedRoles: []string{"user", "agent", "orchestrator"},
	})
	require.NoError(t, err)
	for _, mt := range Builtin() {
		require.NoError(t, st.RegisterType(mt))
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestTask_Registered(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	assert.Contains(t, st.RegisteredTypes(), "task")
}

func TestTask_AcceptsAllStatusValues(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, "x", "")
	require.NoError(t, err)

	for _, status := range []string{TaskStatusNew, TaskStatusInProgress, TaskStatusDone, TaskStatusAbandoned} {
		body, _ := json.Marshal(map[string]any{
			"title":  "test task",
			"status": status,
		})
		_, err := st.DepositMessage(ctx, &messagestore.Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task",
			SubjectID: "task-" + status,
			Content:   body,
		})
		assert.NoError(t, err, "status %q should be accepted", status)
	}
}

func TestTask_RejectsUnknownStatus(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, "x", "")
	require.NoError(t, err)

	_, err = st.DepositMessage(ctx, &messagestore.Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task",
		SubjectID: "task-1",
		Content:   json.RawMessage(`{"title":"x","status":"started"}`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, messagestore.ErrSchemaViolation,
		"the schema validator catches enum violations first (stage 1)")
	// The error message must name the rejected value AND list the
	// acceptable enum members — the load-bearing self-documenting
	// property.
	assert.Contains(t, err.Error(), "started")
	assert.Contains(t, err.Error(), "new")
	assert.Contains(t, err.Error(), "abandoned")
}

func TestTask_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, "x", "")
	require.NoError(t, err)

	_, err = st.DepositMessage(ctx, &messagestore.Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task",
		SubjectID: "task-1",
		Content:   json.RawMessage(`{"title":"x","status":"new","bogus":1}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestTask_RequiresTitleAndStatus(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, "x", "")
	require.NoError(t, err)

	cases := []struct {
		name    string
		content string
		field   string
	}{
		{"missing title", `{"status":"new"}`, "title"},
		{"missing status", `{"title":"x"}`, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := st.DepositMessage(ctx, &messagestore.Message{
				SessionID: sess.ID,
				Role:      "agent",
				Type:      "task",
				SubjectID: "task-X",
				Content:   json.RawMessage(tc.content),
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

// TestTask_HistoryViaGetMessages confirms the "task as message
// stream" design: every status update lives as its own row, and the
// full audit trail is recoverable via GetMessages by subject_id.
func TestTask_HistoryViaGetMessages(t *testing.T) {
	t.Parallel()
	st := newStoreWithBuiltins(t)
	ctx := context.Background()
	sess, err := st.CreateSession(ctx, "x", "")
	require.NoError(t, err)

	for _, update := range []struct{ status, notes string }{
		{TaskStatusNew, "initial"},
		{TaskStatusInProgress, "started work"},
		{TaskStatusDone, "shipped"},
	} {
		body, _ := json.Marshal(map[string]any{
			"title":  "ship the thing",
			"status": update.status,
			"notes":  update.notes,
		})
		_, err := st.DepositMessage(ctx, &messagestore.Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task",
			SubjectID: "ship-the-thing",
			Content:   body,
		})
		require.NoError(t, err)
	}

	history, err := st.GetMessages(ctx, messagestore.MessageFilter{
		SubjectID: "ship-the-thing",
		Type:      "task",
	})
	require.NoError(t, err)
	require.Len(t, history, 3, "every status transition preserved")

	// Current state = the latest message.
	latest, err := st.GetLatestMessage(ctx, messagestore.MessageFilter{
		SubjectID: "ship-the-thing",
		Type:      "task",
	})
	require.NoError(t, err)
	assert.Contains(t, string(latest.Content), `"status":"done"`)
}
