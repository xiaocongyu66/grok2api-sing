package promptcache

import (
	"encoding/json"
	"strings"
)

// ConversationSeedFromChatBody extracts a stable seed from OpenAI chat messages JSON.
func ConversationSeedFromChatBody(body []byte) string {
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	system := ""
	messages := make([]MessageSeed, 0, len(payload.Messages))
	for _, message := range payload.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		text := extractSeedContent(message.Content)
		if role == "system" || role == "developer" {
			if system != "" {
				system += "\n"
			}
			system += text
			continue
		}
		messages = append(messages, MessageSeed{Role: role, Text: text})
	}
	return ConversationSeedFromMessages(system, messages)
}

// ConversationSeedFromMessagesBody extracts a seed from Anthropic messages JSON.
func ConversationSeedFromMessagesBody(body []byte) string {
	var payload struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	system := extractSeedContent(payload.System)
	messages := make([]MessageSeed, 0, len(payload.Messages))
	for _, message := range payload.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		text := extractSeedContent(message.Content)
		if role == "system" || role == "developer" {
			if system != "" {
				system += "\n"
			}
			system += text
			continue
		}
		messages = append(messages, MessageSeed{Role: role, Text: text})
	}
	return ConversationSeedFromMessages(system, messages)
}

// ConversationSeedFromResponsesBody extracts a seed from Responses API input.
func ConversationSeedFromResponsesBody(body []byte) string {
	var payload struct {
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	system := compressSeedText(payload.Instructions)
	// input may be a string or an array of items.
	if len(payload.Input) == 0 {
		return ConversationSeedFromMessages(system, nil)
	}
	var asString string
	if json.Unmarshal(payload.Input, &asString) == nil {
		return ConversationSeedFromMessages(system, []MessageSeed{{Role: "user", Text: asString}})
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(payload.Input, &items) != nil {
		return ConversationSeedFromMessages(system, nil)
	}
	messages := make([]MessageSeed, 0, len(items))
	for _, item := range items {
		role := ""
		_ = json.Unmarshal(item["role"], &role)
		typeName := ""
		_ = json.Unmarshal(item["type"], &typeName)
		text := extractSeedContent(item["content"])
		if text == "" {
			var content string
			_ = json.Unmarshal(item["content"], &content)
			text = content
		}
		if text == "" {
			var inputText string
			_ = json.Unmarshal(item["text"], &inputText)
			text = inputText
		}
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "" && (typeName == "message" || typeName == "input_text") {
			role = "user"
		}
		if role == "system" || role == "developer" {
			if system != "" {
				system += "\n"
			}
			system += compressSeedText(text)
			continue
		}
		if role == "" {
			role = "user"
		}
		messages = append(messages, MessageSeed{Role: role, Text: text})
	}
	return ConversationSeedFromMessages(system, messages)
}

func extractSeedContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return compressSeedText(asString)
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		var builder strings.Builder
		for _, part := range parts {
			var typ string
			_ = json.Unmarshal(part["type"], &typ)
			if typ != "" && typ != "text" && typ != "input_text" && typ != "output_text" {
				continue
			}
			var text string
			if json.Unmarshal(part["text"], &text) == nil && text != "" {
				if builder.Len() > 0 {
					builder.WriteByte(' ')
				}
				builder.WriteString(text)
				continue
			}
			// Anthropic: {"type":"text","text":"..."} already handled; also content string fields.
		}
		return compressSeedText(builder.String())
	}
	return ""
}
