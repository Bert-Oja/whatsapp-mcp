package wabridge

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestOpenModerncSQLiteWithWhatsmeow is the Phase 1 gate for the pure-Go SQLite
// decision: whatsmeow's sqlstore must run its schema migrations against
// modernc.org/sqlite (driver "sqlite", pragma _pragma=foreign_keys(1)).
func TestOpenModerncSQLiteWithWhatsmeow(t *testing.T) {
	dir := t.TempDir()

	b, err := Open(dir, testLogger())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer b.Close()

	if b.Client == nil {
		t.Fatal("client is nil")
	}
	if b.Client.Store == nil || b.Client.Store.ID != nil {
		t.Fatal("expected a fresh, unpaired device store")
	}

	// The whatsmeow session schema must have been created in whatsapp.db.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "whatsapp.db"))
	if err != nil {
		t.Fatalf("opening whatsapp.db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'whatsmeow_%'",
	).Scan(&count); err != nil {
		t.Fatalf("querying whatsmeow schema: %v", err)
	}
	if count == 0 {
		t.Fatal("no whatsmeow_* tables created; migrations did not run")
	}
	t.Logf("whatsmeow created %d tables in %s", count, filepath.Join(dir, "whatsapp.db"))

	// Our own message store must coexist in the same directory.
	if _, err := b.Store.DB().Exec("SELECT 1"); err != nil {
		t.Fatalf("message store unusable: %v", err)
	}
}
