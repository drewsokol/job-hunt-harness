package store

import (
	"strings"
	"unicode"
)

// Actor records who made a change: the user (through the UI), jhh itself,
// or an agent identified by the name its MCP client sent when it connected.
type Actor string

const (
	ActorUser   Actor = "user"
	ActorSystem Actor = "system"
)

// maxAgentName caps the client-supplied part of an agent actor.
const maxAgentName = 64

// Agent returns the actor for an MCP client. The name comes from the client,
// so it is cleaned: control characters are dropped and the length is capped.
func Agent(clientName string) Actor {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(clientName))
	if r := []rune(name); len(r) > maxAgentName {
		name = string(r[:maxAgentName])
	}
	if name == "" {
		name = "unknown"
	}
	return Actor("agent:" + name)
}
