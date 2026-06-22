// Package handlers — agentgw_adapter.go
//
// AgentGateway adapts the concrete *Registry (which owns the live agent
// Sessions) to the neutral agentgw.SessionResolver interface, so provider-side
// code (clusteraccess.AgentBacked, wired via providers.Registry) can resolve a
// zone's session WITHOUT importing package handlers — preserving the acyclic
// dependency graph (handlers → agentgw ← clusteraccess ← providers).
//
// The widening from *Session to agentgw.Session is free: *Session already
// satisfies agentgw.Session structurally (its Apply/Delete/GetStatus/
// WatchStatus signatures match). The only work is turning the concrete return
// of Registry.Session into the interface type.
package handlers

import "github.com/wso2/dc-api/internal/agentgw"

// AgentGateway adapts *Registry to agentgw.SessionResolver.
type AgentGateway struct{ reg *Registry }

// NewAgentGateway wraps the shared agent Registry so it can be injected into
// providers.NewRegistry as an agentgw.SessionResolver.
func NewAgentGateway(reg *Registry) *AgentGateway { return &AgentGateway{reg: reg} }

// Session returns the live Session for a zone as an agentgw.Session, or false if
// no agent is connected there.
func (g *AgentGateway) Session(region, zone string) (agentgw.Session, bool) {
	s, ok := g.reg.Session(region, zone)
	if !ok {
		return nil, false
	}
	return s, true // *Session satisfies agentgw.Session
}
