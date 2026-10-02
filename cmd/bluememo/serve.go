package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeomyeonggeori/bluememo"
)

const protocolVersion = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *responseError  `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func serve(ctx context.Context, storePath string, input io.Reader, output io.Writer) error {
	if errorValue := os.MkdirAll(filepath.Dir(storePath), 0o700); errorValue != nil {
		return errorValue
	}
	store, errorValue := bluememo.Open(ctx, storePath, bluememo.Configuration{})
	if errorValue != nil {
		return errorValue
	}
	defer store.Close()

	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	encoder := json.NewEncoder(output)

	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}
		var incoming request
		if errorValue := json.Unmarshal([]byte(line), &incoming); errorValue != nil {
			continue
		}
		if len(incoming.ID) == 0 {
			continue
		}
		result, failure := answer(ctx, store, incoming)
		reply := response{JSONRPC: "2.0", ID: incoming.ID, Result: result}
		if failure != nil {
			reply.Result = nil
			reply.Error = failure
		}
		if errorValue := encoder.Encode(reply); errorValue != nil {
			return errorValue
		}
	}
	return reader.Err()
}

func answer(ctx context.Context, store *bluememo.Store, incoming request) (any, *responseError) {
	switch incoming.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "bluememo", "version": serverVersion()},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDescriptors()}, nil
	case "tools/call":
		return callTool(ctx, store, incoming.Params)
	}
	return nil, &responseError{Code: -32601, Message: "no method named " + incoming.Method}
}

type toolCall struct {
	Name      string `json:"name"`
	Arguments struct {
		Statements []string `json:"statements"`
		Query      string   `json:"query"`
		Limit      int      `json:"limit"`
		IDs        []string `json:"ids"`
		Reason     string   `json:"reason"`
	} `json:"arguments"`
}

func callTool(ctx context.Context, store *bluememo.Store, params json.RawMessage) (any, *responseError) {
	var call toolCall
	if errorValue := json.Unmarshal(params, &call); errorValue != nil {
		return nil, &responseError{Code: -32602, Message: errorValue.Error()}
	}
	switch call.Name {
	case "remember":
		return told(remember(ctx, store, call.Arguments.Statements))
	case "search":
		return told(search(ctx, store, call.Arguments.Query, call.Arguments.Limit))
	case "forget":
		return told(forget(ctx, store, call.Arguments.IDs, call.Arguments.Reason))
	}
	return nil, &responseError{Code: -32602, Message: "no tool named " + call.Name}
}

func told(text string, errorValue error) (any, *responseError) {
	if errorValue != nil {
		text = errorValue.Error()
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": errorValue != nil,
	}, nil
}

func remember(ctx context.Context, store *bluememo.Store, statements []string) (string, error) {
	origin := bluememo.NewIdentifier()
	adopted := make([]bluememo.AdoptedMemory, 0, len(statements))
	for _, statement := range statements {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		adopted = append(adopted, bluememo.AdoptedMemory{Memory: bluememo.Memory{
			MemoryID:  bluememo.NewIdentifier(),
			Content:   statement,
			OriginID:  origin,
			CreatedAt: time.Now().UTC(),
		}})
	}
	if len(adopted) == 0 {
		return "", errors.New("remember needs at least one statement")
	}
	report, errorValue := store.Adopt(ctx, adopted)
	if errorValue != nil {
		return "", errorValue
	}
	return fmt.Sprintf("remembered %d, already held %d", report.Adopted, report.AlreadyHeld), nil
}

func search(ctx context.Context, store *bluememo.Store, query string, limit int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", errors.New("search needs a query")
	}
	if limit <= 0 {
		limit = 10
	}
	result, errorValue := store.Recall(ctx, query, limit)
	if errorValue != nil {
		return "", errorValue
	}
	if len(result.Memories) == 0 {
		return "nothing remembered about that yet", nil
	}
	var written strings.Builder
	for _, recalled := range result.Memories {
		fmt.Fprintf(&written, "%s  %s\n", recalled.Memory.MemoryID, recalled.Memory.Content)
	}
	if result.DegradedReason != "" {
		fmt.Fprintf(&written, "\nranked by wording only: %s\n", result.DegradedReason)
	}
	return written.String(), nil
}

func forget(ctx context.Context, store *bluememo.Store, memoryIDs []string, reason string) (string, error) {
	if len(memoryIDs) == 0 {
		return "", errors.New("forget needs the identifiers search returned")
	}
	if strings.TrimSpace(reason) == "" {
		return "", errors.New("forget needs a reason, which is kept with the tombstone")
	}
	forgotten, errorValue := store.ForgetMemories(ctx, memoryIDs, reason)
	if errorValue != nil {
		return "", errorValue
	}
	return fmt.Sprintf("forgot %d of %d", len(forgotten), len(memoryIDs)), nil
}
