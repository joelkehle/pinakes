package bus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func migrateTestConfig(clock func() time.Time) Config {
	return Config{
		GracePeriod:            30 * time.Second,
		ProgressMinInterval:    2 * time.Second,
		IdempotencyWindow:      24 * time.Hour,
		InboxWaitMax:           1 * time.Second,
		AckTimeout:             10 * time.Second,
		DefaultMessageTTL:      600 * time.Second,
		DefaultRegistrationTTL: 60 * time.Second,
		Clock:                  clock,
	}
}

func writeSchemaOnlySQLite(t *testing.T, path string) {
	t.Helper()

	db, err := sqlx.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open schema-only sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(sqliteSchema); err != nil {
		_ = db.Close()
		t.Fatalf("create schema-only sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close schema-only sqlite: %v", err)
	}
}

func TestMigrateJSONStateToSQLiteRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	dbPath := filepath.Join(tmp, "bus.db")
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	cfg := migrateTestConfig(func() time.Time { return now })

	// Build legacy JSON state through the real PersistentStore API so the
	// fixture matches what production state files actually contain.
	ps, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("new persistent store: %v", err)
	}
	if _, err := ps.RegisterAgent(RegisterAgentInput{
		AgentID:       "a",
		Mode:          AgentModePull,
		Capabilities:  []string{"x"},
		TTLSeconds:    60,
		Version:       "v0.5.0",
		Description:   "Migration test agent.",
		AgentClass:    "worker",
		MutationClass: "observe",
		Build:         &BuildInfo{Commit: "abc1234", Dirty: false},
		Meta:          &AgentMeta{Owner: "pinakes", Repo: "github.com/joelkehle/pinakes", HealthURL: "http://a/health", Dependencies: []string{"sqlite"}},
	}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := ps.RegisterAgent(RegisterAgentInput{AgentID: "b", Mode: AgentModePull, Capabilities: []string{"y"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register b: %v", err)
	}
	if err := ps.SetAgentSecret("a", "secret-a"); err != nil {
		t.Fatalf("set secret a: %v", err)
	}
	if err := ps.SetAgentSecret("b", "secret-b"); err != nil {
		t.Fatalf("set secret b: %v", err)
	}
	// Orphan secret: keyed to an agent that was never registered, so it has no
	// matching agent row and must be dropped (and WARN-logged) by the migration.
	if err := ps.SetAgentSecret("ghost", "secret-ghost"); err != nil {
		t.Fatalf("set orphan secret: %v", err)
	}

	done, _, err := ps.SendMessage(SendMessageInput{
		To:        "b",
		From:      "a",
		RequestID: "rid-migrate-done",
		Type:      MessageTypeRequest,
		Body:      "finish me",
	})
	if err != nil {
		t.Fatalf("send terminal message: %v", err)
	}
	if err := ps.PostEvent(EventInput{ActorAgentID: "b", MessageID: done.MessageID, Type: "final", Body: "done"}); err != nil {
		t.Fatalf("complete message: %v", err)
	}
	waiting, _, err := ps.SendMessage(SendMessageInput{
		To:             "b",
		From:           "a",
		ConversationID: done.ConversationID,
		RequestID:      "rid-migrate-waiting",
		Type:           MessageTypeRequest,
		Body:           "leave me waiting",
	})
	if err != nil {
		t.Fatalf("send waiting message: %v", err)
	}

	migrated, err := MigrateJSONStateToSQLite(statePath, dbPath, cfg)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Fatalf("expected migration to run")
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected state file to be renamed away, stat err=%v", err)
	}
	if _, err := os.Stat(statePath + ".migrated"); err != nil {
		t.Fatalf("expected state file backup: %v", err)
	}

	ss, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open sqlite store after migration: %v", err)
	}
	defer ss.Close()

	agents := ss.ListAgents("")
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after migration, got %d", len(agents))
	}
	if agents[0].AgentID != "a" || agents[0].Version != "v0.5.0" || agents[0].AgentClass != "worker" {
		t.Fatalf("agent metadata missing after migration: %+v", agents[0])
	}
	if agents[0].Build == nil || agents[0].Build.Commit != "abc1234" {
		t.Fatalf("agent build missing after migration: %#v", agents[0].Build)
	}
	if agents[0].Meta == nil || agents[0].Meta.HealthURL != "http://a/health" {
		t.Fatalf("agent meta missing after migration: %#v", agents[0].Meta)
	}

	secrets, err := ss.AgentSecrets()
	if err != nil {
		t.Fatalf("agent secrets after migration: %v", err)
	}
	if secrets["a"] != "secret-a" || secrets["b"] != "secret-b" {
		t.Fatalf("secrets missing after migration: %#v", secrets)
	}
	if _, ok := secrets["ghost"]; ok {
		t.Fatalf("orphan secret should have been dropped, got %#v", secrets)
	}
	if len(secrets) != 2 {
		t.Fatalf("expected 2 secrets after migration, got %d: %#v", len(secrets), secrets)
	}

	convs := ss.ListConversations(ListConversationsFilter{})
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation after migration, got %d", len(convs))
	}
	if convs[0].ConversationID != done.ConversationID || convs[0].MessageCount != 2 {
		t.Fatalf("conversation mismatch after migration: %+v", convs[0])
	}

	doneMsg, ok := ss.GetMessageForTest(done.MessageID)
	if !ok {
		t.Fatalf("terminal message missing after migration")
	}
	if doneMsg.State != StateCompleted {
		t.Fatalf("terminal message state=%s want=%s", doneMsg.State, StateCompleted)
	}
	if doneMsg.TerminalAt.IsZero() {
		t.Fatalf("terminal message lost TerminalAt in migration")
	}
	waitingMsg, ok := ss.GetMessageForTest(waiting.MessageID)
	if !ok {
		t.Fatalf("waiting message missing after migration")
	}
	if isTerminal(waitingMsg.State) {
		t.Fatalf("waiting message unexpectedly terminal: %s", waitingMsg.State)
	}
	if waitingMsg.TTLExpiresAt.IsZero() {
		t.Fatalf("waiting message lost TTLExpiresAt in migration")
	}

	// Ordering must survive via the conversation_messages positions.
	_, msgs, _, err := ss.ListConversationMessages(ListConversationMessagesInput{
		ConversationID: done.ConversationID,
		Cursor:         0,
		Limit:          50,
	})
	if err != nil {
		t.Fatalf("list conversation messages after migration: %v", err)
	}
	if len(msgs) != 2 || msgs[0].MessageID != done.MessageID || msgs[1].MessageID != waiting.MessageID {
		t.Fatalf("conversation ordering lost in migration: %+v", msgs)
	}

	// Counters must survive: new ids continue past the migrated ones.
	fresh, _, err := ss.SendMessage(SendMessageInput{
		To:        "b",
		From:      "a",
		RequestID: "rid-migrate-fresh",
		Type:      MessageTypeRequest,
		Body:      "fresh after migration",
	})
	if err != nil {
		t.Fatalf("send after migration: %v", err)
	}
	if fresh.MessageID == done.MessageID || fresh.MessageID == waiting.MessageID {
		t.Fatalf("message id counter reset by migration: %s", fresh.MessageID)
	}
	if fresh.ConversationID == done.ConversationID {
		t.Fatalf("conversation id counter reset by migration: %s", fresh.ConversationID)
	}
}

