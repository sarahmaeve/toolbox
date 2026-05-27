package messagestore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/schema"
)

// taskCompletedSchema is the fixture type used across deposit tests.
// Strict-reject; one required string field; one optional integer.
const taskCompletedSchemaJSON = `{
	"type": "object",
	"properties": {
		"task_id":  {"type": "string"},
		"duration": {"type": "integer", "minimum": 0}
	},
	"required": ["task_id"],
	"additionalProperties": false
}`

func mustSchema(t *testing.T, raw string) *schema.Schema {
	t.Helper()
	s, err := schema.Parse(json.RawMessage(raw))
	require.NoError(t, err)
	return s
}

// newTestStore opens a store with a temp DB and a registered
// "task.completed" type.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{
		DBPath:            dbPath,
		AllowedRoles:      []string{"agent", "orchestrator"},
		MaxActiveSessions: 100,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.RegisterType(MessageType{
		Name:   "task.completed",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	}))
	return st
}

// --- Open / migration ------------------------------------------------------

func TestOpen_CreatesDatabaseAndMigrates(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{DBPath: dbPath})
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	// Reopen — migration should be a no-op.
	require.NoError(t, st.Close())
	st2, err := Open(context.Background(), Config{DBPath: dbPath})
	require.NoError(t, err)
	defer st2.Close() //nolint:errcheck
}

func TestOpen_RequiresDBPath(t *testing.T) {
	t.Parallel()
	_, err := Open(context.Background(), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DBPath")
}

// --- RegisterType ----------------------------------------------------------

func TestRegisterType_RejectsPermissiveSchema(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{DBPath: dbPath})
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	permissive := mustSchema(t, `{"type":"object","properties":{"x":{"type":"string"}}}`)
	err = st.RegisterType(MessageType{Name: "foo", Schema: permissive})
	assert.ErrorIs(t, err, ErrSchemaNotStrict)
}

func TestRegisterType_RejectsDuplicate(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	err := st.RegisterType(MessageType{
		Name:   "task.completed",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	})
	assert.ErrorIs(t, err, ErrTypeAlreadyRegistered)
}

func TestRegisterType_RequiresName(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	err := st.RegisterType(MessageType{
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Name")
}

func TestRegisterType_RequiresSchema(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	err := st.RegisterType(MessageType{Name: "other"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Schema")
}

// --- Sessions --------------------------------------------------------------

func TestCreateSession_AssignsIDAndCreatedAt(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	sess, err := st.CreateSession(context.Background(), "task-42", `{"owner":"sarah"}`)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
	assert.Equal(t, "task-42", sess.Target)
	assert.Equal(t, "active", sess.Status)
	assert.False(t, sess.CreatedAt.IsZero())
	assert.Equal(t, `{"owner":"sarah"}`, sess.Metadata)
}

func TestGetSession_ReturnsErrSessionNotFound(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	_, err := st.GetSession(context.Background(), "no-such-id")
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestListSessions_OrdersNewestFirst(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	s1, _ := st.CreateSession(context.Background(), "a", "")
	s2, _ := st.CreateSession(context.Background(), "b", "")

	list, err := st.ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Most recent first; s2 was created second.
	assert.Equal(t, s2.ID, list[0].ID)
	assert.Equal(t, s1.ID, list[1].ID)
}

func TestCreateSession_RespectsMaxActiveSessions(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{
		DBPath:            dbPath,
		MaxActiveSessions: 2,
	})
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	_, err = st.CreateSession(context.Background(), "a", "")
	require.NoError(t, err)
	_, err = st.CreateSession(context.Background(), "b", "")
	require.NoError(t, err)
	_, err = st.CreateSession(context.Background(), "c", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

func TestDeleteSession_CascadesMessages(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	sess, _ := st.CreateSession(ctx, "x", "")
	_, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	require.NoError(t, err)

	require.NoError(t, st.DeleteSession(ctx, sess.ID))

	msgs, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID})
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

// --- DepositMessage --------------------------------------------------------

func TestDeposit_HappyPath(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	msg, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1","duration":42}`),
	})
	require.NoError(t, err)
	assert.NotZero(t, msg.ID)
	assert.False(t, msg.CreatedAt.IsZero())
}

func TestDeposit_RejectsUnknownRole(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	_, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "imposter",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	assert.ErrorIs(t, err, ErrUnknownRole)
}

func TestDeposit_AcceptsAnyRoleWhenAllowListEmpty(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{DBPath: dbPath})
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	require.NoError(t, st.RegisterType(MessageType{
		Name:   "task.completed",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	}))

	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")
	_, err = st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "anything-goes",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	require.NoError(t, err, "empty AllowedRoles means accept any role")
}

func TestDeposit_RejectsUnknownType(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	_, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "no.such.type",
		Content:   json.RawMessage(`{}`),
	})
	assert.ErrorIs(t, err, ErrUnknownType)
}

