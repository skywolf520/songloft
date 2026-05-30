package app

import "testing"

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"/", "", false},
		{"/songloft", "/songloft", false},
		{"/songloft/", "/songloft", false},
		{"songloft", "/songloft", false},
		{"/a/b/c", "/a/b/c", false},
		{"/a/b/c/", "/a/b/c", false},
		{"  /songloft  ", "/songloft", false},
		{"/path?foo", "", true},
		{"/path#bar", "", true},
		{"/path/../etc", "", true},
	}

	for _, tt := range tests {
		got, err := normalizeBasePath(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("normalizeBasePath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
