package credential

import (
	"log/slog"

	"home-os/mcp/internal/server"
)

// Register adds credential_find and credential_reveal (catalog v2, #4446).
// credential_reveal is always registered; without a valid
// AGENT_CREDENTIALS_KEY it answers that shared credentials can't be read
// right now and says where the user can see them.
func Register(srv *server.Server, deps server.Deps) {
	var key *AgentKey
	if deps.Config != nil {
		k, err := ParseAgentKey(deps.Config.AgentCredentialsKey)
		if err != nil {
			slog.Error("credential: agent credential sharing disabled", "error", err)
		}
		key = k
	}
	srv.RegisterTool(NewCredentialFindTool(deps.Pool))
	srv.RegisterTool(NewCredentialRevealTool(deps.Pool, key))
}
