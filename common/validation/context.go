package validation

import "context"

type contextKey struct{}

func Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, true)
}

func Only(ctx context.Context) bool {
	return ctx.Value(contextKey{}) == true
}
