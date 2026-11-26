package main

import (
	"context"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
)

func TestFrozenDiscoveryResponseOmitsDeletedAgent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE agents(agent_id INTEGER PRIMARY KEY,identity_state TEXT); INSERT INTO agents VALUES(7,'deleted'),(8,'active')").Error)
	retained := []discovery.ResultItem{
		{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 7}},
		{Ref: discovery.SourceRef{Type: discovery.Agent, ID: 8}},
	}
	page := discovery.Response{NextCursor: "next", HasMore: true, Items: []discovery.ResultItem{
		retained[0], {Ref: discovery.SourceRef{Type: discovery.Agent, ID: 7}, Preview: map[string]string{"text": "prior name"}}, retained[1],
	}}
	require.NoError(t, filterActiveDiscoveryAgents(context.Background(), db, &page))
	require.Equal(t, retained, page.Items)
	require.Equal(t, "next", page.NextCursor)
	require.True(t, page.HasMore)
	require.NoError(t, db.Exec("DROP TABLE agents").Error)
	require.Error(t, filterActiveDiscoveryAgents(context.Background(), db, &page))
}
