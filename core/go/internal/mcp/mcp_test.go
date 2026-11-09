package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/mcp/mcptest"
)

func TestClientInitListCall(t *testing.T) {
	c := New("fake", []string{"python3", "-I", "-c", mcptest.Script}, []string{"PATH=" + os.Getenv("PATH")}, 5*time.Second, nil)
	defer c.Close()
	tools, err := c.Tools(context.Background())
	if err != nil || len(tools) != 3 {
		t.Fatalf("%v %v", err, tools)
	}
	r, err := c.Call(context.Background(), "lookup", map[string]any{"id": 7})
	if err != nil || r.Text != "record for 7" || r.IsError {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := c.Call(context.Background(), "missing", nil); err == nil || !strings.Contains(err.Error(), "does not offer") {
		t.Fatal(err)
	}
}
