package anthropicbridge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Helpers reimplemented for E2M. The behavior follows sub2api's originals,
// which encode hard-won knowledge about what OpenAI-compatible upstreams
// accept, minus three things E2M must not inherit:
//
//   - the system-prompt filter that silently dropped any text beginning with
//     "x-anthropic-billing-header: ", which is a sub2api transport convention
//     and would delete customer content here;
//   - the tool-input rewrite that special-cased a tool literally named "Read"
//     and deleted its empty "pages" field, which would silently mutate a
//     customer's tool arguments;
//   - the Responses-API content-part structs the originals passed around,
//     which E2M has no use for.

func boolPtr(v bool) *bool { return &v }

// rejectsSamplingParams reports whether an upstream model is known to reject
// temperature and top_p. OpenAI's reasoning models do. The test stays a narrow
// prefix match on purpose: guessing wrong silently strips sampling parameters
// the customer set, which is worse than forwarding them and letting a real
// upstream say no.
func rejectsSamplingParams(model string) bool {
	return strings.HasPrefix(model, "gpt-5")
}

// mapAnthropicEffort translates Anthropic's effort vocabulary to the Chat
// Completions one. Only "max" differs.
func mapAnthropicEffort(effort string) string {
	if effort == "max" {
		return "xhigh"
	}
	return effort
}

func bytesTrimSpace(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

// generateMessageID and generateBlockID name Anthropic documents in the
// protocol's own vocabulary. sub2api reused its Responses ids ("resp_", the
// item id) because its bridge ran through that layer; E2M does not.
func generateMessageID() string { return "msg_" + randomHex(12) }

func generateBlockID() string { return "block_" + randomHex(12) }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// normalizeCallID undoes the "fc_" prefixing some clients echo back, so a
// tool_result can be matched to the tool_use that announced it.
func normalizeCallID(id string) string {
	if after, ok := strings.CutPrefix(id, "fc_"); ok {
		if strings.HasPrefix(after, "toolu_") || strings.HasPrefix(after, "call_") {
			return after
		}
	}
	return id
}

// parseAnthropicSystemText accepts both encodings of the Anthropic system
// field — a bare string and an array of content blocks — and returns the text.
// Unlike sub2api's version it filters nothing: every text block the customer
// sent reaches the upstream.
func parseAnthropicSystemText(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func anthropicImageToDataURI(src *AnthropicImageSource) string {
	if src == nil || src.Data == "" {
		return ""
	}
	mediaType := src.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + src.Data
}

// convertToolResultOutput splits an Anthropic tool_result into the text that
// becomes the "tool" message content and any images, which Chat Completions
// cannot carry on a tool message and must be re-attached as user content.
func convertToolResultOutput(b AnthropicContentBlock) (string, []ChatContentPart) {
	if len(b.Content) == 0 {
		return "(empty)", nil
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		if s == "" {
			s = "(empty)"
		}
		return s, nil
	}
	var inner []AnthropicContentBlock
	if err := json.Unmarshal(b.Content, &inner); err != nil {
		return "(empty)", nil
	}
	var textParts []string
	var imageParts []ChatContentPart
	for _, ib := range inner {
		switch ib.Type {
		case "text":
			if ib.Text != "" {
				textParts = append(textParts, ib.Text)
			}
		case "image":
			if uri := anthropicImageToDataURI(ib.Source); uri != "" {
				imageParts = append(imageParts, ChatContentPart{
					Type: "image_url", ImageURL: &ChatImageURL{URL: uri},
				})
			}
		}
	}
	text := strings.Join(textParts, "\n\n")
	if text == "" {
		text = "(empty)"
	}
	return text, imageParts
}

func extractAnthropicTextFromBlocks(blocks []AnthropicContentBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

// normalizeToolParameters fills in the empty "properties" object that a
// JSON-Schema object type requires. Several upstreams reject a tool whose
// schema declares type object without it.
func normalizeToolParameters(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 || string(schema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(schema, &m); err != nil {
		return schema
	}
	if string(m["type"]) != `"object"` {
		return schema
	}
	if _, ok := m["properties"]; ok {
		return schema
	}
	m["properties"] = json.RawMessage(`{}`)
	out, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return out
}

func rawString(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

func chatMessageContentText(raw json.RawMessage) string {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var texts []string
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	return ""
}

func isBlankChatContent(raw json.RawMessage) bool {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return true
	}
	return chatMessageContentText(raw) == ""
}

// normalizeChatMessages repairs a tool-call history into the exact shape an
// OpenAI-compatible upstream accepts, which is stricter than what an Anthropic
// client may legitimately send (a truncated conversation can carry an
// unanswered tool_use, or a tool_result whose tool_use is gone):
//
//   - every tool reply is emitted immediately after the assistant message that
//     announced its id;
//   - an assistant's tool_calls are pruned to those that actually have a reply;
//     an assistant left with none keeps its text, or is dropped if it had none;
//   - an orphan tool reply, whose id no assistant announced, is dropped.
//
// Forwarding any of those unrepaired makes the upstream reject the request.
func normalizeChatMessages(messages []ChatMessage) []ChatMessage {
	replies := make(map[string]ChatMessage)
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			replies[m.ToolCallID] = m
		}
	}
	out := make([]ChatMessage, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "tool":
			// A tool message with no id is a plain Chat Completions
			// passthrough and keeps its place. One with an id is emitted
			// beside its assistant below, or dropped as an orphan.
			if m.ToolCallID == "" {
				out = append(out, m)
			}
		case len(m.ToolCalls) > 0:
			kept := make([]ChatToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, ok := replies[tc.ID]; ok {
					kept = append(kept, tc)
				}
			}
			if len(kept) == 0 {
				if isBlankChatContent(m.Content) {
					continue
				}
				m.ToolCalls = nil
				out = append(out, m)
				continue
			}
			m.ToolCalls = kept
			out = append(out, m)
			for _, tc := range kept {
				out = append(out, replies[tc.ID])
			}
		default:
			out = append(out, m)
		}
	}
	return out
}
