package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTaskGroupsSessions: two worktrees created for one task share its id and
// come back together, which is what the dashboard groups on.
func TestTaskGroupsSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var id string
	_, err := Mutate(func(s *Store) bool {
		task := s.UpsertTask(Task{Repo: "norn", Goal: "split a task", Trunk: "feature/multi-model/CU-123"})
		id = task.ID
		s.UpsertByPath(Session{ID: MakeID("norn", "feature/multi-model/CU-123"), Repo: "norn",
			Branch: "feature/multi-model/CU-123", Path: "/w/trunk", TaskID: id, Role: "trunk"})
		s.UpsertByPath(Session{ID: MakeID("norn", "feature/multi-model/CU-123/logic"), Repo: "norn",
			Branch: "feature/multi-model/CU-123/logic", Path: "/w/logic", TaskID: id, Role: "logic"})
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("UpsertTask minted no id")
	}

	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	rows := s.SessionsForTask(id)
	if len(rows) != 2 {
		t.Fatalf("SessionsForTask = %d rows, want 2", len(rows))
	}
	if rows[0].Role != "trunk" || rows[1].Role != "logic" {
		t.Fatalf("roles not preserved: %q, %q", rows[0].Role, rows[1].Role)
	}
	if task := s.FindTaskByTrunk("norn", "feature/multi-model/CU-123"); task == nil || task.ID != id {
		t.Fatalf("FindTaskByTrunk missed the task: %+v", task)
	}
}

// TestUpsertPreservesTaskID: the activity tick and the worktree adopter both
// build a bare Session from what git knows. Neither knows about tasks, so an
// upsert that omits the fields must not orphan the row from its task.
func TestUpsertPreservesTaskID(t *testing.T) {
	s := &Store{}
	s.UpsertByPath(Session{ID: "norn:b", Repo: "norn", Branch: "b", Path: "/w/b", TaskID: "abc", Role: "logic"})
	s.UpsertByPath(Session{ID: "norn:b", Repo: "norn", Branch: "b", Path: "/w/b"})
	if got := s.Sessions[0]; got.TaskID != "abc" || got.Role != "logic" {
		t.Fatalf("bare upsert dropped task fields: task_id=%q role=%q", got.TaskID, got.Role)
	}

	s2 := &Store{}
	s2.Upsert(Session{ID: "norn:b", Repo: "norn", Branch: "b", Path: "/w/b", TaskID: "abc", Role: "logic"})
	s2.Upsert(Session{ID: "norn:b", Repo: "norn", Branch: "b", Path: "/w/b"})
	if got := s2.Sessions[0]; got.TaskID != "abc" || got.Role != "logic" {
		t.Fatalf("bare Upsert dropped task fields: task_id=%q role=%q", got.TaskID, got.Role)
	}
}

// TestLoadPreTaskStore: a sessions.json written before tasks existed loads with
// every row intact, and saving it back adds no empty task fields.
func TestLoadPreTaskStore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"sessions":[{"id":"norn:fix/a","repo":"norn","branch":"fix/a","kind":"task",` +
		`"path":"/w/a","title":"a title","clickup_id":"CU-1","pr_number":7,"status":"active",` +
		`"started_at":"2026-01-01T00:00:00Z","last_activity_at":"2026-01-02T00:00:00Z"}]}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(s.Sessions))
	}
	got := s.Sessions[0]
	if got.Title != "a title" || got.ClickUpID != "CU-1" || got.PRNumber != 7 || got.Kind != "task" {
		t.Fatalf("pre-task row lost fields: %+v", got)
	}
	if got.TaskID != "" || got.Role != "" {
		t.Fatalf("pre-task row invented task fields: %+v", got)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["tasks"]; ok {
		t.Fatalf("save added a tasks key to a task-free store:\n%s", data)
	}
}

