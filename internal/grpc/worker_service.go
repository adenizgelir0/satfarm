package grpc

import (
	"io"
	"log"

	pb "satfarm/internal/grpc/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type WorkerService struct {
	pb.UnimplementedWorkerServiceServer
	srv *Server
}

func NewWorkerService(s *Server) *WorkerService {
	return &WorkerService{srv: s}
}

func (w *WorkerService) Work(stream pb.WorkerService_WorkServer) error {
	// Auth info comes from your interceptor (API token → user_id, etc.).
	auth, ok := GetAuth(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "no auth info")
	}

	// userID = owner of this worker (for accounting / penalties later)
	userID := auth.UserID

	// workerID = unique ID for THIS connection
	workerID := w.srv.registry.NewID()

	log.Printf("Worker conn=%d (user=%d) connected", workerID, userID)

	w.srv.registry.Set(workerID, &WorkerConn{
		Stream: stream,
		Info:   auth,
	})
	defer func() {
		w.srv.registry.Delete(workerID)
		w.srv.scheduler.HandleDisconnect(workerID)
		log.Printf("Worker conn=%d (user=%d) disconnected", workerID, userID)
	}()

	// Immediately try to assign work to this worker connection
	_ = w.srv.scheduler.MaybeAssignWork(workerID)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			log.Printf("recv error from worker conn=%d (user=%d): %v", workerID, userID, err)
			return err
		}

		switch m := msg.Msg.(type) {
		case *pb.WorkerMessage_Hello:
			log.Printf("Worker conn=%d (user=%d) hello: name=%s solver=%s",
				workerID, userID, m.Hello.WorkerName, m.Hello.SolverType)
			_ = w.srv.scheduler.MaybeAssignWork(workerID)

		case *pb.WorkerMessage_JobResult:
			if err := w.srv.scheduler.HandleJobResult(workerID, m.JobResult); err != nil {
				log.Printf("JobResult error from worker conn=%d (user=%d): %v",
					workerID, userID, err)
			}
			_ = w.srv.scheduler.MaybeAssignWork(workerID)

		default:
			log.Printf("worker conn=%d (user=%d) sent unknown msg", workerID, userID)
		}
	}
}
