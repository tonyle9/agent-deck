package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

var openCodeFixtureSequence atomic.Int64

func TestOpenCodeClaimAlreadyDetectedStopsRetrying(t *testing.T) {
	project := t.TempDir()
	marker := filepath.Join(t.TempDir(), "queries")
	payload := fmt.Sprintf(`[{"id":"ses_BOUND","directory":%q,"created":1000,"updated":1000}]`, project)
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\nprintf '%%s' %q\n", marker, payload)
	setFakeOpenCodePath(t, script, false)
	configureFakeOpenCode(t)
	inst := &Instance{Tool: "opencode", ProjectPath: project, OpenCodeSessionID: "ses_BOUND"}
	inst.detectOpenCodeSessionAsync()
	data, err := os.ReadFile(marker)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "x"); calls > 1 {
		t.Fatalf("already-bound discovery queried %d times, want at most 1", calls)
	}
}

func openCodePeerFixtures(t *testing.T, count int) ([]*Instance, func(...string)) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	socket := fmt.Sprintf("oc-duplicate-%d", openCodeFixtureSequence.Add(1))
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	project := t.TempDir()
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	var instances []*Instance
	for n := 0; n < count; n++ {
		name := fmt.Sprintf("agentdeck_ocpeer_%d", n)
		run("new-session", "-d", "-s", name, "-c", project)
		instances = append(instances, &Instance{ID: name, Tool: "opencode", ProjectPath: project,
			tmuxSession: &tmux.Session{Name: name, SocketName: socket}})
	}
	return instances, run
}

func TestOpenCodeDuplicateBindingConcurrent(t *testing.T) {
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			instances, _ := openCodePeerFixtures(t, count)
			var wg sync.WaitGroup
			for _, inst := range instances {
				wg.Go(func() { inst.applyOpenCodeSessionCandidate("ses_SHARED") })
			}
			wg.Wait()
			owners := 0
			for _, inst := range instances {
				if inst.OpenCodeSessionID == "ses_SHARED" {
					owners++
				}
			}
			if owners != 1 {
				t.Fatalf("same-directory owners = %d, want 1", owners)
			}
		})
	}
}

func TestOpenCodeDuplicateBindingDiscovery(t *testing.T) {
	instances, run := openCodePeerFixtures(t, 3)
	project := instances[0].ProjectPath
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"id":"ses_OLD","title":"Own conversation","directory":%q,"time":{"created":1000,"updated":1000}},
		{"id":"ses_NEW","title":"Other conversation","directory":%q,"time":{"created":2000,"updated":3000}},
		{"id":"ses_THIRD","title":"Third conversation","directory":%q,"time":{"created":1500,"updated":2000}}]`, project, project, project)
	}))
	t.Cleanup(server.Close)
	for _, inst := range instances {
		inst.OpenCodePort = server.Listener.Addr().(*net.TCPAddr).Port
	}
	run("select-pane", "-t", instances[0].tmuxSession.TmuxName()+":0.0", "-T", "OC | Own conversation")
	started := time.Now()
	var selected string
	for range 10 {
		selected = instances[0].queryOpenCodeSession()
	}
	t.Logf("discovery with three same-directory instances: %s/query (10 queries)", time.Since(started)/10)
	if got := selected; got != "ses_OLD" {
		t.Fatalf("pane title selected %q, want ses_OLD", got)
	}
	instances[1].setOpenCodeSession("ses_NEW")
	if got := instances[2].queryOpenCodeSession(); got != "ses_THIRD" {
		t.Fatalf("unbound sibling selected %q, want ses_THIRD", got)
	}
	instances[2].setOpenCodeSession("ses_NEW")
	if instances[2].OpenCodeSessionID != "" {
		t.Fatal("async setter accepted a sibling's ID")
	}
}

func TestOpenCodeTitleResolution(t *testing.T) {
	for _, tc := range []struct {
		name, pane, first, second, want string
	}{
		{"exact", "OC | Own conversation", "Own conversation", "Other conversation", "ses_FIRST"},
		{"unicode truncation", "OC | Own…", "Own conversation", "Other conversation", "ses_FIRST"},
		{"literal underscore before truncation", "OC | Own_…", "Own_conversation", "Own other conversation", "ses_FIRST"},
		{"tmux truncation", "OC | Own_", "Own conversation", "Other conversation", "ses_FIRST"},
		{"ambiguous prefix", "OC | Own_", "Own conversation", "Own other conversation", ""},
		{"duplicate title", "OC | Own conversation", "Own conversation", "Own conversation", ""},
		{"missing title", "OC | Unknown", "Own conversation", "Other conversation", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instances, run := openCodePeerFixtures(t, 1)
			inst := instances[0]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `[{"id":"ses_FIRST","title":%q,"directory":%q,"time":{"created":1000,"updated":1000}},
				{"id":"ses_SECOND","title":%q,"directory":%q,"time":{"created":2000,"updated":2000}}]`, tc.first, inst.ProjectPath, tc.second, inst.ProjectPath)
			}))
			t.Cleanup(server.Close)
			inst.OpenCodePort = server.Listener.Addr().(*net.TCPAddr).Port
			run("select-pane", "-t", inst.tmuxSession.TmuxName()+":0.0", "-T", tc.pane)
			if got := inst.queryOpenCodeSession(); got != tc.want {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenCodeDuplicateBindingRepair(t *testing.T) {
	instances, run := openCodePeerFixtures(t, 2)
	for _, inst := range instances {
		inst.OpenCodeSessionID = "ses_SHARED"
		run("set-environment", "-t", inst.tmuxSession.TmuxName(), "OPENCODE_SESSION_ID", "ses_SHARED")
	}
	if instances[0].applyOpenCodeSessionCandidate("ses_SHARED") {
		t.Fatal("accepted a duplicate existing claim")
	}
	if !instances[0].OpenCodeDetectedAt.IsZero() {
		t.Fatal("refreshed confidence in a duplicate existing claim")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"id":"ses_SHARED","title":"Other conversation","directory":%q,"time":{"created":2000,"updated":3000}},
		{"id":"ses_DISTINCT","title":"Own conversation","directory":%q,"time":{"created":1000,"updated":1000}}]`, instances[0].ProjectPath, instances[0].ProjectPath)
	}))
	t.Cleanup(server.Close)
	instances[0].OpenCodePort = server.Listener.Addr().(*net.TCPAddr).Port
	run("select-pane", "-t", instances[0].tmuxSession.TmuxName()+":0.0", "-T", "OC | Own conversation")
	candidate := instances[0].queryOpenCodeSession()
	if candidate != "ses_DISTINCT" {
		t.Fatalf("duplicate repair selected %q", candidate)
	}
	if !instances[0].applyOpenCodeSessionCandidate(candidate) {
		t.Fatal("could not repair duplicate binding")
	}
	if got, err := instances[0].tmuxSession.ReadEnvironment("OPENCODE_SESSION_ID"); err != nil || got != "ses_DISTINCT" {
		t.Fatalf("published claim = %q, %v", got, err)
	}
}

