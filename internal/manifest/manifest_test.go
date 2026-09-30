package manifest

import "testing"

func TestTierSatisfies(t *testing.T) {
	cases := []struct {
		held, required Tier
		want           bool
	}{
		{TierUser, TierUser, true},
		{TierUser, TierAdmin, false},
		{TierAdmin, TierUser, true},
		{TierAdmin, TierAdmin, true},
	}
	for _, c := range cases {
		if got := c.held.Satisfies(c.required); got != c.want {
			t.Errorf("Tier(%q).Satisfies(%q) = %v, want %v", c.held, c.required, got, c.want)
		}
	}
}

func TestToolValidate(t *testing.T) {
	cases := []struct {
		name    string
		tool    Tool
		wantErr bool
	}{
		{
			name: "valid rendered",
			tool: Tool{Name: "list_repos", Tier: TierUser, Type: ToolTypeRendered,
				RequestTemplate: "{}", ResponseTemplate: "{}"},
			wantErr: false,
		},
		{
			name: "valid passthrough",
			tool: Tool{Name: "list_repos", Tier: TierAdmin, Type: ToolTypePassthrough,
				UpstreamToolName: "upstream_list_repos"},
			wantErr: false,
		},
		{name: "missing name", tool: Tool{Tier: TierUser, Type: ToolTypeRendered, RequestTemplate: "x", ResponseTemplate: "x"}, wantErr: true},
		{name: "invalid tier", tool: Tool{Name: "t", Tier: "superadmin", Type: ToolTypeRendered, RequestTemplate: "x", ResponseTemplate: "x"}, wantErr: true},
		{name: "rendered missing templates", tool: Tool{Name: "t", Tier: TierUser, Type: ToolTypeRendered}, wantErr: true},
		{name: "passthrough missing upstream name", tool: Tool{Name: "t", Tier: TierUser, Type: ToolTypePassthrough}, wantErr: true},
		{name: "invalid type", tool: Tool{Name: "t", Tier: TierUser, Type: "bogus"}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tool.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestAppValidate(t *testing.T) {
	renderedTool := Tool{Name: "t1", Tier: TierUser, Type: ToolTypeRendered, RequestTemplate: "x", ResponseTemplate: "x"}

	t.Run("valid app", func(t *testing.T) {
		app := App{Name: "gitea", Tools: []Tool{renderedTool}}
		if err := app.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing name", func(t *testing.T) {
		app := App{Tools: []Tool{renderedTool}}
		if err := app.Validate(); err == nil {
			t.Error("expected error for missing name, got nil")
		}
	})

	t.Run("no tools", func(t *testing.T) {
		app := App{Name: "gitea"}
		if err := app.Validate(); err == nil {
			t.Error("expected error for no tools, got nil")
		}
	})

	t.Run("duplicate tool names", func(t *testing.T) {
		app := App{Name: "gitea", Tools: []Tool{renderedTool, renderedTool}}
		if err := app.Validate(); err == nil {
			t.Error("expected error for duplicate tool names, got nil")
		}
	})

	t.Run("passthrough tool without upstream URL", func(t *testing.T) {
		app := App{Name: "gitea", Tools: []Tool{
			{Name: "t1", Tier: TierUser, Type: ToolTypePassthrough, UpstreamToolName: "x"},
		}}
		if err := app.Validate(); err == nil {
			t.Error("expected error for passthrough tool missing app-level upstreamMCPURL, got nil")
		}
	})
}

func TestAppFindTool(t *testing.T) {
	app := App{Name: "gitea", Tools: []Tool{
		{Name: "t1", Tier: TierUser, Type: ToolTypeRendered, RequestTemplate: "x", ResponseTemplate: "x"},
	}}

	if _, ok := app.FindTool("t1"); !ok {
		t.Error("expected to find t1")
	}
	if _, ok := app.FindTool("missing"); ok {
		t.Error("expected not to find missing tool")
	}
}
