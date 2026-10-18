package websocket

import (
	"encoding/json"
	"fmt"
)

// Message is the envelope for all WebSocket messages sent to clients.
type Message struct {
	// Type identifies the event kind, e.g. "deployment.status", "instance.deleted".
	Type string `json:"type"`

	// Payload carries the event-specific data (typically the affected entity).
	Payload json.RawMessage `json:"payload"`
}

// NewMessage creates a Message with the given type and payload.
// The payload is JSON-marshalled; an error is returned if marshalling fails.
func NewMessage(msgType string, payload interface{}) (Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("marshal payload: %w", err)
	}
	return Message{
		Type:    msgType,
		Payload: data,
	}, nil
}

// Bytes serialises the Message to JSON bytes suitable for broadcasting.
func (m Message) Bytes() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return b, nil
}
