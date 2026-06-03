package httputil

import (
	"net/http"
	"net/url"
	"testing"
)

func TestApplyBasicAuthFromURL_WithUserAndPassword(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://alice:s3cret@example.com/file.mp3", nil)
	ApplyBasicAuthFromURL(req)

	if req.URL.User != nil {
		t.Error("URL.User should be nil after apply")
	}
	user, pass, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected BasicAuth to be set")
	}
	if user != "alice" || pass != "s3cret" {
		t.Errorf("got user=%q pass=%q, want alice / s3cret", user, pass)
	}
}

func TestApplyBasicAuthFromURL_UserOnly(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com/file.mp3", nil)
	req.URL.User = url.User("token-abc")
	ApplyBasicAuthFromURL(req)

	if req.URL.User != nil {
		t.Error("URL.User should be nil after apply")
	}
	user, pass, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected BasicAuth to be set")
	}
	if user != "token-abc" || pass != "" {
		t.Errorf("got user=%q pass=%q, want token-abc / empty", user, pass)
	}
}

func TestApplyBasicAuthFromURL_NoUserinfo(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com/file.mp3", nil)
	ApplyBasicAuthFromURL(req)

	if req.Header.Get("Authorization") != "" {
		t.Error("should not set Authorization when no userinfo")
	}
}

func TestApplyBasicAuthFromURL_SpecialCharacters(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://user%40domain:p%40ss%3Aword@example.com/f", nil)
	ApplyBasicAuthFromURL(req)

	user, pass, ok := req.BasicAuth()
	if !ok {
		t.Fatal("expected BasicAuth to be set")
	}
	if user != "user@domain" {
		t.Errorf("got user=%q, want user@domain", user)
	}
	if pass != "p@ss:word" {
		t.Errorf("got pass=%q, want p@ss:word", pass)
	}
}
