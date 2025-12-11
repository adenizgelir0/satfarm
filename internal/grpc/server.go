package grpc

import (
	"database/sql"
	"net"
	"time"

	pb "satfarm/internal/grpc/proto"

	stdgrpc "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type Server struct {
	db        *sql.DB
	grpc      *stdgrpc.Server
	registry  *WorkerRegistry
	scheduler JobScheduler
}

func NewGRPCServer(db *sql.DB) *Server {
	reg := NewWorkerRegistry()
	sched := NewSimpleScheduler(db, reg)

	s := &Server{
		db:        db,
		registry:  reg,
		scheduler: sched,
	}

	// gRPC keepalive config: internal service, long-lived streams
	kaep := keepalive.EnforcementPolicy{
		MinTime:             10 * time.Second, // min time between client pings
		PermitWithoutStream: true,             // allow pings with no active RPC
	}

	kasp := keepalive.ServerParameters{
		MaxConnectionIdle:     0,
		MaxConnectionAge:      0,
		MaxConnectionAgeGrace: 0,
		Time:                  2 * time.Minute, // server pings if idle for 2m
		Timeout:               20 * time.Second,
	}

	s.grpc = stdgrpc.NewServer(
		stdgrpc.ChainUnaryInterceptor(AuthUnaryInterceptor(db)),
		stdgrpc.ChainStreamInterceptor(AuthStreamInterceptor(db)),
		stdgrpc.KeepaliveEnforcementPolicy(kaep),
		stdgrpc.KeepaliveParams(kasp),
	)

	pb.RegisterWorkerServiceServer(s.grpc, NewWorkerService(s))

	return s
}

// Serve listens and serves the gRPC worker service.
func (s *Server) Serve(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.grpc.Serve(lis)
}

func (s *Server) Stop() {
	s.grpc.GracefulStop()
}

// Expose the scheduler so the web layer can enqueue jobs:
func (s *Server) Scheduler() JobScheduler {
	return s.scheduler
}
