package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"shelley.exe.dev/db/generated"
)

func TestModelProviderCRUD(t *testing.T) {
	database := setupTestDB(t)
	ctx := t.Context()

	providers, err := database.GetModelProviders(ctx)
	if err != nil {
		t.Fatalf("GetModelProviders on empty table: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("GetModelProviders on empty table returned %d rows", len(providers))
	}

	created, err := database.CreateModelProvider(ctx, generated.CreateModelProviderParams{
		ProviderID:   "provider-1",
		ProviderType: "openai",
		Endpoint:     "https://api.example.test/v1",
		ApiKey:       "secret",
		DisplayName:  "Example",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	if created.ProviderID != "provider-1" || created.ProviderType != "openai" ||
		created.Endpoint != "https://api.example.test/v1" || created.ApiKey != "secret" ||
		created.DisplayName != "Example" {
		t.Fatalf("CreateModelProvider returned unexpected row: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("CreateModelProvider returned empty timestamps: %+v", created)
	}

	got, err := database.GetModelProvider(ctx, created.ProviderID)
	if err != nil {
		t.Fatalf("GetModelProvider: %v", err)
	}
	if *got != *created {
		t.Fatalf("GetModelProvider = %+v, want %+v", got, created)
	}
	if _, err := database.GetModelProvider(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetModelProvider(missing) error = %v, want sql.ErrNoRows", err)
	}
	providers, err = database.GetModelProviders(ctx)
	if err != nil {
		t.Fatalf("GetModelProviders: %v", err)
	}
	if len(providers) != 1 || providers[0] != *created {
		t.Fatalf("GetModelProviders = %+v, want only %+v", providers, created)
	}

	oldTimestamp := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := database.Pool().Exec(ctx,
		"UPDATE model_providers SET created_at = ?, updated_at = ? WHERE provider_id = ?",
		oldTimestamp, oldTimestamp, created.ProviderID,
	); err != nil {
		t.Fatalf("set old timestamps: %v", err)
	}
	updated, err := database.UpdateModelProvider(ctx, generated.UpdateModelProviderParams{
		ProviderID:   created.ProviderID,
		ProviderType: "anthropic",
		Endpoint:     "https://api.example.test/custom",
		ApiKey:       "new-secret",
		DisplayName:  "Updated",
	})
	if err != nil {
		t.Fatalf("UpdateModelProvider: %v", err)
	}
	if updated.ProviderID != created.ProviderID || updated.ProviderType != "anthropic" ||
		updated.Endpoint != "https://api.example.test/custom" || updated.ApiKey != "new-secret" ||
		updated.DisplayName != "Updated" {
		t.Fatalf("UpdateModelProvider returned unexpected row: %+v", updated)
	}
	if !updated.CreatedAt.Equal(oldTimestamp) || !updated.UpdatedAt.After(oldTimestamp) {
		t.Fatalf("UpdateModelProvider timestamps = created %v updated %v", updated.CreatedAt, updated.UpdatedAt)
	}
	if _, err := database.UpdateModelProvider(ctx, generated.UpdateModelProviderParams{
		ProviderID: "missing",
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateModelProvider(missing) error = %v, want sql.ErrNoRows", err)
	}

	if err := database.DeleteModelProvider(ctx, created.ProviderID); err != nil {
		t.Fatalf("DeleteModelProvider: %v", err)
	}
	if err := database.DeleteModelProvider(ctx, created.ProviderID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteModelProvider(missing) error = %v, want sql.ErrNoRows", err)
	}
}

func TestModelProviderMigrationOldNewOldNew(t *testing.T) {
	database, err := New(Config{DSN: filepath.Join(t.TempDir(), "migration.db")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := t.Context()

	// An old binary has only the migration filenames embedded at its build.
	// Start with that filename set, then create pre-upgrade user data.
	if err := migratePriorEmbeddedFilenameSet(ctx, database); err != nil {
		t.Fatalf("old migration set: %v", err)
	}
	conversation, err := database.CreateConversation(ctx, nil, true, nil, nil, ConversationOptions{})
	if err != nil {
		t.Fatalf("CreateConversation before upgrade: %v", err)
	}
	oldNames := migrationNames(t, database, ctx)

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("new migration set: %v", err)
	}
	newNames := migrationNames(t, database, ctx)
	if len(newNames) != len(oldNames)+1 {
		t.Fatalf("new migration set recorded %d names, old set had %d", len(newNames), len(oldNames))
	}
	if newNames[len(newNames)-1] != "049-model-providers.sql" {
		t.Fatalf("last migration = %q, want 049-model-providers.sql", newNames[len(newNames)-1])
	}
	assertModelProvidersTable(t, database, ctx)
	savedProvider, err := database.CreateModelProvider(ctx, generated.CreateModelProviderParams{
		ProviderID:   "survives-downgrade",
		ProviderType: "openai",
		Endpoint:     "https://api.example.test/v1",
		ApiKey:       "saved-secret",
		DisplayName:  "Saved provider",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider after upgrade: %v", err)
	}

	// Simulate downgrade to the previous executable: it sees only its embedded
	// filenames, so it skips already-recorded migrations and cannot drop the
	// additive table. It does not actually reverse schema changes.
	if err := migratePriorEmbeddedFilenameSet(ctx, database); err != nil {
		t.Fatalf("old migration set after upgrade: %v", err)
	}
	if got := migrationNames(t, database, ctx); !equalStrings(got, newNames) {
		t.Fatalf("old runner changed migration bookkeeping: got %v, want %v", got, newNames)
	}
	assertModelProvidersTable(t, database, ctx)
	gotProvider, err := database.GetModelProvider(ctx, savedProvider.ProviderID)
	if err != nil {
		t.Fatalf("GetModelProvider after old runner: %v", err)
	}
	if *gotProvider != *savedProvider {
		t.Fatalf("provider after old runner = %+v, want %+v", gotProvider, savedProvider)
	}

	// Upgrading again is idempotent and preserves both user data and migration
	// bookkeeping. The old runner's real downgrade limit is that it leaves this
	// new table behind; it is not a schema down-migration.
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("new migration set after old runner: %v", err)
	}
	if got := migrationNames(t, database, ctx); !equalStrings(got, newNames) {
		t.Fatalf("new runner changed migration bookkeeping: got %v, want %v", got, newNames)
	}
	gotConversation, err := database.GetConversationByID(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatalf("GetConversationByID after old/new/old/new: %v", err)
	}
	if gotConversation.ConversationID != conversation.ConversationID {
		t.Fatalf("conversation ID after migration = %q, want %q", gotConversation.ConversationID, conversation.ConversationID)
	}
	assertModelProvidersTable(t, database, ctx)
}

func migratePriorEmbeddedFilenameSet(ctx context.Context, database *DB) error {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return fmt.Errorf("read embedded schema: %w", err)
	}
	pattern := regexp.MustCompile(`^(\d{3})-.*\.sql$`)
	type migration struct {
		name   string
		number int
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "049-model-providers.sql" {
			continue
		}
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		var number int
		if _, err := fmt.Sscanf(match[1], "%d", &number); err != nil {
			return fmt.Errorf("parse migration number from %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{name: entry.Name(), number: number})
	}
	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].number != migrations[j].number {
			return migrations[i].number < migrations[j].number
		}
		return migrations[i].name < migrations[j].name
	})

	applied := make(map[string]bool)
	err = database.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		var tableCount int
		if err := rx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migrations'").Scan(&tableCount); err != nil {
			return err
		}
		if tableCount == 0 {
			return nil
		}
		rows, err := rx.Query("SELECT migration_name FROM migrations")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			applied[name] = true
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("read migration bookkeeping: %w", err)
	}
	for _, migration := range migrations {
		if applied[migration.name] {
			continue
		}
		if err := database.runMigration(ctx, migration.name, migration.number); err != nil {
			return err
		}
	}
	return nil
}

func migrationNames(t *testing.T, database *DB, ctx context.Context) []string {
	t.Helper()
	var names []string
	if err := database.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Query("SELECT migration_name FROM migrations ORDER BY migration_number, migration_name")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			names = append(names, name)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read migration names: %v", err)
	}
	return names
}

func assertModelProvidersTable(t *testing.T, database *DB, ctx context.Context) {
	t.Helper()
	var count int
	err := database.Pool().Rx(ctx, func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'model_providers'").Scan(&count)
	})
	if err != nil {
		t.Fatalf("check model_providers table: %v", err)
	}
	if count != 1 {
		t.Fatalf("model_providers table count = %d, want 1", count)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
