package lspproxy

import (
	"encoding/json"
	"log"
	"strings"
)

const maxSchemaScanLines = 10

// hasSchemaAnnotation checks if the provided text contains a manual schema
// annotation (e.g., `# yaml-language-server: $schema=`) in the first few lines.
func (p *Proxy) hasSchemaAnnotation(text string) bool {
	lines := strings.SplitN(text, "\n", maxSchemaScanLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check if the line is exactly the modeline format
		if strings.HasPrefix(trimmed, "# yaml-language-server: $schema=") {
			return true
		}
	}
	return false
}

// interceptWorkspaceConfiguration dynamically injects schema configurations
// into the editor's response to the language server.
func (p *Proxy) interceptWorkspaceConfiguration(msg *BaseRPC, payload []byte) []byte {
	var result []any
	if err := json.Unmarshal(msg.Result, &result); err != nil || len(result) == 0 {
		return payload
	}

	// Ensure we have a map to work with, even if the editor returned null.
	if result[0] == nil {
		result[0] = make(map[string]any)
	}

	// The first item in the array is the "yaml" section requested by the server
	yamlConfig, ok := result[0].(map[string]any)
	if !ok {
		return payload
	}

	modified := p.injectFeatureDefaults(yamlConfig)

	groupedSchemas := p.getGroupedSchemas()

	// If no schemas are detected yet, return unmodified payload
	if len(groupedSchemas) == 0 {
		log.Printf("[%s] Intercepted workspace/configuration, but no schemas detected to inject.", componentName)
		modified = true
	}

	if !modified {
		return payload
	}

	log.Printf("[%s] Injecting schemas into workspace/configuration: %v", componentName, groupedSchemas)

	// Inject our schemas into Helix's response
	yamlConfig["schemas"] = groupedSchemas
	result[0] = yamlConfig

	newResult, err := json.Marshal(result)
	if err != nil {
		log.Printf("[%s] Error re-marshaling configuration result: %v", componentName, err)
		return payload
	}
	msg.Result = newResult

	modifiedPayload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[%s] Error re-marshaling configuration payload: %v", componentName, err)
		return payload
	}

	return modifiedPayload
}

func (p *Proxy) injectFeatureDefaults(yamlConfig map[string]any) bool {
	modified := false
	featureDefaults := map[string]bool{
		"hover":      p.hover,
		"completion": p.completion,
		"validation": p.validation,
	}

	// Inject defaults only if the key does not already exist in the user's config.
	for key, defaultValue := range featureDefaults {
		if _, exists := yamlConfig[key]; !exists {
			yamlConfig[key] = defaultValue
			modified = true
		}
	}
	return modified
}

func (p *Proxy) getGroupedSchemas() map[string][]string {
	p.stateMutex.RLock()
	defer p.stateMutex.RUnlock()
	groupedSchemas := make(map[string][]string)
	for uri, schemaURL := range p.schemaState {
		groupedSchemas[schemaURL] = append(groupedSchemas[schemaURL], uri)
	}
	return groupedSchemas
}

// FixCompletionIndentation intercepts textDocument/completion (and
// completionItem/resolve) responses and rewrites multi-line snippet text so
// continuation lines carry the same leading indentation as the insertion
// point. LSP servers write snippet text relative to column 0 and rely on the
// client's snippet engine to align it to the insertion column; clients that
// only reindent the first line leave nested completions shifted flush-left.
func FixCompletionIndentation(payload []byte) []byte {
	if !strings.Contains(string(payload), `"insertText"`) {
		return payload
	}

	var msg BaseRPC
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Result) == 0 {
		return payload
	}

	newResult, changed := reindentCompletionResult(msg.Result)
	if !changed {
		return payload
	}
	msg.Result = newResult

	newPayload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[%s] Error re-marshaling completion payload: %v", componentName, err)
		return payload
	}

	return newPayload
}

