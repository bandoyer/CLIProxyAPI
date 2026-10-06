package auth

import (
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
)

// AttributeWebsockets names the attribute and metadata key that turns the
// upstream WebSocket transport on or off for one credential.
const AttributeWebsockets = "websockets"

// WebsocketsEnabled reports whether the credential may use the upstream
// WebSocket transport. An explicit flag in attributes or metadata wins.
// Without a flag, Codex credentials (not Codex API keys) default to enabled
// so a new Codex login is not passed over by the WebSocket preference in
// routing; every other provider, including xAI, stays opt-in.
func (a *Auth) WebsocketsEnabled() bool {
	if a == nil {
		return false
	}
	if enabled, ok := a.websocketsFlag(); ok {
		return enabled
	}
	return a.isCodexCredential()
}

// isCodexCredential reports whether the auth is a Codex login rather than a
// Codex API key.
func (a *Auth) isCodexCredential() bool {
	return a != nil && strings.EqualFold(strings.TrimSpace(a.Provider), "codex") && a.AuthKind() != AuthKindAPIKey
}

// codexWebsocketsTurnedOff reports whether a Codex credential has the
// upstream WebSocket transport turned off.
func codexWebsocketsTurnedOff(a *Auth) bool {
	return a.isCodexCredential() && !a.WebsocketsEnabled()
}

// warnCodexWebsocketsTurnedOff logs one warning per Codex credential with
// WebSockets off. WebSocket clients prefer WebSocket-enabled credentials, so
// in a mixed pool the transport, not quota urgency, chooses the credential.
func warnCodexWebsocketsTurnedOff(authIDs []string) {
	for _, authID := range authIDs {
		log.WithField("auth_id", authID).Warn("codex credential has upstream websockets turned off (websockets: false); websocket clients prefer websocket-enabled codex credentials, so in a mixed pool the transport, not quota urgency, chooses the credential")
	}
}

// websocketsFlag returns the explicit WebSocket flag and whether one is set.
// Attributes take precedence over metadata.
func (a *Auth) websocketsFlag() (bool, bool) {
	if a == nil {
		return false, false
	}
	if raw := authAttribute(a, AttributeWebsockets); raw != "" {
		if parsed, errParse := strconv.ParseBool(raw); errParse == nil {
			return parsed, true
		}
	}
	if a.Metadata == nil {
		return false, false
	}
	switch value := a.Metadata[AttributeWebsockets].(type) {
	case bool:
		return value, true
	case string:
		if parsed, errParse := strconv.ParseBool(strings.TrimSpace(value)); errParse == nil {
			return parsed, true
		}
	}
	return false, false
}
