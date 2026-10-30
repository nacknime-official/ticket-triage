// Package logging builds the process logger: log/slog JSON with the level
// from config and the redaction rules of PRD §4.1 applied to every
// attribute, including attributes added with Logger.With and inside groups.
//
// Redaction rules. Access tokens, stream tokens, the Authorization header,
// and ticket subject and body never reach the logs:
//
//   - An attribute is replaced with "[REDACTED]" when its key, or the name of
//     any group containing it, is sensitive. Keys are matched without regard
//     to case, with "-" read as "_": authorization, proxy_authorization,
//     cookie, set_cookie, token, subject, body, password, secret, api_key,
//     database_url, and any key ending in _token, _subject, _body, _password,
//     _secret or _api_key.
//   - A map with string keys (http.Header, url.Values, map[string]any) is
//     logged with the values of its sensitive keys replaced.
//   - A string value that starts with "Bearer " or contains "token=" is
//     replaced, which catches a raw Authorization header or a raw URL carrying
//     ?token= under any key.
//
// The rules work on keys and string values. Error text, ticket text in a
// message, and the fields of structs the handler marshals itself are not
// recognized, so callers must still never put secrets or ticket text in a
// message or an error, never log a raw URL (log r.Pattern), and give types
// that hold a secret a LogValue method that hides it, as config.Secret does.
//
// Import boundaries: standard library only.
package logging
