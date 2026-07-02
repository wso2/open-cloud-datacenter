// Package providers — shared error types.
package providers

import "github.com/wso2/dc-api/internal/providers/common"

// NotFoundError is the typed not-found sentinel drivers return when the
// backend object for a resource does not exist. Alias of common.NotFoundError
// (the concrete type lives in common because the drivers cannot import this
// package — factory.go imports them). Callers outside the driver tree should
// use this name.
type NotFoundError = common.NotFoundError
