package client

import (
	"context"

	"github.com/VKCOM/nocc/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

// Clear closes the connection. The fields stay set: stream goroutines of a replaced connection may still
// be running, and they find out they're done by calls failing and callContext being canceled, see IsCleared.
func (grpcClient *GRPCClient) Clear() {
	if grpcClient != nil {
		grpcClient.cancelFunc()
		_ = grpcClient.connection.Close()
	}
}

func (grpcClient *GRPCClient) IsCleared() bool {
	return grpcClient.callContext.Err() != nil
}
