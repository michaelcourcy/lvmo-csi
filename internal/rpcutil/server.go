// Package rpcutil bounds server work even when callers omit deadlines.
package rpcutil

import (
	"context"
	"google.golang.org/grpc"
	"time"
)

const MaxRequestTime = 5 * time.Minute

func Unary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, MaxRequestTime)
	defer cancel()
	return handler(ctx, req)
}

type stream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stream) Context() context.Context { return s.ctx }
func Stream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, cancel := context.WithTimeout(ss.Context(), MaxRequestTime)
	defer cancel()
	return handler(srv, stream{ServerStream: ss, ctx: ctx})
}
func Stop(s *grpc.Server) {
	done := make(chan struct{})
	go func() { s.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		s.Stop()
	}
}
