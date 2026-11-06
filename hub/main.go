// FluxSpace Hub is an independent Go module. It must not import the root
// eigenflux module (AGENTS.md, independent-module convention used by console/
// and cli/).
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"fluxspace.ai/hub/api"
	"fluxspace.ai/hub/archive"
	"fluxspace.ai/hub/followup"
	"fluxspace.ai/hub/govern"
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

// cardProjectionFields reads the opt-in Agent Card projection list. Empty is
// the default and means the Hub never writes to an owner's public card; when it
// is set, only the blank-filling descriptor fields survive the whitelist in
// services.ProjectAgentCardFields.
//
// HUB_CARD_PROJECTION_FIELDS=agent_description,human_description
func cardProjectionFields() []string {
	raw := strings.TrimSpace(os.Getenv("HUB_CARD_PROJECTION_FIELDS"))
	if raw == "" {
		log.Printf("HUB_CARD_PROJECTION_FIELDS unset: post-match Agent Card projection is disabled")
		return nil
	}
	parts := strings.Split(raw, ",")
	fields := make([]string, 0, len(parts))
	for _, part := range parts {
		if field := strings.TrimSpace(part); field != "" {
			fields = append(fields, field)
		}
	}
	log.Printf("post-match Agent Card projection enabled for %v (blank public fields only)", fields)
	return fields
}

// governanceComponents opens the S5 governance state, or returns memory-only
// components when HUB_GOVERNANCE_DIR is unset.
//
// A load failure is fatal, not downgraded to memory mode: silently forgetting
// permissions, spend and the audit chain because a file would not parse is the
// one failure a governance store must not have (ADR-0008).
func governanceComponents() *govern.Components {
	dir := strings.TrimSpace(os.Getenv("HUB_GOVERNANCE_DIR"))
	components, err := govern.Open(dir)
	if err != nil {
		log.Fatalf("governance state refused: %v", err)
	}
	if note := components.LoadNote(); note != nil {
		log.Printf("governance state loaded with a note: %v", note)
	}
	if components.Store == nil {
		log.Printf("HUB_GOVERNANCE_DIR unset: permissions, budget and audit are memory-only and reset on restart")
	} else {
		log.Printf("governance state persisted under %s", components.Store.Dir())
	}
	return components
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
		Archives:       archive.NewStore(),
		Governance:     &api.Governance{Components: governanceComponents()},
		Port:           port,
	}
	if port != nil {
		srv.FollowUp = &followup.Sender{
			Port: port, HubAgentID: os.Getenv("HUB_AGENT_ID"),
			CardFields: cardProjectionFields(),
		}
	}

	mux := http.NewServeMux()
	api.RegisterHealth(mux)
	api.RegisterSessions(mux, srv)
	api.RegisterInvitations(mux, srv)
	api.RegisterFollowUps(mux, srv)
	api.RegisterArchives(mux, srv)
	api.RegisterGovernance(mux, srv)
	log.Printf("FluxSpace Hub listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
