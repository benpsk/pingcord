package store

import (
	"context"
	"testing"
)

func TestBindProvider(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BindProvider(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindProvider(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindProvider(ctx, "discord"); err == nil {
		t.Fatal("platform switch accepted")
	}
	if err := s.BindProvider(ctx, "unknown"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestLegacyLinksBelongToDiscord(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.UpsertLink(ctx, Link{GitHubID: 1, GitHubLogin: "alice", DiscordID: "123456789012345678", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database before migration 003, then rerun the migrations.
	if _, err = s.db.ExecContext(ctx, `DROP TABLE notification_provider; DELETE FROM schema_version WHERE version=3`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.BindProvider(ctx, "telegram"); err == nil {
		t.Fatal("legacy Discord links treated as Telegram IDs")
	}
	if err = s.BindProvider(ctx, "discord"); err != nil {
		t.Fatal(err)
	}
}
