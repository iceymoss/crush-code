package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// recordingHistoryService captures every call made to it so tests can assert
// on the order, count, and arguments of history writes. It models the real
// history.Service behavior on the only point that matters for the bug under
// test: GetByPathAndSession returns sql.ErrNoRows when no record exists.
type recordingHistoryService struct {
	*pubsub.Broker[history.File]

	mu            sync.Mutex
	versions      map[string][]string // key: sessionID|path -> ordered list of contents
	createCalls   int
	versionCalls  int
	lastCreateArg string
}

func newRecordingHistoryService() *recordingHistoryService {
	return &recordingHistoryService{
		versions: make(map[string][]string),
	}
}

func (m *recordingHistoryService) key(sessionID, path string) string {
	return sessionID + "|" + path
}

func (m *recordingHistoryService) Create(ctx context.Context, sessionID, path, content string) (history.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	m.lastCreateArg = content
	k := m.key(sessionID, path)
	m.versions[k] = append(m.versions[k], content)
	return history.File{SessionID: sessionID, Path: path, Content: content}, nil
}

func (m *recordingHistoryService) CreateVersion(ctx context.Context, sessionID, path, content string) (history.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versionCalls++
	k := m.key(sessionID, path)
	m.versions[k] = append(m.versions[k], content)
	return history.File{SessionID: sessionID, Path: path, Content: content}, nil
}

func (m *recordingHistoryService) GetByPathAndSession(ctx context.Context, path, sessionID string) (history.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(sessionID, path)
	versions, ok := m.versions[k]
	if !ok || len(versions) == 0 {
		// Match the real service which returns sql.ErrNoRows here.
		return history.File{}, sql.ErrNoRows
	}
	return history.File{SessionID: sessionID, Path: path, Content: versions[len(versions)-1]}, nil
}

func (m *recordingHistoryService) Get(ctx context.Context, id string) (history.File, error) {
	return history.File{}, errors.New("not implemented")
}

func (m *recordingHistoryService) ListBySession(ctx context.Context, sessionID string) ([]history.File, error) {
	return nil, nil
}

func (m *recordingHistoryService) ListLatestSessionFiles(ctx context.Context, sessionID string) ([]history.File, error) {
	return nil, nil
}

func (m *recordingHistoryService) Delete(ctx context.Context, id string) error { return nil }

func (m *recordingHistoryService) DeleteSessionFiles(ctx context.Context, sessionID string) error {
	return nil
}

// snapshot returns the recorded versions for a (session, path) plus call counts.
func (m *recordingHistoryService) snapshot(sessionID, path string) (versions []string, createCalls, versionCalls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(sessionID, path)
	out := make([]string, len(m.versions[k]))
	copy(out, m.versions[k])
	return out, m.createCalls, m.versionCalls
}

// TestWriteToolFirstEditDoesNotDuplicateVersion asserts that writing to an
// existing file for the first time in a session records exactly two versions
// in history (v0=oldContent, v1=newContent) and does NOT insert a spurious
// duplicate of oldContent between them.
func TestWriteToolFirstEditDoesNotDuplicateVersion(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	target := filepath.Join(workingDir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	hist := newRecordingHistoryService()
	tool := NewWriteTool(nil, &mockPermissionService{}, hist, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "hello.txt", Content: "new"})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: WriteToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "tool returned error: %s", resp.Content)

	versions, createCalls, versionCalls := hist.snapshot("s1", target)
	require.Equal(t, []string{"old", "new"}, versions,
		"expected history chain [old, new], got %v (createCalls=%d versionCalls=%d)",
		versions, createCalls, versionCalls)
	require.Equal(t, 1, createCalls, "Create should be called exactly once for the initial version")
	require.Equal(t, 1, versionCalls, "CreateVersion should be called exactly once for the new content")
}

// TestEditToolFirstEditDoesNotDuplicateVersion is the same regression check
// for the edit tool's replaceContent path.
func TestEditToolFirstEditDoesNotDuplicateVersion(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	target := filepath.Join(workingDir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	hist := newRecordingHistoryService()
	tool := NewEditTool(nil, &mockPermissionService{}, hist, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(EditParams{FilePath: "hello.txt", OldString: "old", NewString: "new"})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: EditToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "tool returned error: %s", resp.Content)

	versions, createCalls, versionCalls := hist.snapshot("s1", target)
	require.Equal(t, []string{"old", "new"}, versions,
		"expected history chain [old, new], got %v (createCalls=%d versionCalls=%d)",
		versions, createCalls, versionCalls)
}

// TestEditToolDeleteFirstEditDoesNotDuplicateVersion covers the edit tool's
// deleteContent path (NewString=="") which has the same buggy block.
func TestEditToolDeleteFirstEditDoesNotDuplicateVersion(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	target := filepath.Join(workingDir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("keep-DEL-keep"), 0o644))

	hist := newRecordingHistoryService()
	tool := NewEditTool(nil, &mockPermissionService{}, hist, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(EditParams{FilePath: "hello.txt", OldString: "-DEL-", NewString: ""})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: EditToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "tool returned error: %s", resp.Content)

	versions, _, _ := hist.snapshot("s1", target)
	require.Equal(t, []string{"keep-DEL-keep", "keepkeep"}, versions,
		"expected history chain [keep-DEL-keep, keepkeep], got %v", versions)
}

// TestMultiEditToolFirstEditDoesNotDuplicateVersion covers the multiedit
// tool's processMultiEditExistingFile path.
func TestMultiEditToolFirstEditDoesNotDuplicateVersion(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	target := filepath.Join(workingDir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("a\nb\n"), 0o644))

	hist := newRecordingHistoryService()
	tool := NewMultiEditTool(nil, &mockPermissionService{}, hist, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(MultiEditParams{
		FilePath: "hello.txt",
		Edits: []MultiEditOperation{
			{OldString: "a", NewString: "A"},
			{OldString: "b", NewString: "B"},
		},
	})
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: MultiEditToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "tool returned error: %s", resp.Content)

	versions, _, _ := hist.snapshot("s1", target)
	require.Equal(t, []string{"a\nb\n", "A\nB\n"}, versions,
		"expected history chain [a\\nb\\n, A\\nB\\n], got %v", versions)
}

// TestEditToolSecondEditPreservesIntermediateLogic verifies that after a
// session already has history, the "external modification" intermediate
// version logic still works. This guards against an over-zealous fix that
// would skip the intermediate version unconditionally.
func TestEditToolSecondEditPreservesIntermediateLogic(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	target := filepath.Join(workingDir, "hello.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	hist := newRecordingHistoryService()
	tool := NewEditTool(nil, &mockPermissionService{}, hist, mockFileTrackerService{}, workingDir)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")

	// First edit: old -> new
	input1, _ := json.Marshal(EditParams{FilePath: "hello.txt", OldString: "old", NewString: "new"})
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: EditToolName, Input: string(input1)})
	require.NoError(t, err)
	require.False(t, resp.IsError)

	// Simulate external modification of the file, then edit again.
	require.NoError(t, os.WriteFile(target, []byte("external"), 0o644))

	input2, _ := json.Marshal(EditParams{FilePath: "hello.txt", OldString: "external", NewString: "final"})
	resp, err = tool.Run(ctx, fantasy.ToolCall{ID: "c2", Name: EditToolName, Input: string(input2)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "tool returned error: %s", resp.Content)

	versions, _, _ := hist.snapshot("s1", target)
	require.Equal(t, []string{"old", "new", "external", "final"}, versions,
		"expected history chain to include intermediate external version, got %v", versions)
}
