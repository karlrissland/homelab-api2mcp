package mcpserver

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractCallerUsername(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         map[string]any
		wantUsername string
		wantArgs     map[string]any
		wantErr      string
	}{
		{
			name: "no reserved caller username",
			args: map[string]any{"page": 2},
			wantArgs: map[string]any{
				"page": 2,
			},
		},
		{
			name: "reserved caller username stripped",
			args: map[string]any{
				"page":                 2,
				callerUsernameArgument: "alice",
			},
			wantUsername: "alice",
			wantArgs: map[string]any{
				"page": 2,
			},
		},
		{
			name: "caller username only leaves nil args",
			args: map[string]any{
				callerUsernameArgument: "alice",
			},
			wantUsername: "alice",
			wantArgs:     nil,
		},
		{
			name: "caller username must be string",
			args: map[string]any{
				callerUsernameArgument: 42,
			},
			wantErr: "must be a string",
		},
		{
			name: "caller username must not be empty",
			args: map[string]any{
				callerUsernameArgument: "  ",
			},
			wantErr: "must not be empty",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := cloneMap(tt.args)
			gotUsername, gotArgs, err := extractCallerUsername(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("extractCallerUsername() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractCallerUsername() error = %v", err)
			}
			if gotUsername != tt.wantUsername {
				t.Fatalf("extractCallerUsername() username = %q, want %q", gotUsername, tt.wantUsername)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("extractCallerUsername() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
			if !reflect.DeepEqual(tt.args, original) {
				t.Fatalf("extractCallerUsername() mutated input args = %#v, want %#v", tt.args, original)
			}
		})
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