func TestOpenCodeClaimIgnoresGlobalEnvironment(t *testing.T) {
	instances, run := openCodePeerFixtures(t, 2)
	run("set-environment", "-g", "OPENCODE_SESSION_ID", "ses_GLOBAL")
	if !instances[0].applyOpenCodeSessionCandidate("ses_GLOBAL") {
		t.Fatal("global environment was treated as a peer claim")
	}
	if instances[1].applyOpenCodeSessionCandidate("ses_GLOBAL") {
		t.Fatal("session-local claim was ignored")
	}
}

func TestOpenCodeClaimRequiresPublication(t *testing.T) {
	instances, run := openCodePeerFixtures(t, 2)
	inst := instances[0]
	run("kill-session", "-t", inst.tmuxSession.TmuxName())
	if inst.applyOpenCodeSessionCandidate("ses_UNPUBLISHED") {
		t.Fatal("accepted an unpublished claim")
	}
	if inst.OpenCodeSessionID != "" || !inst.OpenCodeDetectedAt.IsZero() {
		t.Fatal("unpublished claim changed in-memory identity")
	}
}

func TestOpenCodeDuplicateTitleOnLaterPage(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	instances, run := openCodePeerFixtures(t, 2)
	inst := instances[0]
	inst.OpenCodeStartedAt = fixtureSpawnedAt
	run("select-pane", "-t", inst.tmuxSession.TmuxName()+":0.0", "-T", "OC | Same title")
	page := func(id, next string) string {
		return fmt.Sprintf(`{"data":[{"id":%q,"title":"Same title","location":{"directory":%q},"time":{"created":1000,"updated":2000}}],"cursor":{"next":%q}}`, id, inst.ProjectPath, next)
	}
	systemPath := os.Getenv("PATH")
	dir := setFakeOpenCodeServicePages(t, page("ses_FIRST", "c1"), page("ses_SECOND", ""))
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+systemPath)
	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("ambiguous title selected %q", got)
	}
	if got := fakeOpenCodeCallCount(t, dir); got != 2 {
		t.Fatalf("queried %d pages, want 2", got)
	}
}

func TestOpenCodeTitleRequiresCompleteListing(t *testing.T) {
	for _, tc := range []struct{ name, next, want string }{
		{"unique match on later page", "", "ses_OWN"},
		{"page cap", "c1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinOpenCodeMajorVersion(t, 2, true)
			instances, run := openCodePeerFixtures(t, 1)
			inst := instances[0]
			run("select-pane", "-t", inst.tmuxSession.TmuxName()+":0.0", "-T", "OC | Own conversation")
			page := func(id, title, next string) string {
				return fmt.Sprintf(`{"data":[{"id":%q,"title":%q,"location":{"directory":%q},"time":{"created":1000,"updated":2000}}],"cursor":{"next":%q}}`, id, title, inst.ProjectPath, next)
			}
			systemPath := os.Getenv("PATH")
			dir := setFakeOpenCodeServicePages(t, page("ses_OTHER", "Other", "c1"), page("ses_OWN", "Own conversation", tc.next))
			t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+systemPath)
			if got := inst.queryOpenCodeSession(); got != tc.want {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}
			wantPages := 2
			if tc.next != "" {
				wantPages = openCodeServiceSessionMaxPages
			}
			if got := fakeOpenCodeCallCount(t, dir); got != wantPages {
				t.Fatalf("queried %d pages, want %d", got, wantPages)
			}
		})
	}
}
