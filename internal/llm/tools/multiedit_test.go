package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type fakeHistoryService struct {
	*pubsub.Broker[history.File]
	mu       sync.Mutex
	files    map[string]history.File
	versions map[string][]history.File
}

func newFakeHistoryService() *fakeHistoryService {
	return &fakeHistoryService{
		Broker:   pubsub.NewBroker[history.File](),
		files:    make(map[string]history.File),
		versions: make(map[string][]history.File),
	}
}

func (f *fakeHistoryService) Create(ctx context.Context, sessionID, path, content string) (history.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sessionID + "|" + path
	file := history.File{ID: key, SessionID: sessionID, Path: path, Content: content, Version: 0}
	f.files[key] = file
	f.versions[key] = append(f.versions[key], file)
	f.Publish(pubsub.CreatedEvent, file)
	return file, nil
}

func (f *fakeHistoryService) CreateVersion(ctx context.Context, sessionID, path, content string) (history.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sessionID + "|" + path
	var v int64
	if existing, ok := f.files[key]; ok {
		v = existing.Version + 1
	}
	file := history.File{ID: key, SessionID: sessionID, Path: path, Content: content, Version: v}
	f.files[key] = file
	f.versions[key] = append(f.versions[key], file)
	f.Publish(pubsub.UpdatedEvent, file)
	return file, nil
}

func (f *fakeHistoryService) Get(ctx context.Context, id string) (history.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range f.files {
		if file.ID == id {
			return file, nil
		}
	}
	return history.File{}, fmt.Errorf("not found")
}

func (f *fakeHistoryService) GetByPathAndSession(ctx context.Context, path, sessionID string) (history.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := sessionID + "|" + path
	if file, ok := f.files[key]; ok {
		return file, nil
	}
	return history.File{}, fmt.Errorf("not found")
}

func (f *fakeHistoryService) ListBySession(ctx context.Context, sessionID string) ([]history.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []history.File{}
	for _, file := range f.files {
		if file.SessionID == sessionID {
			out = append(out, file)
		}
	}
	return out, nil
}

func (f *fakeHistoryService) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]history.File, error) {
	return f.ListBySession(ctx, sessionID)
}

func (f *fakeHistoryService) Delete(ctx context.Context, id string) error { return nil }
func (f *fakeHistoryService) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	return nil
}

func testTool(t *testing.T, wd string) BaseTool {
	perm := permission.NewPermissionService(wd, true, []string{})
	files := newFakeHistoryService()
	return NewMultiEditTool(nil, perm, files, wd)
}

func withSession(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, SessionIDContextKey, "test-session")
	ctx = context.WithValue(ctx, MessageIDContextKey, "test-message")
	return ctx
}

func TestMultiEdit_SuccessSequential(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "file.txt")
	original := "x=1\ny=2\n"
	require.NoError(t, os.WriteFile(file, []byte(original), 0o644))
	recordFileRead(file)

	edits := []MultiEditOperation{
		{OldString: "x=1", NewString: "x=3"},
		{OldString: "y=2", NewString: "y=4"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "1", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.False(t, resp.IsError)

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "x=3\ny=4\n", string(data))

	var meta MultiEditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, len(edits), meta.EditsApplied)
}

func TestMultiEdit_AtomicFailure_IdenticalStrings(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "file.txt")
	original := "hello world"
	require.NoError(t, os.WriteFile(file, []byte(original), 0o644))
	recordFileRead(file)

	edits := []MultiEditOperation{
		{OldString: "hello", NewString: "hello"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "2", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "identical")
	require.Contains(t, resp.Content, "None of the edits were applied.")

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, original, string(data))
}

func TestMultiEdit_AtomicFailure_EmptyOldStringInNonFirst(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "file.txt")
	original := "a b c"
	require.NoError(t, os.WriteFile(file, []byte(original), 0o644))
	recordFileRead(file)

	edits := []MultiEditOperation{
		{OldString: "a", NewString: "A"},
		{OldString: "", NewString: "should-fail"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "3", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "only the first edit can have empty old_string")
	require.Contains(t, resp.Content, "None of the edits were applied.")

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, original, string(data))
}

func TestMultiEdit_AtomicFailure_MidEditError_MultipleMatches(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "file.txt")
	original := "foo bar foo"
	require.NoError(t, os.WriteFile(file, []byte(original), 0o644))
	recordFileRead(file)

	edits := []MultiEditOperation{
		{OldString: "bar", NewString: "BAR"},
		{OldString: "foo", NewString: "FOO"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "4", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "appears multiple times")
	require.Contains(t, resp.Content, "None of the edits were applied.")

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, original, string(data))
}

func TestMultiEdit_CreateFile_Success(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "new.txt")

	edits := []MultiEditOperation{
		{OldString: "", NewString: "line1\n"},
		{OldString: "line1", NewString: "LINE1"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "5", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.False(t, resp.IsError)

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "LINE1\n", string(data))

	var meta MultiEditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, len(edits), meta.EditsApplied)
}

func TestMultiEdit_CreateFile_Fail_FirstEditMustHaveEmptyOldString(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	file := filepath.Join(wd, "new.txt")

	edits := []MultiEditOperation{
		{OldString: "not-empty", NewString: "content"},
	}
	params := MultiEditParams{FilePath: file, Edits: edits}
	b, _ := json.Marshal(params)
	call := ToolCall{ID: "6", Name: MultiEditToolName, Input: string(b)}
	resp, err := testTool(t, wd).Run(withSession(context.Background()), call)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "first edit must have empty old_string")
	require.Contains(t, resp.Content, "None of the edits were applied.")

	_, statErr := os.Stat(file)
	require.True(t, os.IsNotExist(statErr))
}