// TestPruneTasks drops a task once its last worktree is gone, and clears a
// TaskID left pointing at nothing — a header over no rows is worse than none.
func TestPruneTasks(t *testing.T) {
	s := &Store{}
	task := s.UpsertTask(Task{ID: "abc", Repo: "norn", Trunk: "feature/x", CreatedAt: time.Now()})
	s.UpsertByPath(Session{ID: "norn:feature/x", Repo: "norn", Branch: "feature/x", Path: "/w/x", TaskID: task.ID})
	s.UpsertByPath(Session{ID: "norn:other", Repo: "norn", Branch: "other", Path: "/w/o", TaskID: "gone", Role: "logic"})

	if n := s.PruneTasks(); n != 1 {
		t.Fatalf("PruneTasks = %d changes, want 1 (the dangling task_id)", n)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("pruned a task that still has a worktree: %+v", s.Tasks)
	}
	if s.Sessions[1].TaskID != "" || s.Sessions[1].Role != "" {
		t.Fatalf("dangling task_id not cleared: %+v", s.Sessions[1])
	}

	s.Prune(func(sess Session) bool { return sess.Path != "/w/x" })
	if n := s.PruneTasks(); n != 1 {
		t.Fatalf("PruneTasks = %d changes, want 1 (the orphaned task)", n)
	}
	if len(s.Tasks) != 0 {
		t.Fatalf("orphaned task survived: %+v", s.Tasks)
	}
}

// TestRunStateSurvivesBareUpsert: the supervisor writes a role's run state, and
// an activity tick that knows nothing about runs then upserts the same row. The
// tick must not reset the thread to "never ran", which is what would make a
// restarted `norn run` re-run a role that already finished.
func TestRunStateSurvivesBareUpsert(t *testing.T) {
	s := &Store{}
	s.UpsertByPath(Session{ID: "norn:b/logic", Repo: "norn", Branch: "b/logic", Path: "/w/logic", TaskID: "abc", Role: "logic"})
	if !s.SetRun("/w/logic", RunRunning, 4242) {
		t.Fatal("SetRun reported no change on a fresh row")
	}
	if got := s.Sessions[0].RunPID; got != 4242 {
		t.Fatalf("running row lost its pid: %d", got)
	}
	if !s.SetRun("/w/logic", RunDone, 4242) {
		t.Fatal("SetRun reported no change moving running to done")
	}
	if got := s.Sessions[0].RunPID; got != 0 {
		t.Fatalf("pid outlived the running state: %d", got)
	}
	s.UpsertByPath(Session{ID: "norn:b/logic", Repo: "norn", Branch: "b/logic", Path: "/w/logic"})
	if got := s.Sessions[0].Run; got != RunDone {
		t.Fatalf("bare upsert dropped run state: %q", got)
	}
	if s.SetRun("/w/logic", RunDone, 0) {
		t.Fatal("SetRun reported a change for the state already stored")
	}
	if s.SetRun("/w/missing", RunFailed, 0) {
		t.Fatal("SetRun reported a change for a path with no row")
	}
}

// TestSetTaskBlocked: a conflicted merge blocks the task, an UpsertTask that
// omits the reason leaves it blocked, and only SetTaskBlocked clears it.
func TestSetTaskBlocked(t *testing.T) {
	s := &Store{}
	task := s.UpsertTask(Task{Repo: "norn", Goal: "split", Trunk: "feature/x/trunk"})
	if !s.SetTaskBlocked(task.ID, "merge conflict in logic") {
		t.Fatal("SetTaskBlocked reported no change")
	}
	s.UpsertTask(Task{ID: task.ID, Goal: "split again"})
	if got := s.FindTask(task.ID).Blocked; got != "merge conflict in logic" {
		t.Fatalf("UpsertTask cleared the blocked reason: %q", got)
	}
	if !s.SetTaskBlocked(task.ID, "") {
		t.Fatal("SetTaskBlocked reported no change when clearing")
	}
	if got := s.FindTask(task.ID).Blocked; got != "" {
		t.Fatalf("blocked reason not cleared: %q", got)
	}
}
