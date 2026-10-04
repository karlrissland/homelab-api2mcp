package main

import "testing"

func TestInternalBaseURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		serviceName string
		namespace   string
		addr        string
		want        string
	}{
		{
			name:        "colon-only addr (the real deployment shape)",
			serviceName: "mcp2rest",
			namespace:   "mcp2rest",
			addr:        ":8080",
			want:        "http://mcp2rest.mcp2rest.svc.cluster.local:8080",
		},
		{
			name:        "explicit host:port addr",
			serviceName: "mcp2rest",
			namespace:   "mcp2rest",
			addr:        "0.0.0.0:9090",
			want:        "http://mcp2rest.mcp2rest.svc.cluster.local:9090",
		},
		{
			name:        "malformed addr falls back to default port",
			serviceName: "mcp2rest",
			namespace:   "mcp2rest",
			addr:        "not-a-valid-addr",
			want:        "http://mcp2rest.mcp2rest.svc.cluster.local:8080",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := internalBaseURL(c.serviceName, c.namespace, c.addr); got != c.want {
				t.Fatalf("internalBaseURL(%q, %q, %q) = %q, want %q", c.serviceName, c.namespace, c.addr, got, c.want)
			}
		})
	}
}
