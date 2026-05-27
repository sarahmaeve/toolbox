package bridge

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sarahmaeve/toolbox/pkg/messagestore"
	"github.com/sarahmaeve/toolbox/pkg/schema"
)

const taskSchemaJSON = `{
	"type": "object",
	"properties": {
		"task_id":  {"type": "string"},
		"duration": {"type": "integer", "minimum": 0}
	},
	"required": ["task_id"],
	"additionalProperties": false
}`

// fixture spins up a bridge backed by a real messagestore + a single
// registered MessageType. Returns a Client wired to the httptest URL
// and the underlying Store for direct inspection.
func fixture(t *testing.T) (*Client, *messagestore.Store, *httptest.Server) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	store, err := messagestore.Open(context.Background(), messagestore.Config{
		DBPath:            dbPath,
		AllowedRoles:      []string{"agent", "orchestrator"},
		MaxActiveSessions: 100,
	})
	require.NoError(t, err)

	sch, err := schema.Parse(json.RawMessage(taskSchemaJSON))
	require.NoError(t, err)
	require.NoError(t, store.RegisterType(messagestore.MessageType{
		Name:   "task.completed",
		Schema: sch,
	}))

	srv := NewServer(ServerConfig{
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	httpSrv := httptest.NewServer(srv.Handler())

	client, err := NewClient(ClientConfig{BaseURL: httpSrv.URL})
	require.NoError(t, err)

	t.Cleanup(func() {
		httpSrv.Close()
		_ = store.Close()
	})

	return client, store, httpSrv
}

// --- session lifecycle -----------------------------------------------------

func TestClient_CreateSession_RoundTrips(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)

	sess, err := c.CreateSession(context.Background(), "task-42", `{"k":"v"}`)
	require.NoError(t, err)
	assert.NotEmpty(t, sess.ID)
	assert.Equal(t, "task-42", sess.Target)
	assert.Equal(t, "active", sess.Status)
}

func TestClient_CreateSession_TargetRequired(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)

	_, err := c.CreateSession(context.Background(), "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target")
}

// --- deposit + validate ----------------------------------------------------

func TestClient_DepositMessage_HappyPath(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	msg, err := c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "agent",
		Type:    "task.completed",
		Content: json.RawMessage(`{"task_id":"t-1","duration":7}`),
	})
	require.NoError(t, err)
	assert.NotZero(t, msg.ID)
	assert.Equal(t, "task.completed", msg.Type)
}

func TestClient_DepositMessage_RoundTripsSenderAndSubject(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()
	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	msg, err := c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:      "agent",
		SenderID:  "agent.reviewer.v2",
		Type:      "task.completed",
		SubjectID: "ticket-1234",
		Content:   json.RawMessage(`{"task_id":"t-1"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "agent.reviewer.v2", msg.SenderID)
	assert.Equal(t, "ticket-1234", msg.SubjectID)
}

func TestClient_DepositMessage_SchemaViolationSurfacesAtClient(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "agent",
		Type:    "task.completed",
		Content: json.RawMessage(`{"task_id":"t-1","bogus":true}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus",
		"unknown-field violation must reach the caller intact for self-correction")
}

func TestClient_DepositMessage_UnknownTypeSurfaces(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "agent",
		Type:    "no.such.type",
		Content: json.RawMessage(`{}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no.such.type")
}

func TestClient_DepositMessage_UnknownRoleSurfaces(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "imposter",
		Type:    "task.completed",
		Content: json.RawMessage(`{"task_id":"t-1"}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "imposter")
}

