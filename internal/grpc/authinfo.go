package grpc

import "context"

// AuthInfo represents authenticated worker identity.
type AuthInfo struct {
	UserID int64
}

type authKey struct{}

func WithAuth(ctx context.Context, ai *AuthInfo) context.Context {
	return context.WithValue(ctx, authKey{}, ai)
}

func GetAuth(ctx context.Context) (*AuthInfo, bool) {
	v := ctx.Value(authKey{})
	if v == nil {
		return nil, false
	}
	ai, ok := v.(*AuthInfo)
	return ai, ok
}
