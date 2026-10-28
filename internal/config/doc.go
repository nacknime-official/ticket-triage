// Package config loads process configuration from environment variables into
// a typed struct, applies defaults, and validates it, failing with an error
// that names the offending variable.
//
// Import boundaries: standard library only. It must not import any other
// internal package; cmd/* passes the values it needs to each component.
package config
