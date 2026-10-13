// Package keys is the registry for cache Redis wire keys. Constants are shared
// by readers, writers and invalidators. Changes require a migration decision.
package keys

// Legacy recommendation/profile caches (pkg/cache).
const (
	Search              = "cache:search:%s:%d:%d"
	Profile             = "cache:profile:%d"
	Embedding           = "cache:profile:emb:%d"
	ItemStats           = "cache:item_stats:%d"
	AgentInfluence      = "cache:agent_influence:%d"
	FeedPrefix          = "feed:cache:"
	Feed                = FeedPrefix + "%d"
	DiscoveryGeneration = "cache:discovery:v1:{%d}:generation"
	DiscoveryValue      = "cache:discovery:v1:{%d}:%s:%s:%s"
)

// Private search protection (pkg/searchguard, gateway and domain RPCs).
const (
	SearchRate   = "search:rate:v1:%s:%d"
	SearchResult = "search:cache:v1:%s:%d:%s"
)

// PM projections and relationships (rpc/pm).
const (
	PMItemOwner       = "pm:itemowner:%d"
	PMItemResponse    = "pm:itemresp:%d"
	PMConversation    = "pm:conv:%d"
	PMConversationMap = "pm:convmap:%d:%d:%d"
	PMFetch           = "pm:fetch:%d"
	Friends           = "friend:%d"
	Blocks            = "block:%d"
	FriendCount       = "friend_count:%d"
)

// Gateway, auth and pipeline caches.
const (
	EmailToUID        = "cache:email2uid:"
	BeatSignals       = "cache:beat_signals:"
	Blacklist         = "cache:blacklist:keywords"
	AuthVerify        = "auth:verify:result:"
	AuthSession       = "auth:session:"
	HomeDiscovery     = "console:v2:home:discovery:"
	HomeActivity      = "console:v2:home:activity:v2"
	HomeWorthWatching = "console:v2:home:worth-watching:"
)

// Frozen delivery pages, query embeddings and associated leases.
const (
	DeliveryResponse = "discovery:serve:%d:%x"
	DeliveryPage     = "discovery:feed:%d:page"
	DeliverySearch   = "discovery:search:%d:%s"
	NeedEmbedding    = "discovery:need_embedding:v1:"
	LockSuffix       = ":lock"
)
