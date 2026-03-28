package rpcx

import (
	"net"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/discovery"
	"github.com/cloudwego/kitex/pkg/registry"
	"github.com/cloudwego/kitex/pkg/rpcinfo"
	"github.com/cloudwego/kitex/pkg/transmeta"
	"github.com/cloudwego/kitex/server"
	"github.com/cloudwego/kitex/transport"
	kitextracing "github.com/kitex-contrib/obs-opentelemetry/tracing"
)

const DefaultRPCTimeout = 10 * time.Second

func ClientOptions(resolver discovery.Resolver, extra ...client.Option) []client.Option {
	opts := []client.Option{
		client.WithResolver(resolver),
		client.WithRPCTimeout(DefaultRPCTimeout),
		client.WithTransportProtocol(transport.TTHeader),
		client.WithMetaHandler(transmeta.ClientTTHeaderHandler),
		client.WithSuite(kitextracing.NewClientSuite()),
	}
	return append(opts, extra...)
}

func ServerOptions(addr net.Addr, reg registry.Registry, serviceName string, extra ...server.Option) []server.Option {
	opts := []server.Option{
		server.WithServiceAddr(addr),
		server.WithRegistry(reg),
		server.WithServerBasicInfo(&rpcinfo.EndpointBasicInfo{ServiceName: serviceName}),
		server.WithMetaHandler(transmeta.ServerTTHeaderHandler),
		server.WithSuite(kitextracing.NewServerSuite()),
	}
	return append(opts, extra...)
}
