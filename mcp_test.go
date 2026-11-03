package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPStdioSession(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"sessions_search","arguments":{"query":"ab"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := serveMCP(context.Background(), Settings{DeadlineMS: 1000, PastLimit: 10}, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("response %q is not JSON: %v", l, err)
		}
		got = append(got, m)
	}
	if len(got) != 6 {
		t.Fatalf("got %d responses, want 6 (none for the notification): %s", len(got), out.String())
	}
	field := func(m map[string]any, path ...string) any {
		var v any = m
		for _, p := range path {
			v = v.(map[string]any)[p]
		}
		return v
	}
	if v := field(got[0], "result", "protocolVersion"); v != "2025-06-18" {
		t.Errorf("initialize agreed on %v, want the client's 2025-06-18", v)
	}
	if got[1]["id"] != "two" || len(field(got[1], "result", "tools").([]any)) != 4 {
		t.Errorf("tools/list = %v, want 4 tools under id \"two\"", got[1])
	}
	if code := field(got[2], "error", "code"); code != float64(rpcMethodNotFound) {
		t.Errorf("unknown method code = %v, want %d", code, rpcMethodNotFound)
	}
	if code := field(got[3], "error", "code"); code != float64(rpcParseError) || got[3]["id"] != nil {
		t.Errorf("parse error = %v, want code %d and a null id", got[3], rpcParseError)
	}
	if field(got[4], "result", "isError") != true {
		t.Errorf("a query with no 3-letter word = %v, want a tool error result", got[4])
	}
	if v := field(got[5], "result", "protocolVersion"); v != mcpVersions[0] {
		t.Errorf("unknown client version got %v, want our latest %s", v, mcpVersions[0])
	}
}
