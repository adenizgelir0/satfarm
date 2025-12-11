package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func validateToken(ctx context.Context, db *sql.DB, secret string) (int64, bool, error) {
	const q = `
        SELECT user_id
        FROM api_tokens
        WHERE secret = $1
          AND revoked = FALSE
    `
	var userID int64
	err := db.QueryRowContext(ctx, q, secret).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return userID, true, nil
}

func authenticateCtx(ctx context.Context, db *sql.DB) (*AuthInfo, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	vals := md.Get("authorization")
	if len(vals) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization")
	}

	const prefix = "Bearer "
	raw := vals[0]
	if !strings.HasPrefix(raw, prefix) {
		return nil, status.Error(codes.Unauthenticated, "invalid auth format")
	}

	secret := strings.TrimPrefix(raw, prefix)
	userID, ok2, err := validateToken(ctx, db, secret)
	if err != nil {
		return nil, status.Error(codes.Internal, "db error")
	}
	if !ok2 {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	return &AuthInfo{UserID: userID}, nil
}

func AuthUnaryInterceptor(db *sql.DB) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ai, err := authenticateCtx(ctx, db)
		if err != nil {
			return nil, err
		}
		return handler(WithAuth(ctx, ai), req)
	}
}

func AuthStreamInterceptor(db *sql.DB) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ai, err := authenticateCtx(ss.Context(), db)
		if err != nil {
			return err
		}

		wrapped := &wrappedStream{ServerStream: ss, ctx: WithAuth(ss.Context(), ai)}
		return handler(srv, wrapped)
	}
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
