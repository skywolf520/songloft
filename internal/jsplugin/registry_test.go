package jsplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.10.0", "1.9.0", 1},
		{"2026.6.2", "2026.6.1", 1},
		{"2026.6.2", "2026.6.2", 0},
		{"2025.12.1", "2026.1.1", -1},
		{"1.0", "1.0.0", 0},
		{"1.0.1", "1.0", 1},
	}
	for _, tt := range tests {
		got := compareVersion(tt.a, tt.b)
		if (tt.want > 0 && got <= 0) || (tt.want < 0 && got >= 0) || (tt.want == 0 && got != 0) {
			t.Errorf("compareVersion(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// servePluginJSON 返回一个处理函数，提供带 download_url 的 plugin.json。
func servePluginJSON(name, entryPath, version, downloadURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		manifest := PluginManifest{
			Name:        name,
			EntryPath:   entryPath,
			Version:     version,
			DownloadURL: downloadURL,
		}
		json.NewEncoder(w).Encode(manifest)
	}
}

func TestFetchAndMerge_Basic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/a/plugin.json", servePluginJSON("Plugin A", "plugin-a", "1.0.0", "https://example.com/a.zip"))
	mux.HandleFunc("/b/plugin.json", servePluginJSON("Plugin B", "plugin-b", "2.0.0", "https://example.com/b.zip"))
	pluginSrv := httptest.NewServer(mux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Name: "Test Registry",
		Plugins: []string{
			pluginSrv.URL + "/a/plugin.json",
			pluginSrv.URL + "/b/plugin.json",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(plugins))
	}
}

func TestFetchAndMerge_Dedup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/plugin.json", servePluginJSON("Plugin A v1", "plugin-a", "1.0.0", "https://example.com/a1.zip"))
	mux.HandleFunc("/v2/plugin.json", servePluginJSON("Plugin A v2", "plugin-a", "2.0.0", "https://example.com/a2.zip"))
	pluginSrv := httptest.NewServer(mux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{
			pluginSrv.URL + "/v1/plugin.json",
			pluginSrv.URL + "/v2/plugin.json",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, _, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin after dedup, got %d", len(plugins))
	}
	if plugins[0].Version != "2.0.0" {
		t.Errorf("expected version 2.0.0, got %s", plugins[0].Version)
	}
}

func TestFetchAndMerge_Includes(t *testing.T) {
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", servePluginJSON("Plugin A", "plugin-a", "1.0.0", "https://example.com/a.zip"))
	pluginMux.HandleFunc("/b/plugin.json", servePluginJSON("Plugin B", "plugin-b", "1.0.0", "https://example.com/b.zip"))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/main.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Name:     "Main",
			Includes: []string{srv.URL + "/sub.json"},
			Plugins:  []string{pluginSrv.URL + "/a/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})
	mux.HandleFunc("/sub.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Name:    "Sub",
			Plugins: []string{pluginSrv.URL + "/b/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL+"/main.json", "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins from main + sub, got %d", len(plugins))
	}
}

func TestFetchAndMerge_IncludesDedup(t *testing.T) {
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/v1/plugin.json", servePluginJSON("Plugin A v1", "plugin-a", "1.0.0", "https://example.com/a1.zip"))
	pluginMux.HandleFunc("/v2/plugin.json", servePluginJSON("Plugin A v2", "plugin-a", "2.0.0", "https://example.com/a2.zip"))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/main.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Includes: []string{srv.URL + "/sub.json"},
			Plugins:  []string{pluginSrv.URL + "/v1/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})
	mux.HandleFunc("/sub.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Plugins: []string{pluginSrv.URL + "/v2/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, _, err := svc.FetchAndMerge(context.Background(), srv.URL+"/main.json", "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin after cross-registry dedup, got %d", len(plugins))
	}
	if plugins[0].Version != "2.0.0" {
		t.Errorf("expected higher version 2.0.0, got %s", plugins[0].Version)
	}
}

func TestFetchAndMerge_CycleDetection(t *testing.T) {
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", servePluginJSON("A", "a", "1.0.0", "https://example.com/a.zip"))
	pluginMux.HandleFunc("/b/plugin.json", servePluginJSON("B", "b", "1.0.0", "https://example.com/b.zip"))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/a.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Includes: []string{srv.URL + "/b.json"},
			Plugins:  []string{pluginSrv.URL + "/a/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})
	mux.HandleFunc("/b.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Includes: []string{srv.URL + "/a.json"},
			Plugins:  []string{pluginSrv.URL + "/b/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, _, err := svc.FetchAndMerge(context.Background(), srv.URL+"/a.json", "", "")
	if err != nil {
		t.Fatalf("cycle should not cause error, got: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins (cycle resolved by visited set), got %d", len(plugins))
	}
}

func TestFetchAndMerge_DepthLimit(t *testing.T) {
	const chainLen = 25 // 超过 registryMaxDepth=20

	pluginMux := http.NewServeMux()
	for i := range chainLen {
		name := fmt.Sprintf("P%d", i)
		pluginMux.HandleFunc(fmt.Sprintf("/%d/plugin.json", i), servePluginJSON(name, fmt.Sprintf("p%d", i), "1.0.0", "https://example.com/p.zip"))
	}
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	for i := range chainLen {
		depth := i
		path := fmt.Sprintf("/%d.json", depth)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			registry := RegistryJSON{
				Plugins: []string{fmt.Sprintf("%s/%d/plugin.json", pluginSrv.URL, depth)},
			}
			if depth < chainLen-1 {
				registry.Includes = []string{fmt.Sprintf("%s/%d.json", srv.URL, depth+1)}
			}
			json.NewEncoder(w).Encode(registry)
		})
	}

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL+"/0.json", "", "")
	if err != nil {
		t.Fatalf("depth limit should not cause error, got: %v", err)
	}
	// depth 0..20 共 21 层可达，depth 21+ 超限
	if len(plugins) > registryMaxDepth+1 {
		t.Errorf("expected at most %d plugins within depth limit, got %d", registryMaxDepth+1, len(plugins))
	}
	if len(warnings) == 0 {
		t.Error("expected depth limit warning")
	}
}

func TestFetchAndMerge_PluginFetchFailureWarning(t *testing.T) {
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/bad/plugin.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	pluginMux.HandleFunc("/good/plugin.json", servePluginJSON("Good Plugin", "good", "1.0.0", "https://example.com/good.zip"))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{
			pluginSrv.URL + "/bad/plugin.json",
			pluginSrv.URL + "/good/plugin.json",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected warning about failed plugin.json fetch")
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 valid plugin, got %d", len(plugins))
	}
	if plugins[0].EntryPath != "good" {
		t.Errorf("expected 'good' plugin, got %q", plugins[0].EntryPath)
	}
}

func TestFetchAndMerge_IncludeFailureWarning(t *testing.T) {
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", servePluginJSON("A", "a", "1.0.0", "https://example.com/a.zip"))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/main.json", func(w http.ResponseWriter, r *http.Request) {
		registry := RegistryJSON{
			Includes: []string{srv.URL + "/nonexistent.json"},
			Plugins:  []string{pluginSrv.URL + "/a/plugin.json"},
		}
		json.NewEncoder(w).Encode(registry)
	})
	mux.HandleFunc("/nonexistent.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL+"/main.json", "", "")
	if err != nil {
		t.Fatalf("include failure should not cause error, got: %v", err)
	}
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin, got %d", len(plugins))
	}
	if len(warnings) == 0 {
		t.Error("expected warning about failed include")
	}
}

func TestFetchAndMerge_RootFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	_, _, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("expected error for root registry failure")
	}
}

