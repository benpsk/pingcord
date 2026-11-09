package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantStdout string
	}{
		{
			name:       "version flag prints version and exits cleanly",
			args:       []string{"-version"},
			wantStdout: "dev\n",
		},
		{
			name:    "unknown flag returns an error",
			args:    []string{"-nope"},
			wantErr: "flag provided but not defined",
		},
		{
			name:    "no args reports the server is not implemented yet",
			args:    nil,
			wantErr: "not implemented",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			err := run(tt.args, &stdout, &stderr)

			if tt.wantErr == "" && err != nil {
				t.Fatalf("run() unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("run() error = %v, want containing %q", err, tt.wantErr)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Fatalf("stdout = %q, want %q", got, tt.wantStdout)
			}
		})
	}
}
