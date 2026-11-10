package discordcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterCommands(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth string
		gotBody                     []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	err := RegisterCommands(context.Background(), srv.Client(), srv.URL+"/api/v10", "111", "222", "bot-secret")

	if err != nil {
		t.Fatalf("RegisterCommands: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v10/applications/111/guilds/222/commands" || gotAuth != "Bot bot-secret" {
		t.Fatalf("request = %s %s auth=%q", gotMethod, gotPath, gotAuth)
	}
	if len(gotBody) != 1 || gotBody[0]["name"] != "admin" || gotBody[0]["default_member_permissions"] != "32" {
		t.Fatalf("body = %+v", gotBody)
	}
	subs := map[string]bool{}
	for _, o := range gotBody[0]["options"].([]any) {
		subs[o.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"link", "unlink", "list", "unlinked", "ignore", "unignore", "watch", "unwatch"} {
		if !subs[want] {
			t.Errorf("subcommand %q not registered", want)
		}
	}
	for _, o := range gotBody[0]["options"].([]any) {
		sub := o.(map[string]any)
		if sub["name"] != "watch" {
			continue
		}
		opts := sub["options"].([]any)
		level := opts[1].(map[string]any)
		if level["name"] != "level" || len(level["choices"].([]any)) != 3 {
			t.Errorf("watch level option = %+v", level)
		}
		repo := opts[2].(map[string]any)
		if repo["name"] != "repo" || repo["required"] != true || repo["autocomplete"] != true {
			t.Errorf("watch repo option = %+v", repo)
		}
	}
	for _, o := range gotBody[0]["options"].([]any) {
		sub := o.(map[string]any)
		if sub["name"] != "unwatch" {
			continue
		}
		repo := sub["options"].([]any)[1].(map[string]any)
		if repo["name"] != "repo" || repo["required"] != true {
			t.Errorf("unwatch repo option = %+v", repo)
		}
	}
}

func TestRegisterCommandsErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"401: Unauthorized"}`)
	}))
	defer srv.Close()

	err := RegisterCommands(context.Background(), srv.Client(), srv.URL, "111", "222", "bot-secret")

	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "bot-secret") {
		t.Fatalf("err = %v", err)
	}

	err = RegisterCommands(context.Background(), nil, "http://127.0.0.1:1", "111", "222", "bot-secret")
	if err == nil || strings.Contains(err.Error(), "bot-secret") {
		t.Fatalf("network err = %v", err)
	}
}