func TestMigrateJSONStateToSQLiteRecoversFromCrashedImport(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	dbPath := filepath.Join(tmp, "bus.db")
	tmpPath := dbPath + ".tmp"
	now := time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC)
	cfg := migrateTestConfig(func() time.Time { return now })

	ps, err := NewPersistentStore(statePath, cfg)
	if err != nil {
		t.Fatalf("new persistent store: %v", err)
	}
	if _, err := ps.RegisterAgent(RegisterAgentInput{AgentID: "a", Mode: AgentModePull, Capabilities: []string{"x"}, TTLSeconds: 60}); err != nil {
		t.Fatalf("register a: %v", err)
	}
	msg, _, err := ps.SendMessage(SendMessageInput{
		To:        "a",
		From:      "a",
		RequestID: "rid-crash-retry",
		Type:      MessageTypeRequest,
		Body:      "survive retry",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	writeSchemaOnlySQLite(t, tmpPath)

	migrated, err := MigrateJSONStateToSQLite(statePath, dbPath, cfg)
	if err != nil {
		t.Fatalf("migrate after crashed import: %v", err)
	}
	if !migrated {
		t.Fatalf("expected migration to run")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected final db file: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected tmp db file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(tmpPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("expected tmp wal file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(tmpPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("expected tmp shm file to be removed, stat err=%v", err)
	}

	ss, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("open sqlite store after migration: %v", err)
	}
	defer ss.Close()

	agents := ss.ListAgents("")
	if len(agents) != 1 || agents[0].AgentID != "a" {
		t.Fatalf("expected migrated agent a, got %+v", agents)
	}
	got, ok := ss.GetMessageForTest(msg.MessageID)
	if !ok {
		t.Fatalf("migrated message missing")
	}
	if got.Body != "survive retry" || got.From != "a" || got.To != "a" {
		t.Fatalf("migrated message mismatch: %+v", got)
	}
}

func TestMigrateJSONStateToSQLiteSkips(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	dbPath := filepath.Join(tmp, "bus.db")
	cfg := migrateTestConfig(time.Now)

	// Neither file exists: no-op, and no db file gets created.
	migrated, err := MigrateJSONStateToSQLite(statePath, dbPath, cfg)
	if err != nil {
		t.Fatalf("migrate with nothing to do: %v", err)
	}
	if migrated {
		t.Fatalf("expected no migration without a state file")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("expected no db file, stat err=%v", err)
	}

	// DB already exists (second startup): state file must be left alone.
	ss, err := NewSQLiteStore(dbPath, cfg)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	ss.Close()
	if err := os.WriteFile(statePath, []byte(`{"agents":{}}`), 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	migrated, err = MigrateJSONStateToSQLite(statePath, dbPath, cfg)
	if err != nil {
		t.Fatalf("migrate with existing db: %v", err)
	}
	if migrated {
		t.Fatalf("expected migration skip when db exists")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file must be untouched when migration skips: %v", err)
	}
}

func TestMigrateJSONStateToSQLiteFailsLoudlyOnCorruptState(t *testing.T) {
	tmp := t.TempDir()
	statePath := filepath.Join(tmp, "state.json")
	dbPath := filepath.Join(tmp, "bus.db")
	tmpPath := dbPath + ".tmp"
	cfg := migrateTestConfig(time.Now)

	if err := os.WriteFile(statePath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt state file: %v", err)
	}
	writeSchemaOnlySQLite(t, tmpPath)

	migrated, err := MigrateJSONStateToSQLite(statePath, dbPath, cfg)
	if err == nil {
		t.Fatalf("expected error for corrupt state file")
	}
	if migrated {
		t.Fatalf("corrupt state must not report success")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("corrupt state file must not be renamed: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("failed migration must not leave a db file, stat err=%v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("failed migration must not leave a tmp db file, stat err=%v", err)
	}
	if _, err := os.Stat(tmpPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("failed migration must not leave a tmp wal file, stat err=%v", err)
	}
	if _, err := os.Stat(tmpPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("failed migration must not leave a tmp shm file, stat err=%v", err)
	}
}