func TestFetchAndMerge_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	svc := NewRegistryService()
	_, _, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFetchAndMerge_ChainFetchManifest(t *testing.T) {
	mux := http.NewServeMux()

	// plugin.json 没有 download_url，但有 updateUrl 指向 manifest.json
	mux.HandleFunc("/plugin.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"name": "Chain Plugin",
			"entryPath": "chain-plugin",
			"version": "2.0.0",
			"description": "Plugin with chained manifest",
			"author": "Test",
			"updateUrl": "` + "http://" + r.Host + `/manifest.json"
		}`))
	})
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"2.0.0","download_url":"https://example.com/chain-plugin.zip"}`))
	})

	pluginSrv := httptest.NewServer(mux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{pluginSrv.URL + "/plugin.json"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, _, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].DownloadURL != "https://example.com/chain-plugin.zip" {
		t.Errorf("expected download_url from manifest.json, got %q", plugins[0].DownloadURL)
	}
}

func TestFetchAndMerge_NoDownloadURL_Warning(t *testing.T) {
	mux := http.NewServeMux()
	// plugin.json 既没有 download_url 也没有 updateUrl
	mux.HandleFunc("/plugin.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"name": "No Download Plugin",
			"entryPath": "no-download",
			"version": "1.0.0",
			"description": "Plugin without download_url or updateUrl",
			"author": "Test"
		}`))
	})
	pluginSrv := httptest.NewServer(mux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{pluginSrv.URL + "/plugin.json"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (no download_url), got %d", len(plugins))
	}
	if len(warnings) == 0 {
		t.Error("expected warning about missing download_url")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "no-download") && strings.Contains(w, "download_url") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning mentioning 'no-download' and 'download_url', got %v", warnings)
	}
}

func TestFetchAndMerge_ChainFetchFailure_Warning(t *testing.T) {
	mux := http.NewServeMux()
	// plugin.json 有 updateUrl 但 manifest.json 返回 404
	mux.HandleFunc("/plugin.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"name": "Chain Fail Plugin",
			"entryPath": "chain-fail",
			"version": "1.0.0",
			"description": "Plugin with broken updateUrl",
			"author": "Test",
			"updateUrl": "` + "http://" + r.Host + `/manifest.json"
		}`))
	})
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	pluginSrv := httptest.NewServer(mux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{pluginSrv.URL + "/plugin.json"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins (chain fetch failed), got %d", len(plugins))
	}
	if len(warnings) == 0 {
		t.Error("expected warning about missing download_url after chain fetch failure")
	}
}

