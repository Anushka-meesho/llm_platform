package api

import (
	"bytes"
	"io"
	"net/http"

	"llm_platform_go/internal/schema"
)

// validateBody returns middleware that enforces the named request schema on the
// body before the handler runs. It reads the body to validate it, then restores
// it (io.NopCloser over the buffered bytes) so the downstream handler can decode
// as usual. A non-conforming body is rejected with 422 and never reaches the
// handler, so handler-side decode/validation becomes defense-in-depth.
func validateBody(reg *schema.Registry, name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body []byte
			if r.Body != nil {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					writeErr(w, r, Unprocessable(CodeInvalidBody, "could not read request body: %s", err.Error()))
					return
				}
				body = b
			}
			if err := reg.Validate(name, body); err != nil {
				writeErr(w, r, Unprocessable(CodeValidationFailed, "request validation failed: %s", err.Error()))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}
