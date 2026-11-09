package discordcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/benpsk/pingcord/internal/store"
)

const (
	registerTimeout = 15 * time.Second
	// permManageGuild hides /admin from members without "Manage Server".
	permManageGuild = "32"

	optSubcommand = 1
	optString     = 3
	optUser       = 6
)

type commandOption struct {
	Type         int             `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Required     bool            `json:"required,omitempty"`
	Autocomplete bool            `json:"autocomplete,omitempty"`
	Choices      []choice        `json:"choices,omitempty"`
	Options      []commandOption `json:"options,omitempty"`
}

type commandDef struct {
	Name                     string          `json:"name"`
	Description              string          `json:"description"`
	DefaultMemberPermissions string          `json:"default_member_permissions"`
	Options                  []commandOption `json:"options"`
}

func userOpt() commandOption {
	return commandOption{Type: optUser, Name: "user", Description: "Discord user", Required: true}
}

func githubOpt(desc string) commandOption {
	return commandOption{Type: optString, Name: "github", Description: desc, Required: true, Autocomplete: true}
}

func levelOpt() commandOption {
	opt := commandOption{Type: optString, Name: "level", Description: "How much activity to ping them for", Required: true}
	for _, l := range []string{store.LevelNewPRs, store.LevelPRs, store.LevelAll} {
		opt.Choices = append(opt.Choices, choice{Name: levelDescriptions[l], Value: l})
	}
	return opt
}

func repoOpt() commandOption {
	return commandOption{Type: optString, Name: "repo", Description: "Repository (leave empty for the whole org)", Autocomplete: true}
}

// commands is the full /admin command tree.
func commands() []commandDef {
	sub := func(name, desc string, opts ...commandOption) commandOption {
		return commandOption{Type: optSubcommand, Name: name, Description: desc, Options: opts}
	}
	return []commandDef{{
		Name:                     adminCommand,
		Description:              "Manage pingcord GitHub ↔ Discord links",
		DefaultMemberPermissions: permManageGuild,
		Options: []commandOption{
			sub("link", "Link a Discord user to a GitHub account", userOpt(), githubOpt("GitHub username")),
			sub("unlink", "Remove a Discord user's link", userOpt()),
			sub("list", "Show all links"),
			sub("unlinked", "Org members who don't have a link yet"),
			sub("ignore", "Never ping a GitHub user", githubOpt("GitHub username to ignore")),
			sub("unignore", "Allow pinging an ignored GitHub user again", githubOpt("Ignored GitHub username")),
			sub("watch", "Ping someone for org or repo activity, even without a mention", userOpt(), levelOpt(), repoOpt()),
			sub("unwatch", "Stop a watch (leave repo empty for the org-wide one)", userOpt(), repoOpt()),
		},
	}}
}

// RegisterCommands bulk-overwrites the guild's slash commands with /admin.
// It is idempotent and safe to call on every start. Errors never include the
// bot token.
func RegisterCommands(ctx context.Context, client *http.Client, apiBase, appID, guildID, botToken string) error {
	if client == nil {
		client = &http.Client{Timeout: registerTimeout}
	}
	body, err := json.Marshal(commands())
	if err != nil {
		return fmt.Errorf("register commands: encode: %w", err)
	}
	endpoint := strings.TrimRight(apiBase, "/") + "/applications/" + url.PathEscape(appID) +
		"/guilds/" + url.PathEscape(guildID) + "/commands"

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("register commands: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/benpsk/pingcord, 1)")

	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("register commands: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("register commands: discord returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
