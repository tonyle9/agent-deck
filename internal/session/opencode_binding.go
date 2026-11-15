package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Reserve IDs across the managed sessions on this socket. An OpenCode ID names
// one conversation even if a peer's working directory has since changed.
func openCodePeerIDs(sess *tmux.Session) (map[string]bool, error) {
	excluded := make(map[string]bool)
	if sess == nil {
		return excluded, nil
	}
	claims, err := tmux.ListAgentDeckOpenCodeSessionIDsOnSocket(sess.SocketName)
	if err != nil {
		return nil, err
	}
	for name, id := range claims {
		if name != sess.TmuxName() {
			excluded[id] = true
		}
	}
	return excluded, nil
}

// Recheck claims under a process + file lock and hold it through tmux publication.
// Selection alone cannot exclude a peer that is discovering concurrently. The
// lock is shared across profiles because they can share a tmux server. No
// instance mutex is held while acquiring it or querying tmux.
func lockOpenCodeSessionCandidate(sess *tmux.Session, candidate string) (func(), error) {
	if sess == nil {
		return func() {}, nil
	}
	dir, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return nil, err
	}
	lock, err := AcquireConfigFileLockTimeout(filepath.Join(dir, "opencode-bindings"), time.Second)
	if err != nil {
		return nil, err
	}
	excluded, err := openCodePeerIDs(sess)
	if err != nil || excluded[candidate] {
		lock.Release()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("OpenCode session %q is already claimed by another instance", candidate)
	}
	return lock.Release, nil
}

// A pane title is a fallback identity signal, not an exact session ID. Accept
// only a unique root-session match across the complete directory listing,
// including claimed sessions: removing peers first could hide duplicate titles.
func openCodeSessionByTitle(sessions []openCodeSessionMetadata, projectPath, paneTitle string) string {
	title := strings.TrimSpace(strings.TrimPrefix(paneTitle, "OC | "))
	if title == "" {
		return ""
	}
	prefix := title
	truncated := strings.HasSuffix(title, "…") || strings.HasSuffix(title, "_")
	if truncated {
		if strings.HasSuffix(title, "…") {
			prefix = strings.TrimSuffix(title, "…")
		} else {
			prefix = strings.TrimSuffix(title, "_")
		}
		if prefix == "" {
			return ""
		}
	}
	projectPath = normalizePath(projectPath)
	match := ""
	for _, sess := range sessions {
		dir := sess.Directory
		if dir == "" {
			dir = sess.Path
		}
		if dir == "" || normalizePath(dir) != projectPath {
			continue
		}
		if sess.Title != title && !(truncated && strings.HasPrefix(sess.Title, prefix)) {
			continue
		}
		if match != "" && match != sess.ID {
			return ""
		}
		match = sess.ID
	}
	return match
}
