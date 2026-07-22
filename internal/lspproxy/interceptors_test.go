package lspproxy_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/netops2devops/yaml-schema-router/internal/lspproxy"
)

// realWorldCompletionPayload is a captured textDocument/completion response
// from yaml-language-server, reproducing the EnvoyProxy "remote" module
// completion that renders shifted flush-left in editors that don't reindent
// multi-line snippets to the insertion column.
const realWorldCompletionPayload = `{"jsonrpc":"2.0","id":239,"result":{"items":[` +
	`{"kind":10,"label":"local","insertText":"local:\n  path: ","insertTextFormat":2,` +
	`"textEdit":{"range":{"start":{"line":10,"character":6},"end":{"line":10,"character":6}},` +
	`"newText":"local:\n  path: "}},` +
	`{"kind":10,"label":"remote","insertText":"remote:\n  sha256: $1\n  url: $2","insertTextFormat":2,` +
	`"textEdit":{"range":{"start":{"line":10,"character":6},"end":{"line":10,"character":6}},` +
	`"newText":"remote:\n  sha256: $1\n  url: $2"}}` +
	`],"isIncomplete":false}}`

type completionResponse struct {
	Result struct {
		Items []struct {
			Label      string `json:"label"`
			InsertText string `json:"insertText"`
			TextEdit   struct {
				NewText string `json:"newText"`
			} `json:"textEdit"`
		} `json:"items"`
	} `json:"result"`
}

func decodeCompletion(t *testing.T, payload []byte) completionResponse {
	t.Helper()
	var resp completionResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return resp
}

func TestFixCompletionIndentationRealPayload(t *testing.T) {
	got := lspproxy.FixCompletionIndentation([]byte(realWorldCompletionPayload))
	resp := decodeCompletion(t, got)

	if len(resp.Result.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(resp.Result.Items))
	}

	// The insertion column is character 6, so a snippet line written 2
	// spaces deep (relative to column 0) must land at 8 spaces.
	wantLocal := "local:\n        path: "
	if resp.Result.Items[0].InsertText != wantLocal {
		t.Errorf("local insertText = %q, want %q", resp.Result.Items[0].InsertText, wantLocal)
	}
	if resp.Result.Items[0].TextEdit.NewText != wantLocal {
		t.Errorf("local textEdit.newText = %q, want %q", resp.Result.Items[0].TextEdit.NewText, wantLocal)
	}

	wantRemote := "remote:\n        sha256: $1\n        url: $2"
	if resp.Result.Items[1].InsertText != wantRemote {
		t.Errorf("remote insertText = %q, want %q", resp.Result.Items[1].InsertText, wantRemote)
	}
	if resp.Result.Items[1].TextEdit.NewText != wantRemote {
		t.Errorf("remote textEdit.newText = %q, want %q", resp.Result.Items[1].TextEdit.NewText, wantRemote)
	}
}

func TestFixCompletionIndentationSingleLineUnchanged(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"items":[` +
		`{"label":"foo","insertText":"foo: bar",` +
		`"textEdit":{"range":{"start":{"line":0,"character":4},"end":{"line":0,"character":4}},"newText":"foo: bar"}}` +
		`]}}`)

	got := lspproxy.FixCompletionIndentation(payload)
	resp := decodeCompletion(t, got)

	want := "foo: bar"
	if resp.Result.Items[0].InsertText != want {
		t.Errorf("insertText = %q, want %q (single-line snippets should be untouched)", resp.Result.Items[0].InsertText, want)
	}
}

func TestFixCompletionIndentationMissingTextEditUnchanged(t *testing.T) {
	// Without a textEdit range, the insertion column is unknown, so the
	// snippet must be forwarded exactly as received rather than guessed at.
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"items":[` +
		`{"label":"remote","insertText":"remote:\n  sha256: $1\n  url: $2"}` +
		`]}}`)

	got := lspproxy.FixCompletionIndentation(payload)
	resp := decodeCompletion(t, got)

	want := "remote:\n  sha256: $1\n  url: $2"
	if resp.Result.Items[0].InsertText != want {
		t.Errorf("insertText = %q, want %q", resp.Result.Items[0].InsertText, want)
	}
}

func TestFixCompletionIndentationNonCompletionPayloadPassthrough(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"result":{"contents":"some hover text"}}`)

	got := lspproxy.FixCompletionIndentation(payload)
	if !bytes.Equal(got, payload) {
		t.Errorf("non-completion payload was modified: got %q, want %q", got, payload)
	}
}
