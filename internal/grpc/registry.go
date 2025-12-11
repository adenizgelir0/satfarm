package grpc

import (
	pb "satfarm/internal/grpc/proto"
	"sync"
)

type WorkerConn struct {
	Stream pb.WorkerService_WorkServer
	Info   *AuthInfo
}

type WorkerRegistry struct {
	mu     sync.Mutex
	data   map[int64]*WorkerConn
	nextID int64
}

func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		data: make(map[int64]*WorkerConn),
	}
}

// NewID allocates a fresh worker ID for each new connection.
// This is independent of user_id so a single user can run multiple workers.
func (r *WorkerRegistry) NewID() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	return r.nextID
}

func (r *WorkerRegistry) Set(id int64, conn *WorkerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[id] = conn
}

func (r *WorkerRegistry) Get(id int64) (*WorkerConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.data[id]
	return c, ok
}

func (r *WorkerRegistry) Delete(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, id)
}

func (r *WorkerRegistry) IDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0, len(r.data))
	for id := range r.data {
		ids = append(ids, id)
	}
	return ids
}
