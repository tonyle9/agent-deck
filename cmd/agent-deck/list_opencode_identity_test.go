package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestBuildListJSONOpenCodeIdentity(t *testing.T) {
	inst := &session.Instance{ID: "fixture", Tool: "opencode", OpenCodeSessionID: "ses_OWN"}
	data, err := buildListJSON("_test", []*session.Instance{inst}, map[*session.Instance]bool{inst: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["opencode_session_id"] != "ses_OWN" {
		t.Fatalf("missing OpenCode identity: %s", data)
	}
}

func TestListJSONOpenCodeIdentityCLI(t *testing.T) {
	home := t.TempDir()
	writeMacappConfig(t, home, "")
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "opencode-list", "-c", "opencode", "--no-parent", "--json", home)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add JSON: %v %s", err, stdout)
	}
	stdout, stderr, code = runAgentDeck(t, home, "session", "set", added.ID, "opencode-session-id", "ses_OWN")
	if code != 0 {
		t.Fatalf("set: %d %s %s", code, stdout, stderr)
	}
	for _, args := range [][]string{{"list", "--json"}, {"list", "--all", "--json"}} {
		stdout, stderr, code = runAgentDeck(t, home, args...)
		if code != 0 {
			t.Fatalf("%v: %d %s %s", args, code, stdout, stderr)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0]["opencode_session_id"] != "ses_OWN" {
			t.Fatalf("%v lost OpenCode identity: %s", args, stdout)
		}
	}
}
