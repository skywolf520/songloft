package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"songloft/internal/database/testutil"
	"songloft/internal/services"
)

// newTestScanHandlerWithConfig 构造带 ConfigService 的 ScanHandler，覆盖业务设置端点。
func newTestScanHandlerWithConfig(t *testing.T) *ScanHandler {
	t.Helper()
	mdb := testutil.OpenMemoryDB(t)
	configService := services.NewConfigService(mdb.ConfigRepository())
	return NewScanHandler(nil, nil, configService)
}

// TestMusicPathSetting_GetDefault GET /settings/music-path 在配置缺失时返回业务默认值。
// 这是方向 A 的关键：handler 内承担默认值，前端无需先 POST 创建。
func TestMusicPathSetting_GetDefault(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)

	rr := httptest.NewRecorder()
	h.GetMusicPathSetting(rr, httptest.NewRequest("GET", "/api/v1/settings/music-path", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp MusicPathSetting
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path != "music" {
		t.Errorf("default path: got %q want %q", resp.Path, "music")
	}
	if len(resp.ExcludeDirs) == 0 {
		t.Error("default exclude_dirs should contain @eaDir / tmp")
	}
}

// TestMusicPathSetting_UpdateThenReadAndCallback PUT 写入后 GET 读到最新值，
// 且 onMusicPathChanged 回调被异步触发（保证 Scanner 重建副作用）。
func TestMusicPathSetting_UpdateThenReadAndCallback(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)

	called := make(chan struct{}, 1)
	var callCount atomic.Int32
	h.SetOnMusicPathChanged(func() {
		callCount.Add(1)
		select {
		case called <- struct{}{}:
		default:
		}
	})

	body := `{"path":"/data/music","exclude_dirs":["tmp",".cache"],"exclude_paths":["/data/music/old"]}`
	rr := httptest.NewRecorder()
	h.UpdateMusicPathSetting(rr, httptest.NewRequest("PUT", "/api/v1/settings/music-path",
		strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}

	// 等回调（异步）
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("onMusicPathChanged callback should fire after PUT")
	}
	if callCount.Load() != 1 {
		t.Errorf("callback should fire exactly once, got %d", callCount.Load())
	}

	// GET 读到最新值
	rr2 := httptest.NewRecorder()
	h.GetMusicPathSetting(rr2, httptest.NewRequest("GET", "/api/v1/settings/music-path", nil))
	var got MusicPathSetting
	if err := json.Unmarshal(rr2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Path != "/data/music" || len(got.ExcludeDirs) != 2 || len(got.ExcludePaths) != 1 {
		t.Errorf("read-after-write mismatch: %+v", got)
	}
}

// TestMusicPathSetting_EmptyPathRejected path 为空时 PUT 应返回 400 并保留旧值。
func TestMusicPathSetting_EmptyPathRejected(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)

	rr := httptest.NewRecorder()
	h.UpdateMusicPathSetting(rr, httptest.NewRequest("PUT", "/api/v1/settings/music-path",
		strings.NewReader(`{"path":"","exclude_dirs":[]}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty path: got %d want 400", rr.Code)
	}
}

// TestPlaylistModeSetting_RoundTrip GET 默认 directory，PUT top_level 后读到 top_level。
func TestPlaylistModeSetting_RoundTrip(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)

	// 默认 directory
	rr := httptest.NewRecorder()
	h.GetPlaylistModeSetting(rr, httptest.NewRequest("GET", "/api/v1/settings/scan-playlist-mode", nil))
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["mode"] != "directory" {
		t.Errorf("default: got %q want directory", resp["mode"])
	}

	// PUT top_level
	rr2 := httptest.NewRecorder()
	h.UpdatePlaylistModeSetting(rr2, httptest.NewRequest("PUT", "/api/v1/settings/scan-playlist-mode",
		strings.NewReader(`{"mode":"top_level"}`)))
	if rr2.Code != http.StatusOK {
		t.Fatalf("PUT status: %d", rr2.Code)
	}

	// GET 应为 top_level
	rr3 := httptest.NewRecorder()
	h.GetPlaylistModeSetting(rr3, httptest.NewRequest("GET", "/api/v1/settings/scan-playlist-mode", nil))
	json.Unmarshal(rr3.Body.Bytes(), &resp)
	if resp["mode"] != "top_level" {
		t.Errorf("after PUT: got %q want top_level", resp["mode"])
	}
}

// TestPlaylistModeSetting_InvalidMode PUT 非法 mode 返回 400。
func TestPlaylistModeSetting_InvalidMode(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)
	rr := httptest.NewRecorder()
	h.UpdatePlaylistModeSetting(rr, httptest.NewRequest("PUT", "/api/v1/settings/scan-playlist-mode",
		strings.NewReader(`{"mode":"invalid"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid mode: got %d want 400", rr.Code)
	}
}

// TestScanSettings_BadJSON 请求体非法时返回 400。
func TestScanSettings_BadJSON(t *testing.T) {
	h := newTestScanHandlerWithConfig(t)

	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"music-path", h.UpdateMusicPathSetting},
		{"playlist-mode", h.UpdatePlaylistModeSetting},
	} {
		rr := httptest.NewRecorder()
		tc.fn(rr, httptest.NewRequest("PUT", "/api/v1/settings/test", strings.NewReader(`not json`)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: bad JSON got %d want 400", tc.name, rr.Code)
		}
	}
}
