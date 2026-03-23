package clients

import (
	"eigenflux_server/kitex_gen/eigenflux/auth/authservice"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/item/itemservice"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/kitex_gen/eigenflux/profile/profileservice"
)

var (
	ProfileClient profileservice.Client
	ItemClient    itemservice.Client
	FeedClient    feedservice.Client
	AuthClient    authservice.Client
	PMClient      pmservice.Client
)