func TestClient_DepositMessage_UnknownSessionSurfaces(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	_, err := c.DepositMessage(ctx, "no-such-session", DepositRequest{
		Role:    "agent",
		Type:    "task.completed",
		Content: json.RawMessage(`{"task_id":"t-1"}`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- latest message --------------------------------------------------------

func TestClient_GetLatestMessage_RoundTrips(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	for _, tid := range []string{"t-1", "t-2", "t-3"} {
		_, err := c.DepositMessage(ctx, sess.ID, DepositRequest{
			Role:    "agent",
			Type:    "task.completed",
			Content: json.RawMessage(`{"task_id":"` + tid + `"}`),
		})
		require.NoError(t, err)
	}

	latest, err := c.GetLatestMessage(ctx, sess.ID, MessageQuery{})
	require.NoError(t, err)
	assert.Contains(t, string(latest.Content), "t-3")
}

func TestClient_GetLatestMessage_FiltersByRoleAndType(t *testing.T) {
	t.Parallel()
	c, store, _ := fixture(t)
	ctx := context.Background()

	sch, err := schema.Parse(json.RawMessage(taskSchemaJSON))
	require.NoError(t, err)
	require.NoError(t, store.RegisterType(messagestore.MessageType{
		Name:   "task.failed",
		Schema: sch,
	}))

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "agent",
		Type:    "task.completed",
		Content: json.RawMessage(`{"task_id":"t-completed"}`),
	})
	require.NoError(t, err)
	_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
		Role:    "orchestrator",
		Type:    "task.failed",
		Content: json.RawMessage(`{"task_id":"t-failed"}`),
	})
	require.NoError(t, err)

	got, err := c.GetLatestMessage(ctx, sess.ID, MessageQuery{Type: "task.completed"})
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), "t-completed")

	got, err = c.GetLatestMessage(ctx, sess.ID, MessageQuery{Role: "orchestrator"})
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), "t-failed")
}

func TestClient_GetLatestMessage_FiltersBySender(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()
	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	for _, sender := range []string{"agent.a.v1", "agent.b.v1", "agent.a.v1"} {
		_, err := c.DepositMessage(ctx, sess.ID, DepositRequest{
			Role:     "agent",
			SenderID: sender,
			Type:     "task.completed",
			Content:  json.RawMessage(`{"task_id":"t"}`),
		})
		require.NoError(t, err)
	}

	got, err := c.GetLatestMessage(ctx, sess.ID, MessageQuery{SenderID: "agent.b.v1"})
	require.NoError(t, err)
	assert.Equal(t, "agent.b.v1", got.SenderID)
}

func TestClient_GetLatestMessage_NoMatchIs404(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	sess, err := c.CreateSession(ctx, "t", "")
	require.NoError(t, err)

	_, err = c.GetLatestMessage(ctx, sess.ID, MessageQuery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no matching message")
}

// --- cross-session search --------------------------------------------------

func TestClient_SearchMessages_BySubjectAcrossSessions(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	for _, target := range []string{"run-a", "run-b", "run-c"} {
		sess, err := c.CreateSession(ctx, target, "")
		require.NoError(t, err)
		_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: "ticket-1234",
			Content:   json.RawMessage(`{"task_id":"t"}`),
		})
		require.NoError(t, err)
	}

	got, err := c.SearchMessages(ctx, MessageQuery{SubjectID: "ticket-1234"})
	require.NoError(t, err)
	assert.Len(t, got, 3, "cross-session search must return all three deposits")
}

func TestClient_SearchMessages_RejectsEmptyFilter(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	_, err := c.SearchMessages(context.Background(), MessageQuery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one")
}

func TestClient_SearchLatestMessage_AcrossSessions(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	for _, label := range []string{"first", "second", "third"} {
		sess, err := c.CreateSession(ctx, label, "")
		require.NoError(t, err)
		_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: "ticket-X",
			Content:   json.RawMessage(`{"task_id":"` + label + `"}`),
		})
		require.NoError(t, err)
	}

	got, err := c.SearchLatestMessage(ctx, MessageQuery{SubjectID: "ticket-X"})
	require.NoError(t, err)
	assert.Contains(t, string(got.Content), "third")
}

func TestClient_SearchMessages_LimitCaps(t *testing.T) {
	t.Parallel()
	c, _, _ := fixture(t)
	ctx := context.Background()

	for range 5 {
		sess, err := c.CreateSession(ctx, "x", "")
		require.NoError(t, err)
		_, err = c.DepositMessage(ctx, sess.ID, DepositRequest{
			Role:      "agent",
			Type:      "task.completed",
			SubjectID: "ticket-Y",
			Content:   json.RawMessage(`{"task_id":"t"}`),
		})
		require.NoError(t, err)
	}

	got, err := c.SearchMessages(ctx, MessageQuery{SubjectID: "ticket-Y", Limit: 2})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// --- client construction ---------------------------------------------------

func TestNewClient_RequiresBaseURL(t *testing.T) {
	t.Parallel()
	_, err := NewClient(ClientConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BaseURL")
}

func TestNewClient_RejectsUnsupportedScheme(t *testing.T) {
	t.Parallel()
	_, err := NewClient(ClientConfig{BaseURL: "ftp://nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}
