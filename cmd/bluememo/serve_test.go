package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func exchange(t *testing.T, storePath string, lines ...string) []map[string]any {
	t.Helper()
	var output strings.Builder
	if errorValue := serve(context.Background(), storePath, strings.NewReader(strings.Join(lines, "\n")+"\n"), &output); errorValue != nil {
		t.Fatal(errorValue)
	}
	var replies []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var reply map[string]any
		if errorValue := json.Unmarshal([]byte(line), &reply); errorValue != nil {
			t.Fatalf("a client would choke on this line: %q", line)
		}
		replies = append(replies, reply)
	}
	return replies
}

func toolText(t *testing.T, reply map[string]any) (string, bool) {
	t.Helper()
	result, carried := reply["result"].(map[string]any)
	if !carried {
		t.Fatalf("expected a tool result, got %v", reply)
	}
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	return first["text"].(string), result["isError"] == true
}

// Every line a client reads has to be one JSON-RPC message. Anything else on
// this stream, a log line or a stray print, breaks every client silently.
func TestNothingButJSONRPCReachesTheClient(t *testing.T) {
	replies := exchange(t, filepath.Join(t.TempDir(), "me.db"),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(replies) != 2 {
		t.Fatalf("expected one message per request, got %d", len(replies))
	}
	for _, reply := range replies {
		if reply["jsonrpc"] != "2.0" {
			t.Fatalf("expected every message to name the protocol, got %v", reply)
		}
	}
}

// A notification carries no id and must draw no reply. Answering one leaves a
// client waiting for a response to a request it never made.
func TestANotificationDrawsNoReply(t *testing.T) {
	replies := exchange(t, filepath.Join(t.TempDir(), "me.db"),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if len(replies) != 1 {
		t.Fatalf("expected the notification to be answered with silence, got %d replies", len(replies))
	}
	if replies[0]["id"].(float64) != 1 {
		t.Fatalf("expected the reply to belong to the ping, got %v", replies[0])
	}
}

func TestTheHandshakeNamesTheProtocolAndTheTools(t *testing.T) {
	replies := exchange(t, filepath.Join(t.TempDir(), "me.db"),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	handshake := replies[0]["result"].(map[string]any)
	if handshake["protocolVersion"] != protocolVersion {
		t.Fatalf("expected %q, got %v", protocolVersion, handshake["protocolVersion"])
	}
	var named []string
	for _, descriptor := range replies[1]["result"].(map[string]any)["tools"].([]any) {
		named = append(named, descriptor.(map[string]any)["name"].(string))
	}
	if strings.Join(named, ",") != "remember,search,forget" {
		t.Fatalf("expected the three tools, got %v", named)
	}
}

// A store opened with no ports at all is the whole point: an agent brings its
// own wording and needs no key, no model and no setup step.
func TestRememberingAndRecallingNeedsNoPorts(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "me.db")
	replies := exchange(t, storePath,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"statements":["박예시 keeps the ledger in Numbers"]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"where is the ledger"}}}`)
	if text, failed := toolText(t, replies[0]); failed {
		t.Fatalf("remembering failed: %s", text)
	}
	text, failed := toolText(t, replies[1])
	if failed {
		t.Fatalf("recalling failed: %s", text)
	}
	if !strings.Contains(text, "박예시 keeps the ledger in Numbers") {
		t.Fatalf("expected the statement back, got %q", text)
	}
	if !strings.Contains(text, "no embedder is configured") {
		t.Fatalf("expected the recall to say why it ranks by wording alone, got %q", text)
	}
}

func TestForgettingTakesWhatSearchReturned(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "me.db")
	remembered := exchange(t, storePath,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remember","arguments":{"statements":["최견본 sits by the window"]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"where does 최견본 sit"}}}`)
	listed, _ := toolText(t, remembered[1])
	identifier := strings.Fields(listed)[0]

	after := exchange(t, storePath,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"forget","arguments":{"ids":["`+identifier+`"],"reason":"they moved desks"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"where does 최견본 sit"}}}`)
	if text, failed := toolText(t, after[0]); failed {
		t.Fatalf("forgetting failed: %s", text)
	}
	text, _ := toolText(t, after[1])
	if strings.Contains(text, "sits by the window") {
		t.Fatalf("expected the forgotten memory to be gone, got %q", text)
	}
}

// A tool that refuses has to come back as a tool result a model can read and
// correct. A JSON-RPC error is the transport failing, and a client may give up
// on the session for one.
func TestARefusingToolAnswersTheModelRatherThanTheTransport(t *testing.T) {
	replies := exchange(t, filepath.Join(t.TempDir(), "me.db"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"forget","arguments":{"ids":["whatever"]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"invent","arguments":{}}}`)
	text, failed := toolText(t, replies[0])
	if !failed {
		t.Fatalf("expected a missing reason to be refused, got %q", text)
	}
	if replies[0]["error"] != nil {
		t.Fatalf("expected a tool refusal to stay out of the transport, got %v", replies[0]["error"])
	}
	if replies[1]["error"] == nil {
		t.Fatalf("expected an unknown tool to be a protocol fault, got %v", replies[1])
	}
}