func TestDeposit_RejectsSchemaViolation_UnknownField(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	_, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1","bogus":true}`),
	})
	assert.ErrorIs(t, err, ErrSchemaViolation)
	assert.Contains(t, err.Error(), "bogus")
}

func TestDeposit_RejectsSchemaViolation_MissingRequired(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	_, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"duration":5}`),
	})
	assert.ErrorIs(t, err, ErrSchemaViolation)
	assert.Contains(t, err.Error(), "task_id")
}

func TestDeposit_RunsOnIngestHook(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), Config{DBPath: dbPath})
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck

	semanticErr := errors.New("task_id must start with 't-'")
	require.NoError(t, st.RegisterType(MessageType{
		Name:   "task.completed",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
		OnIngest: func(content json.RawMessage) error {
			var p struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(content, &p); err != nil {
				return err
			}
			if len(p.TaskID) < 2 || p.TaskID[:2] != "t-" {
				return semanticErr
			}
			return nil
		},
	}))

	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	// Schema-valid but semantically wrong.
	_, err = st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"banana"}`),
	})
	assert.ErrorIs(t, err, ErrSemanticViolation)
	assert.ErrorIs(t, err, semanticErr)

	// Schema-valid AND semantically valid.
	_, err = st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t-1"}`),
	})
	require.NoError(t, err)
}

func TestDeposit_RejectsUnknownSession(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.DepositMessage(ctx, &Message{
		SessionID: "no-such-session",
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

// --- GetMessages / GetLatestMessage ----------------------------------------

func TestGetMessages_FiltersByRoleAndType(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	require.NoError(t, st.RegisterType(MessageType{
		Name:   "task.failed",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	}))

	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	for _, tc := range []struct{ role, typ string }{
		{"agent", "task.completed"},
		{"agent", "task.failed"},
		{"orchestrator", "task.completed"},
	} {
		_, err := st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      tc.role,
			Type:      tc.typ,
			Content:   json.RawMessage(`{"task_id":"t1"}`),
		})
		require.NoError(t, err)
	}

	all, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID})
	require.NoError(t, err)
	assert.Len(t, all, 3)

	agentOnly, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, Role: "agent"})
	require.NoError(t, err)
	assert.Len(t, agentOnly, 2)

	completed, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, Type: "task.completed"})
	require.NoError(t, err)
	assert.Len(t, completed, 2)

	specific, err := st.GetMessages(ctx, MessageFilter{
		SessionID: sess.ID, Role: "agent", Type: "task.completed",
	})
	require.NoError(t, err)
	assert.Len(t, specific, 1)
}

func TestGetLatestMessage_PicksMostRecent(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	for _, tid := range []string{"t1", "t2", "t3"} {
		_, err := st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			Content:   json.RawMessage(`{"task_id":"` + tid + `"}`),
		})
		require.NoError(t, err)
	}

	latest, err := st.GetLatestMessage(ctx, MessageFilter{SessionID: sess.ID})
	require.NoError(t, err)
	assert.Contains(t, string(latest.Content), "t3")
}

// --- sender_id / subject_id ------------------------------------------------

func TestDeposit_PersistsSenderAndSubject(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	deposited, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		SenderID:  "agent.reviewer.v2",
		Type:      "task.completed",
		SubjectID: "ticket-1234",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "agent.reviewer.v2", deposited.SenderID)
	assert.Equal(t, "ticket-1234", deposited.SubjectID)

	// Round-trip via read path.
	got, err := st.GetLatestMessage(ctx, MessageFilter{SessionID: sess.ID})
	require.NoError(t, err)
	assert.Equal(t, "agent.reviewer.v2", got.SenderID)
	assert.Equal(t, "ticket-1234", got.SubjectID)
}

func TestDeposit_OptionalSenderAndSubject(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	got, err := st.DepositMessage(ctx, &Message{
		SessionID: sess.ID,
		Role:      "agent",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t1"}`),
	})
	require.NoError(t, err)
	assert.Empty(t, got.SenderID, "unset sender_id round-trips as empty, not NULL bytes")
	assert.Empty(t, got.SubjectID)
}

func TestGetMessages_FiltersBySender(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	for _, sender := range []string{"agent.a.v1", "agent.b.v1", "agent.a.v1"} {
		_, err := st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			SenderID:  sender,
			Type:      "task.completed",
			Content:   json.RawMessage(`{"task_id":"t1"}`),
		})
		require.NoError(t, err)
	}

	got, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, SenderID: "agent.a.v1"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
	got, err = st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, SenderID: "agent.b.v1"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestGetMessages_FiltersBySubject(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	for _, subj := range []string{"ticket-1", "ticket-2", "ticket-1"} {
		_, err := st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: subj,
			Content:   json.RawMessage(`{"task_id":"t1"}`),
		})
		require.NoError(t, err)
	}

	got, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, SubjectID: "ticket-1"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// --- cross-session search --------------------------------------------------

