package utils

// ContextKey is the type used for context value keys to avoid collisions.
type ContextKey string

// ClientIDKey is the context key for the authenticated client's GUID.
const ClientIDKey ContextKey = "client_id"
