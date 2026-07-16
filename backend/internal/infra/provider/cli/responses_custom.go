package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// normalizeCustomTool 将任意字符串输入包装为普通函数。
// format.type=text 原样兼容；grammar 等约束无法在 Grok Build 等价执行，降级为自由文本并给出兼容警告。
func (c *responsesToolCompatibility) normalizeCustomTool(tool map[string]any, namespace, param string) ([]any, error) {
	name := strings.TrimSpace(stringField(tool, "name"))
	if name == "" {
		return nil, &responsesRequestError{Message: param + ".name 不能为空", Param: param + ".name", Code: "invalid_parameter"}
	}
	grammarNote := ""
	if format, exists := tool["format"]; exists && format != nil {
		formatObject, ok := format.(map[string]any)
		if !ok {
			return nil, &responsesRequestError{
				Message: param + ".format 必须是对象",
				Param:   param + ".format", Code: "invalid_parameter",
			}
		}
		formatType := strings.TrimSpace(stringField(formatObject, "type"))
		switch formatType {
		case "", "text":
			// Free-text custom tools map cleanly.
		case "grammar":
			// Codex Desktop often sends custom tools with a grammar (e.g. Lark/CFG).
			// Upstream Grok Build has no equivalent constrained decoder; degrade to
			// free-form string input and surface a stable compatibility warning.
			syntax := strings.TrimSpace(stringField(formatObject, "syntax"))
			definition := strings.TrimSpace(stringField(formatObject, "definition"))
			if definition == "" {
				// Some clients nest the grammar body under different keys.
				definition = strings.TrimSpace(stringField(formatObject, "grammar"))
			}
			grammarNote = "Original custom tool format was grammar"
			if syntax != "" {
				grammarNote += " (syntax=" + syntax + ")"
			}
			grammarNote += "; Grok Build cannot enforce the grammar, so the model must produce plain text in the input field."
			if definition != "" {
				// Cap description growth; keep a short hint of the intended shape.
				snippet := definition
				if len(snippet) > 400 {
					snippet = snippet[:400] + "…"
				}
				grammarNote += " Intended grammar (not enforced):\n" + snippet
			}
			c.addWarning("custom_tool_grammar_downgraded")
		default:
			// Unknown format types: still try free-text rather than hard-fail Codex sessions.
			grammarNote = fmt.Sprintf("Original custom tool format type %q is not supported by Grok Build; using free-text input.", formatType)
			c.addWarning("custom_tool_format_downgraded")
		}
	}
	identity := responsesToolIdentity{Kind: responsesCustomTool, Namespace: namespace, Name: name}
	description := strings.TrimSpace(stringField(tool, "description"))
	if description != "" {
		description += "\n"
	}
	description += "Provide the custom tool input in the input string field."
	if grammarNote != "" {
		description += "\n" + grammarNote
	}
	c.changed = true
	c.addWarning("custom_tool_emulated")
	return []any{map[string]any{
		"type": "function", "name": c.alias(identity), "description": description,
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"input": map[string]any{"type": "string"}},
			"required":   []any{"input"}, "additionalProperties": false,
		},
	}}, nil
}

func encodeCustomToolArguments(value any) (string, error) {
	input, ok := value.(string)
	if !ok {
		return "", &responsesRequestError{Message: "custom_tool_call.input 必须是字符串", Code: "invalid_parameter"}
	}
	encoded, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeCustomToolInput(value any) string {
	text, _ := value.(string)
	var wrapper map[string]any
	if json.Unmarshal([]byte(text), &wrapper) == nil {
		if input, ok := wrapper["input"].(string); ok {
			return input
		}
	}
	return text
}