func TestFetchAndMerge_WithToken(t *testing.T) {
	const testToken = "ghp_test123"

	requireAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", requireAuth(servePluginJSON("Plugin A", "plugin-a", "1.0.0", "https://example.com/a.zip")))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	registry := RegistryJSON{
		Plugins: []string{pluginSrv.URL + "/a/plugin.json"},
	}
	srv := httptest.NewServer(requireAuth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()

	// 不带 token 应该失败
	_, _, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err == nil {
		t.Fatal("expected error without token")
	}

	// 带正确 token 应该成功
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", testToken)
	if err != nil {
		t.Fatalf("FetchAndMerge with token error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].EntryPath != "plugin-a" {
		t.Errorf("expected plugin-a, got %q", plugins[0].EntryPath)
	}
}

func TestFetchAndMerge_TokenWithIncludes(t *testing.T) {
	const testToken = "secret-token"

	requireAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", requireAuth(servePluginJSON("A", "a", "1.0.0", "https://example.com/a.zip")))
	pluginMux.HandleFunc("/b/plugin.json", requireAuth(servePluginJSON("B", "b", "1.0.0", "https://example.com/b.zip")))
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/main.json", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		reg := RegistryJSON{
			Includes: []string{srv.URL + "/sub.json"},
			Plugins:  []string{pluginSrv.URL + "/a/plugin.json"},
		}
		json.NewEncoder(w).Encode(reg)
	}))
	mux.HandleFunc("/sub.json", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		reg := RegistryJSON{
			Plugins: []string{pluginSrv.URL + "/b/plugin.json"},
		}
		json.NewEncoder(w).Encode(reg)
	}))

	srv = httptest.NewServer(mux)
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL+"/main.json", "", testToken)
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins from authenticated main + sub, got %d", len(plugins))
	}
}

func TestFetchAndMerge_TokenNotLeakedToCrossOriginIncludes(t *testing.T) {
	const testToken = "private-token"

	// 公开源：不需要认证，如果收到 Authorization 头则返回 403（模拟 token 泄露检测）
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/public.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token leaked to cross-origin include")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		reg := RegistryJSON{
			Plugins: []string{},
		}
		json.NewEncoder(w).Encode(reg)
	})
	publicSrv := httptest.NewServer(publicMux)
	defer publicSrv.Close()

	// 私有源：需要认证，include 了公开源
	pluginMux := http.NewServeMux()
	pluginMux.HandleFunc("/a/plugin.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(PluginManifest{
			Name: "A", EntryPath: "a", Version: "1.0.0", DownloadURL: "https://example.com/a.zip",
		})
	})
	pluginSrv := httptest.NewServer(pluginMux)
	defer pluginSrv.Close()

	privateMux := http.NewServeMux()
	privateMux.HandleFunc("/private.json", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		reg := RegistryJSON{
			Includes: []string{publicSrv.URL + "/public.json"},
			Plugins:  []string{pluginSrv.URL + "/a/plugin.json"},
		}
		json.NewEncoder(w).Encode(reg)
	})
	privateSrv := httptest.NewServer(privateMux)
	defer privateSrv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), privateSrv.URL+"/private.json", "", testToken)
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
}

func TestFetchAndMerge_EmptyPlugins(t *testing.T) {
	registry := RegistryJSON{
		Name:    "Empty",
		Plugins: []string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(registry)
	}))
	defer srv.Close()

	svc := NewRegistryService()
	plugins, warnings, err := svc.FetchAndMerge(context.Background(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("FetchAndMerge error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if len(plugins) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(plugins))
	}
}
