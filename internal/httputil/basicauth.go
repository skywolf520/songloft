package httputil

import "net/http"

// ApplyBasicAuthFromURL extracts userinfo from req.URL, sets the Authorization
// header, and clears req.URL.User to prevent credential leakage in logs.
// No-op when the URL has no userinfo.
func ApplyBasicAuthFromURL(req *http.Request) {
	if req.URL.User != nil {
		password, _ := req.URL.User.Password()
		req.SetBasicAuth(req.URL.User.Username(), password)
		req.URL.User = nil
	}
}
