// FluxSpace Hub is an independent Go module. It must not import the root
// eigenflux module (AGENTS.md, independent-module convention used by console/
// and cli/).
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"fluxspace.ai/hub/api"
	"fluxspace.ai/hub/followup"
	"fluxspace.ai/hub/invitation"
	"fluxspace.ai/hub/services"
	"fluxspace.ai/hub/session"
)

// gatewayPort builds the real EigenFlux HTTP client only when the operator has
// supplied a gateway URL. Without one, negotiation send/poll and follow-up
// delivery return an explicit 503; nothing is silently stubbed.
//
// HUB_AGENT_TOKENS is a JSON object mapping Agent ID to access token, e.g.
// {"agent_1":"efv2a_...","hub_agent":"efv2a_..."}. Access tokens expire in 15
// minutes; the client pushes rotated tokens in via GatewayPort.SetAccessToken
// or injects a TokenRefresher. The Hub never holds refresh tokens or keys.
func gatewayPort() services.EigenFluxPort {
	base := os.Getenv("HUB_GATEWAY_URL")
	if base == "" {
		log.Printf("HUB_GATEWAY_URL unset: PM send/poll and follow-up delivery are disabled (explicit 503)")
		return nil
	}
	tokens := map[string]string{}
	if raw := os.Getenv("HUB_AGENT_TOKENS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
			log.Fatalf("HUB_AGENT_TOKENS is not a JSON string map: %v", err)
		}
	}
	log.Printf("EigenFlux gateway configured at %s with %d Agent token(s)", base, len(tokens))
	return services.NewGatewayPort(base, tokens)
}

func main() {
	addr := os.Getenv("HUB_ADDR")
	if addr == "" {
		// 8095 keeps clear of EigenFlux gateway 8080, ws 8088, console 8090
		// and the existing offline replay service 8092.
		addr = ":8095"
	}
	port := gatewayPort()
	srv := &api.Server{
		Manager:        session.NewManager(),
		SpectatorToken: os.Getenv("HUB_SPECTATOR_TOKEN"),
		Invitations:    invitation.NewManager(),
		Port:           port,
	}
	if port != nil {
		srv.FollowUp = &followup.Sender{
			Port: port, HubAgentID: os.Getenv("HUB_AGENT_ID"),
		}
	}

	mux := http.NewServeMux()
	api.RegisterHealth(mux)
	api.RegisterSessions(mux, srv)
	api.RegisterInvitations(mux, srv)
	api.RegisterFollowUps(mux, srv)
	log.Printf("FluxSpace Hub listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
