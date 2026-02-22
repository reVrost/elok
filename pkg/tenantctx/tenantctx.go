package tenantctx

import (
	"context"
	"strings"
)

const DefaultTenantID = "default"

type contextKey string

const tenantKey contextKey = "tenant_id"

func Normalize(tenantID string) string {
	normalized := strings.TrimSpace(tenantID)
	if normalized == "" {
		return DefaultTenantID
	}
	return normalized
}

func WithTenantID(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, tenantKey, Normalize(tenantID))
}

func TenantID(ctx context.Context) string {
	if ctx == nil {
		return DefaultTenantID
	}
	value, _ := ctx.Value(tenantKey).(string)
	return Normalize(value)
}
