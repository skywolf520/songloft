package jsplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"songloft/internal/httputil"
)

const (
	registryMaxDepth     = 20
	registryMaxPlugins   = 500
	registryMaxBodyBytes = 2 * 1024 * 1024 // 2 MB
	registryFetchTimeout = 15 * time.Second
	manifestConcurrency  = 8
)

// RegistryConfig 订阅源配置（存储在 config 表中）。
type RegistryConfig struct {
	URL     string `json:"url"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
}

// RegistryEntry 解析后的插件条目（内部表示 + API 返回值）。
type RegistryEntry struct {
	Name           string `json:"name,omitempty"`
	EntryPath      string `json:"entry_path,omitempty"`
	Version        string `json:"version,omitempty"`
	Description    string `json:"description,omitempty"`
	Author         string `json:"author,omitempty"`
	Homepage       string `json:"homepage,omitempty"`
	Icon           string `json:"icon,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	UpdateURL      string `json:"update_url,omitempty"`
	MinHostVersion string `json:"min_host_version,omitempty"`
}

// RegistryJSON 注册表 JSON 顶层结构。
// plugins 是 plugin.json URL 数组。
type RegistryJSON struct {
	Name     string   `json:"name,omitempty"`
	Includes []string `json:"includes,omitempty"`
	Plugins  []string `json:"plugins"`
}

// RegistryService 处理注册表的拉取、递归解析和去重合并。
type RegistryService struct {
	httpClient *http.Client
}

// NewRegistryService 创建 RegistryService。
func NewRegistryService() *RegistryService {
	return &RegistryService{
		httpClient: httputil.NewClient(registryFetchTimeout),
	}
}

// FetchAndMerge 从指定 URL 拉取注册表（含递归 includes），去重合并后返回插件列表。
// token 非空时，所有 HTTP 请求携带 Authorization: Bearer <token> 头。
func (s *RegistryService) FetchAndMerge(ctx context.Context, registryURL string, githubProxy string, token string) ([]RegistryEntry, []string, error) {
	visited := make(map[string]bool)
	var warnings []string

	// [1] 递归拉取所有 registry JSON，收集 plugin.json URL
	var pluginURLs []string
	if err := s.fetchRecursive(ctx, registryURL, githubProxy, token, 0, visited, &pluginURLs, &warnings); err != nil {
		return nil, warnings, err
	}

	if len(pluginURLs) > registryMaxPlugins {
		warnings = append(warnings, fmt.Sprintf("plugin count %d exceeds limit %d, truncated", len(pluginURLs), registryMaxPlugins))
		pluginURLs = pluginURLs[:registryMaxPlugins]
	}

	// [2] 并发解析所有 plugin.json URL
	resolved := s.resolveAll(ctx, pluginURLs, githubProxy, token, &warnings)

	// [3] 按 entry_path 去重（高版本优先）
	plugins := make(map[string]RegistryEntry)
	for _, entry := range resolved {
		if entry.EntryPath == "" || entry.DownloadURL == "" {
			if entry.EntryPath != "" {
				warnings = append(warnings, fmt.Sprintf("plugin %q: no download_url, skipped", entry.EntryPath))
			}
			continue
		}
		existing, exists := plugins[entry.EntryPath]
		if !exists || compareVersion(entry.Version, existing.Version) > 0 {
			plugins[entry.EntryPath] = entry
		}
	}

	result := make([]RegistryEntry, 0, len(plugins))
	for _, entry := range plugins {
		result = append(result, entry)
	}

	return result, warnings, nil
}

func (s *RegistryService) fetchRecursive(
	ctx context.Context,
	url string,
	githubProxy string,
	token string,
	depth int,
	visited map[string]bool,
	pluginURLs *[]string,
	warnings *[]string,
) error {
	if depth > registryMaxDepth {
		*warnings = append(*warnings, fmt.Sprintf("includes depth exceeded %d for %s", registryMaxDepth, url))
		return nil
	}

	canonicalURL := strings.TrimRight(url, "/")
	if visited[canonicalURL] {
		return nil
	}
	visited[canonicalURL] = true

	requestURL := applyProxy(url, githubProxy)

	registry, err := s.fetchJSON(ctx, requestURL, token)
	if err != nil {
		if depth == 0 {
			return fmt.Errorf("fetch registry %s: %w", requestURL, err)
		}
		*warnings = append(*warnings, fmt.Sprintf("failed to fetch include %s: %v", requestURL, err))
		return nil
	}

	*pluginURLs = append(*pluginURLs, registry.Plugins...)

	for _, includeURL := range registry.Includes {
		if includeURL == "" {
			continue
		}
		includeToken := ""
		if token != "" && sameHost(url, includeURL) {
			includeToken = token
		}
		if err := s.fetchRecursive(ctx, includeURL, githubProxy, includeToken, depth+1, visited, pluginURLs, warnings); err != nil {
			return err
		}
	}

	return nil
}