// reindentCompletionResult decodes an LSP completion result and reindents
// any multi-line snippet items it contains, returning the re-encoded result
// and whether anything changed.
func reindentCompletionResult(rawResult json.RawMessage) (json.RawMessage, bool) {
	var result any
	if err := json.Unmarshal(rawResult, &result); err != nil {
		return rawResult, false
	}

	items := completionItemsOf(result)
	if items == nil {
		return rawResult, false
	}

	changed := false
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && indentSnippetItem(item) {
			changed = true
		}
	}
	if !changed {
		return rawResult, false
	}

	newResult, err := json.Marshal(result)
	if err != nil {
		log.Printf("[%s] Error re-marshaling completion result: %v", componentName, err)
		return rawResult, false
	}

	return newResult, true
}

// completionItemsOf extracts the completion items from an LSP completion
// result, which may be a bare array, a CompletionList, or (for
// completionItem/resolve) a single item.
func completionItemsOf(result any) []any {
	switch v := result.(type) {
	case []any:
		return v
	case map[string]any:
		if items, ok := v["items"].([]any); ok {
			return items
		}
		if _, ok := v["label"]; ok {
			return []any{v}
		}
	}
	return nil
}

// indentSnippetItem reindents a single completion item's multi-line snippet
// text in place, using its textEdit's insertion column as the base
// indentation. It reports whether it changed anything.
func indentSnippetItem(item map[string]any) bool {
	baseIndent, ok := snippetBaseIndent(item)
	if !ok {
		return false
	}

	changed := false
	if text, ok := item["insertText"].(string); ok {
		if indented, did := reindentSnippet(text, baseIndent); did {
			item["insertText"] = indented
			changed = true
		}
	}

	if textEdit, ok := item["textEdit"].(map[string]any); ok {
		if text, ok := textEdit["newText"].(string); ok {
			if indented, did := reindentSnippet(text, baseIndent); did {
				textEdit["newText"] = indented
				changed = true
			}
		}
	}

	return changed
}

// snippetBaseIndent reads the insertion column from a completion item's
// textEdit range. Without it, there is no reliable way to know how far to
// indent continuation lines, so the caller must leave the item untouched.
func snippetBaseIndent(item map[string]any) (int, bool) {
	textEdit, ok := item["textEdit"].(map[string]any)
	if !ok {
		return 0, false
	}
	rng, ok := textEdit["range"].(map[string]any)
	if !ok {
		return 0, false
	}
	start, ok := rng["start"].(map[string]any)
	if !ok {
		return 0, false
	}
	character, ok := start["character"].(float64)
	if !ok {
		return 0, false
	}
	return int(character), true
}

// reindentSnippet prepends baseIndent spaces to every line after the first in
// a multi-line snippet, so nested lines align under the insertion point
// instead of arriving at whatever column-0-relative indentation the server
// wrote them with.
func reindentSnippet(text string, baseIndent int) (string, bool) {
	if baseIndent <= 0 || !strings.Contains(text, "\n") {
		return text, false
	}

	pad := strings.Repeat(" ", baseIndent)
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n"), true
}

// forceFullSync intercepts the 'initialize' response from the language server
// and overwrites the textDocumentSync capability to 1 (Full Sync).
func (p *Proxy) forceFullSync(payload []byte) []byte {
	// Fast path: avoid JSON unmarshaling if this clearly isn't an initialize response.
	// We just look for "capabilities" and "textDocumentSync".
	payloadStr := string(payload)
	if !strings.Contains(payloadStr, `"capabilities"`) || !strings.Contains(payloadStr, `"textDocumentSync"`) {
		return payload
	}

	var msg BaseRPC
	if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Result) == 0 {
		return payload
	}

	var result map[string]any
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		return payload
	}

	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		return payload
	}

	if _, exists := capabilities["textDocumentSync"]; exists {
		capabilities["textDocumentSync"] = 1
		result["capabilities"] = capabilities

		if newResult, resultErr := json.Marshal(result); resultErr == nil {
			msg.Result = newResult
			if newPayload, msgErr := json.Marshal(msg); msgErr == nil {
				log.Printf("[%s] Successfully intercepted 'initialize' and forced textDocumentSync to Full (1)", componentName)
				return newPayload
			}
			log.Printf("[%s] Warning: failed to re-marshal modified capabilities: %v", componentName, resultErr)
		}
	}

	return payload
}
