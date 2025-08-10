package e2e

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/llm/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/stretchr/testify/require"
)

func setupTestServices(t *testing.T, baseURL string) (agent.Service, session.Service, message.Service, func()) {
	permissions := permission.NewPermissionService("defaultId", true, nil)
	dbConn, err := db.Connect(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)

	queries := db.New(dbConn)
	sessions := session.NewService(queries)
	messages := message.NewService(queries)

	agentSvc, err := agent.NewAgent(
		context.Background(),
		config.Agent{},
		permissions,
		sessions,
		messages,
		history.NewService(queries, dbConn),
		map[string]*lsp.Client{},
	)
	require.NoError(t, err)
	return agentSvc, sessions, messages, func(){}
}

func TestAgentResponsesScenarioBasic_Mock(t *testing.T) {
	mock := &mockResponsesServer{}
	ts := httptest.NewServer(mock)
	defer ts.Close()

	agentSvc, sessions, messages, cleanup := setupTestServices(t, ts.URL + "/v1");
	defer cleanup()

	ctx := context.Background()
	sess, err := sessions.Create(ctx, "e2e")
	require.NoError(t, err)

	// Test implementation...
}

func TestAgentResponsesScenarioBasic_Live(t *testing.T) {
	if os.Getenv("E2E_LIVE") == "" || os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live test disabled")
	}

	agentSvc, sessions, messages, cleanup := setupTestServices(t, "https://api.openai.com/v1");
	defer cleanup()

	ctx := context.Background()
	sess, err := sessions.Create(ctx, "live-e2e")
	require.NoError(t, err)

	// Live test implementation...
}