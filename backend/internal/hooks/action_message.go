package hooks

import (
	"encoding/json"
	"regexp"
	"strings"
)

// actionMessageURL matches a URL in a subscriber message; oneLineMessage
// replaces it with "[url]".
var actionMessageURL = regexp.MustCompile(`https?://\S+`)

// actionMessageKeys are the fields of a subscriber answer that can hold its
// message, in the order of preference.
var actionMessageKeys = []string{"message", "error", "detail", "reason"}

// ActionResultMessage returns the message of a refusal (status 400 or
// higher), so a client can show why the subscriber refused (for example 409
// "refresh already in flight"). It reads a JSON string body, or the first
// string field of message, error, detail and reason (also error.message).
// The message is one line of at most 500 characters, with URLs replaced by
// "[url]". It returns "" for a status below 400 or when the body has no
// message.
func ActionResultMessage(statusCode int, body json.RawMessage) string {
	if statusCode < 400 {
		return ""
	}
	var text string
	if json.Unmarshal(body, &text) == nil {
		return oneLineMessage(text)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	for _, key := range actionMessageKeys {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
			return oneLineMessage(text)
		}
		// {"error": {"message": "..."}}
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &nested) == nil && strings.TrimSpace(nested.Message) != "" {
			return oneLineMessage(nested.Message)
		}
	}
	return ""
}

// plainTextActionBody returns a non-JSON body of a refusal (status 400 or
// higher) as a JSON string (one line, at most 500 characters, URLs replaced)
// when the subscriber sent plain text, so its reason reaches the client.
// Other bodies (for example an HTML error page of a proxy) give "null".
func plainTextActionBody(contentType string, body []byte) json.RawMessage {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/plain") {
		return json.RawMessage("null")
	}
	text := oneLineMessage(string(body))
	if text == "" {
		return json.RawMessage("null")
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return json.RawMessage("null")
	}
	return encoded
}

// oneLineMessage returns msg as one line of at most 500 characters with
// URLs replaced by "[url]", or "" for a blank message.
func oneLineMessage(msg string) string {
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return sanitizeDenyMessage(actionMessageURL.ReplaceAllString(msg, "[url]"))
}