func TestGetMessages_RejectsEmptyFilter(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	_, err := st.GetMessages(context.Background(), MessageFilter{})
	assert.ErrorIs(t, err, ErrFilterRequired)
}

func TestGetLatestMessage_RejectsEmptyFilter(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	_, err := st.GetLatestMessage(context.Background(), MessageFilter{})
	assert.ErrorIs(t, err, ErrFilterRequired)
}

func TestGetMessages_CrossSessionBySubject(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	// Deposit messages with the same SubjectID across three different
	// sessions — the memory-aid-for-digests use case.
	for i := range 3 {
		sess, err := st.CreateSession(ctx, "run-"+string(rune('a'+i)), "")
		require.NoError(t, err)
		_, err = st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: "ticket-1234",
			Content:   json.RawMessage(`{"task_id":"t1"}`),
		})
		require.NoError(t, err)
	}
	// One message at a different subject, in its own session — must NOT
	// match.
	otherSess, _ := st.CreateSession(ctx, "unrelated", "")
	_, err := st.DepositMessage(ctx, &Message{
		SessionID: otherSess.ID,
		Role:      "agent",
		Type:      "task.completed",
		SubjectID: "ticket-5678",
		Content:   json.RawMessage(`{"task_id":"t2"}`),
	})
	require.NoError(t, err)

	got, err := st.GetMessages(ctx, MessageFilter{SubjectID: "ticket-1234"})
	require.NoError(t, err)
	assert.Len(t, got, 3, "all three ticket-1234 deposits should surface, regardless of session")
	for _, m := range got {
		assert.Equal(t, "ticket-1234", m.SubjectID)
	}
}

func TestGetMessages_CrossSessionBySender(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	// Same sender across three sessions; one different sender as noise.
	for i := range 3 {
		sess, err := st.CreateSession(ctx, "run-"+string(rune('a'+i)), "")
		require.NoError(t, err)
		_, err = st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			SenderID:  "agent.digester.v1",
			Type:      "task.completed",
			Content:   json.RawMessage(`{"task_id":"t1"}`),
		})
		require.NoError(t, err)
	}
	noiseSess, _ := st.CreateSession(ctx, "other", "")
	_, err := st.DepositMessage(ctx, &Message{
		SessionID: noiseSess.ID,
		Role:      "agent",
		SenderID:  "agent.other.v1",
		Type:      "task.completed",
		Content:   json.RawMessage(`{"task_id":"t2"}`),
	})
	require.NoError(t, err)

	got, err := st.GetMessages(ctx, MessageFilter{SenderID: "agent.digester.v1"})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

func TestGetLatestMessage_CrossSessionPicksMostRecent(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()

	// Deposit "first" then "last" with the same subject across two sessions.
	for _, label := range []string{"first", "second", "third"} {
		sess, err := st.CreateSession(ctx, label, "")
		require.NoError(t, err)
		_, err = st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: "ticket-X",
			Content:   json.RawMessage(`{"task_id":"` + label + `"}`),
		})
		require.NoError(t, err)
	}

	got, err := st.GetLatestMessage(ctx, MessageFilter{SubjectID: "ticket-X"})
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), "third",
		"latest across sessions should be the most-recent deposit by created_at")
}

func TestGetMessages_LimitCapsResults(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := context.Background()
	sess, _ := st.CreateSession(ctx, "x", "")

	for range 5 {
		_, err := st.DepositMessage(ctx, &Message{
			SessionID: sess.ID,
			Role:      "agent",
			Type:      "task.completed",
			Content:   json.RawMessage(`{"task_id":"t"}`),
		})
		require.NoError(t, err)
	}

	got, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)

	all, err := st.GetMessages(ctx, MessageFilter{SessionID: sess.ID})
	require.NoError(t, err)
	assert.Len(t, all, 5, "Limit:0 means unlimited")
}

func TestRegisteredTypes_Sorted(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	require.NoError(t, st.RegisterType(MessageType{
		Name:   "a.first",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	}))
	require.NoError(t, st.RegisterType(MessageType{
		Name:   "z.last",
		Schema: mustSchema(t, taskCompletedSchemaJSON),
	}))
	got := st.RegisteredTypes()
	require.Len(t, got, 3) // task.completed + a.first + z.last
	assert.Equal(t, []string{"a.first", "task.completed", "z.last"}, got)
}