// resolveAll 并发拉取所有 plugin.json URL，返回解析后的 RegistryEntry 列表。
func (s *RegistryService) resolveAll(ctx context.Context, pluginURLs []string, githubProxy string, token string, warnings *[]string) []RegistryEntry {
	if len(pluginURLs) == 0 {
		return nil
	}

	result := make([]RegistryEntry, len(pluginURLs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, manifestConcurrency)

	for i, pluginURL := range pluginURLs {
		if pluginURL == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, rawURL string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			requestURL := applyProxy(rawURL, githubProxy)
			entry, err := s.resolvePluginJSON(ctx, requestURL, githubProxy, token)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				*warnings = append(*warnings, fmt.Sprintf("failed to fetch plugin.json %s: %v", requestURL, err))
				return
			}
			result[idx] = entry
		}(i, pluginURL)
	}
	wg.Wait()

	return result
}

// resolvePluginJSON 拉取远程 plugin.json 并映射到 RegistryEntry。
// 如果 plugin.json 中 download_url 为空但 updateUrl 有值，链式拉取 updateUrl 获取 download_url（兼容旧版插件）。
func (s *RegistryService) resolvePluginJSON(ctx context.Context, url string, githubProxy string, token string) (RegistryEntry, error) {
	body, err := s.fetchBody(ctx, url, token)
	if err != nil {
		return RegistryEntry{}, err
	}

	var manifest PluginManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return RegistryEntry{}, fmt.Errorf("parse plugin.json: %w", err)
	}

	entry := RegistryEntry{
		Name:           manifest.Name,
		EntryPath:      manifest.EntryPath,
		Version:        manifest.Version,
		Description:    manifest.Description,
		Author:         manifest.Author,
		Homepage:       manifest.Homepage,
		DownloadURL:    manifest.DownloadURL,
		UpdateURL:      manifest.UpdateURL,
		MinHostVersion: manifest.MinHostVersion,
	}

	if manifest.Icon != "" {
		if lastSlash := strings.LastIndex(url, "/"); lastSlash >= 0 {
			entry.Icon = url[:lastSlash+1] + "static/" + manifest.Icon
		}
	}

	// 兼容旧版插件：如果 plugin.json 未直接提供 download_url，通过 updateUrl 链式获取
	if entry.DownloadURL == "" && entry.UpdateURL != "" {
		updateRequestURL := applyProxy(entry.UpdateURL, githubProxy)
		if updateBody, err := s.fetchBody(ctx, updateRequestURL, token); err != nil {
			slog.Debug("chain fetch updateUrl failed", "entryPath", entry.EntryPath, "updateUrl", entry.UpdateURL, "error", err)
		} else {
			var updateManifest PluginManifest
			if err := json.Unmarshal(updateBody, &updateManifest); err == nil && updateManifest.DownloadURL != "" {
				entry.DownloadURL = updateManifest.DownloadURL
			}
		}
	}

	slog.Debug("resolved plugin from plugin.json", "entryPath", entry.EntryPath, "version", entry.Version)
	return entry, nil
}

func (s *RegistryService) fetchJSON(ctx context.Context, url string, token string) (*RegistryJSON, error) {
	body, err := s.fetchBody(ctx, url, token)
	if err != nil {
		return nil, err
	}

	var registry RegistryJSON
	if err := json.Unmarshal(body, &registry); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	return &registry, nil
}

func (s *RegistryService) fetchBody(ctx context.Context, url string, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, registryMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > registryMaxBodyBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", registryMaxBodyBytes)
	}

	return body, nil
}

// compareVersion 比较两个版本号，返回 >0 表示 a 更大。
// 支持 semver（1.2.3）和日期格式（2026.6.2），按 dot-separated 数值逐段比较。
func compareVersion(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	maxLen := max(len(aParts), len(bParts))

	for i := range maxLen {
		aVal := 0
		bVal := 0
		if i < len(aParts) {
			aVal, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bVal, _ = strconv.Atoi(bParts[i])
		}
		if aVal != bVal {
			return aVal - bVal
		}
	}
	return 0
}

// sameHost 判断两个 URL 的 host 是否相同（scheme+host+port）。
func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}
