package apiserver

// CORS handling, replicating the CORSMiddleware that api.py registers:
//
//	app.add_middleware(CORSMiddleware,
//	    allow_origins=["*"], allow_credentials=True,
//	    allow_methods=["*"], allow_headers=["*"])
//
// Behaviour copied from starlette/middleware/cors.py (the version FastAPI
// ships with), for these specific settings:
//   allow_all_origins = true, allow_all_headers = true, allow_credentials =
//   true, so preflight_explicit_allow_origin = true.
//   - Preflight (OPTIONS + Origin + Access-Control-Request-Method) answers
//     200 "OK" with Vary, Access-Control-Allow-Origin: <request Origin>,
//     Access-Control-Allow-Methods (the "*" expansion), Access-Control-Max-Age:
//     600, Access-Control-Allow-Headers: <echo of requested headers> and
//     Access-Control-Allow-Credentials: true. A disallowed piece answers 400
//     plain-text "Disallowed CORS <parts>".
//   - Simple responses set the concrete origin (never "*", because credentials
//     are allowed) plus Access-Control-Allow-Credentials: true and Vary:
//     Origin, and only when the request carried an Origin header. Responses
//     without Origin just get Vary: Origin added.

import "net/http"

// corsAllowMethods is what CORSMiddleware(allow_methods=["*"]) expands to.
var corsAllowMethods = []string{"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT", "QUERY"}

// corsMaxAge mirrors CORSMiddleware's default max_age=600.
const corsMaxAge = "600"

// corsPreflightVary is starlette's preflight Vary value, verbatim.
const corsPreflightVary = "Origin, Access-Control-Request-Method, Access-Control-Request-Headers, Access-Control-Request-Private-Network"

type corsMiddleware struct{ next http.Handler }

func (c *corsMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")

	if r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != "" {
		c.preflight(w, r, origin)
		return
	}

	if origin != "" {
		// simple_headers + allow_explicit_origin(): allow_credentials forces
		// the concrete origin rather than "*".
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Add("Vary", "Origin")
	} else {
		w.Header().Add("Vary", "Origin")
	}
	c.next.ServeHTTP(w, r)
}

// preflight answers a CORS preflight request like CORSMiddleware does.
func (c *corsMiddleware) preflight(w http.ResponseWriter, r *http.Request, origin string) {
	h := w.Header()
	h.Add("Vary", corsPreflightVary)
	h.Set("Access-Control-Allow-Methods", joinComma(corsAllowMethods))
	h.Set("Access-Control-Max-Age", corsMaxAge)
	h.Set("Access-Control-Allow-Credentials", "true")

	var failures []string
	// is_allowed_origin(): allow_origins contains "*" -> every origin passes.
	h.Set("Access-Control-Allow-Origin", origin)
	if !methodAllowed(r.Header.Get("Access-Control-Request-Method")) {
		failures = append(failures, "method")
	}
	// allow_all_headers: mirror back whatever the client asked for.
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		h.Set("Access-Control-Allow-Headers", requested)
	}

	if len(failures) > 0 {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Disallowed CORS " + joinComma(failures)))
		return
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func methodAllowed(m string) bool {
	for _, a := range corsAllowMethods {
		if a == m {
			return true
		}
	}
	return false
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
