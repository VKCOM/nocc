package client

import (
	"context"

	"github.com/VKCOM/nocc/internal/common"
	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type GRPCClient struct {
	remoteHostPort string
	connection     *grpc.ClientConn
	callContext    context.Context
	cancelFunc     context.CancelFunc
	pb             pb.CompilationServiceClient
}

func MakeGRPCClient(remoteHostPort string) (*GRPCClient, error) {
	// this connection is non-blocking: it's created immediately
	// if the remote is not available, it will fail on request
	connection, err := grpc.Dial(
		remoteHostPort,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(),
		// Keepalive, so a router/NAT/conntrack table between client and server does not
		// silently evict an idle stream (which surfaces later as "connection reset by
		// peer" and drops the whole build to local compilation). Values are shared with
		// the server's gRPC options; see internal/common/keepalive.go.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                common.KeepaliveTime,
			Timeout:             common.KeepaliveTimeout,
			PermitWithoutStream: common.KeepalivePermitWithoutStream,
		}),
	)
	if err != nil {
		return nil, err
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	return &GRPCClient{
		remoteHostPort: remoteHostPort,
		connection:     connection,
		callContext:    ctx,
		cancelFunc:     cancelFunc,
		pb:             pb.NewCompilationServiceClient(connection),
	}, nil
}

func (grpcClient *GRPCClient) Clear() {
	if grpcClient.connection != nil {
		grpcClient.cancelFunc()
		_ = grpcClient.connection.Close()

		grpcClient.connection = nil
		grpcClient.callContext = nil
		grpcClient.cancelFunc = nil
		grpcClient.pb = nil
	}
}
