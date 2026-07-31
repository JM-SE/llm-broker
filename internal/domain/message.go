package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MessageRole is the stable chat message role vocabulary. Only system,
// user, and assistant text messages are accepted; tool/function roles are
// out of scope for v1.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

var validRoles = map[MessageRole]bool{
	RoleSystem:    true,
	RoleUser:      true,
	RoleAssistant: true,
}

// Valid reports whether the role is accepted by the contract.
func (r MessageRole) Valid() bool { return validRoles[r] }

// Message is one chat message. Content is plain text; binary content,
// images, and tool payloads are out of scope for v1.
type Message struct {
	Role    MessageRole `json:"role"`
	Content string      `json:"content"`
}

// Validate checks role membership and content shape.
func (m Message) Validate() error {
	if !m.Role.Valid() {
		return fmt.Errorf("unsupported message role %q (want system, user, or assistant)", m.Role)
	}
	if !utf8.ValidString(m.Content) {
		return fmt.Errorf("message content is not valid UTF-8")
	}
	if strings.ContainsRune(m.Content, '\x00') {
		return fmt.Errorf("message content contains a NUL byte")
	}
	return nil
}
