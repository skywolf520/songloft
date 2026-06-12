package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"songloft/internal/services"
	"songloft/internal/services/playactivity"
)

func validateDirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建目录: %w", err)
	}
	testFile := filepath.Join(dir, ".songloft_write_test")
	f, err := os.Create(testFile)
	if err != nil {
		return fmt.Errorf("目录不可写: %w", err)
	}
	f.Close()
	os.Remove(testFile)
	return nil
}

// dirValidateRequest 目录验证请求
type dirValidateRequest struct {
	Path string `json:"path"`
}

// dirValidateResponse 目录验证响应
type dirValidateResponse struct {
	Valid     bool   `json:"valid"`
	Created   bool   `json:"created"`
	TotalSize int64  `json:"total_size"`
	FreeSize  int64  `json:"free_size"`
	Error     string `json:"error,omitempty"`
}

// CacheHandler 音乐缓存管理处理器。
//
// 注:播放 URL 端点已迁移到 SongHandler.GetSongURL (/songs/{id}/url)
type CacheHandler struct {
	cacheService  *services.CacheService
	configService *services.ConfigService
}

// AsyncReassigner 抽象 SourceOrchestrator.AsyncReassign(避免 handlers 依赖 source 包)
//
// sk 标识当前请求的客户端会话(由 playactivity.SessionFromContext 提取)，
// 让 reassign 后台任务的 ctx 注册到该会话桶——用户切到其他歌时被一并 cancel，
// 不会与新切歌的 plugin worker 抢占。
type AsyncReassigner interface {
	AsyncReassign(songID int64, sk playactivity.SessionKey)
}

// NewCacheHandler 创建缓存管理处理器。
func NewCacheHandler(
	cacheService *services.CacheService,
	configService *services.ConfigService,
) *CacheHandler {
	return &CacheHandler{
		cacheService:  cacheService,
		configService: configService,
	}
}

// HandleGetCacheStats 获取缓存统计信息
// @Summary 获取缓存统计信息
// @Description 获取服务端音乐缓存的统计信息,包括总大小、文件数量和最大缓存限制
// @Tags 缓存管理
// @Accept json
// @Produce json
// @Success 200 {object} services.CacheStats "缓存统计信息"
// @Failure 500 {object} map[string]string "服务器错误"
// @Security BearerAuth
// @Router /cache-manage/stats [get]
func (h *CacheHandler) HandleGetCacheStats(w http.ResponseWriter, r *http.Request) {
	stats := h.cacheService.GetCacheStats()
	respondJSON(w, http.StatusOK, stats)
}

// HandleCleanCache 清理全部缓存
// @Summary 清理全部音乐缓存
// @Description 删除服务端所有已缓存的音乐文件,清理后需要重新下载
// @Tags 缓存管理
// @Accept json
// @Produce json
// @Success 200 {object} map[string]string "清理成功"
// @Failure 500 {object} map[string]string "清理失败"
// @Security BearerAuth
// @Router /cache-manage/clean [post]
func (h *CacheHandler) HandleCleanCache(w http.ResponseWriter, r *http.Request) {
	if err := h.cacheService.CleanCache(); err != nil {
		slog.Error("清理缓存失败", "error", err)
		respondError(w, http.StatusInternalServerError, "清理缓存失败", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "缓存已清理"})
}

// HandleGetCacheConfig 获取缓存配置
// @Summary 获取缓存配置
// @Description 获取服务端音乐缓存的配置信息,包括最大缓存大小限制和缓存目录路径。cache_dir 为空表示使用 default_cache_dir。
// @Tags 缓存管理
// @Accept json
// @Produce json
// @Success 200 {object} services.CacheConfigResponse "缓存配置"
// @Failure 500 {object} map[string]string "服务器错误"
// @Security BearerAuth
// @Router /cache-manage/config [get]
func (h *CacheHandler) HandleGetCacheConfig(w http.ResponseWriter, r *http.Request) {
	resp := h.cacheService.GetCacheConfigResponse()
	respondJSON(w, http.StatusOK, resp)
}

// HandleUpdateCacheConfig 更新缓存配置
// @Summary 更新缓存配置
// @Description 更新服务端音乐缓存的配置,如最大缓存大小和缓存目录。cache_dir 为空字符串时恢复使用默认目录。更新后会自动触发 LRU 淘汰检查。切换目录时不会自动迁移旧缓存文件。
// @Tags 缓存管理
// @Accept json
// @Produce json
// @Param request body services.CacheConfig true "缓存配置"
// @Success 200 {object} services.CacheConfigResponse "更新后的缓存配置"
// @Failure 400 {object} map[string]string "请求参数无效"
// @Failure 500 {object} map[string]string "更新失败"
// @Security BearerAuth
// @Router /cache-manage/config [put]
func (h *CacheHandler) HandleUpdateCacheConfig(w http.ResponseWriter, r *http.Request) {
	var cfg services.CacheConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		respondError(w, http.StatusBadRequest, "请求参数无效", err)
		return
	}

	if cfg.MaxSize < 0 {
		respondError(w, http.StatusBadRequest, "最大缓存大小不能为负数", nil)
		return
	}

	if cfg.CacheDir != "" {
		if !filepath.IsAbs(cfg.CacheDir) {
			respondError(w, http.StatusBadRequest, "缓存目录必须为绝对路径", nil)
			return
		}
		if err := validateDirWritable(cfg.CacheDir); err != nil {
			respondError(w, http.StatusBadRequest, "缓存目录不可用: "+err.Error(), err)
			return
		}
	}

	if err := h.cacheService.UpdateCacheConfig(cfg); err != nil {
		slog.Error("更新缓存配置失败", "error", err)
		respondError(w, http.StatusInternalServerError, "更新缓存配置失败", err)
		return
	}

	resp := h.cacheService.GetCacheConfigResponse()
	respondJSON(w, http.StatusOK, resp)
}

// HandleValidateCacheDir 验证缓存目录
// @Summary 验证缓存目录
// @Description 验证指定目录是否可用作缓存目录。目录不存在时自动创建,检查可写性并返回磁盘空间信息。
// @Tags 缓存管理
// @Accept json
// @Produce json
// @Param request body dirValidateRequest true "目录路径"
// @Success 200 {object} dirValidateResponse "验证结果"
// @Failure 400 {object} map[string]string "请求参数无效"
// @Security BearerAuth
// @Router /cache-manage/validate-dir [post]
func (h *CacheHandler) HandleValidateCacheDir(w http.ResponseWriter, r *http.Request) {
	var req dirValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求参数无效", err)
		return
	}
	req.Path = filepath.Clean(req.Path)

	if req.Path == "" {
		respondError(w, http.StatusBadRequest, "路径不能为空", nil)
		return
	}
	if !filepath.IsAbs(req.Path) {
		respondJSON(w, http.StatusOK, dirValidateResponse{Error: "必须为绝对路径"})
		return
	}

	_, statErr := os.Stat(req.Path)
	created := os.IsNotExist(statErr)

	if err := validateDirWritable(req.Path); err != nil {
		respondJSON(w, http.StatusOK, dirValidateResponse{Error: err.Error()})
		return
	}

	var total, free int64
	var fs syscall.Statfs_t
	if err := syscall.Statfs(req.Path, &fs); err == nil {
		total = int64(fs.Blocks) * int64(fs.Bsize)
		free = int64(fs.Bavail) * int64(fs.Bsize)
	}

	respondJSON(w, http.StatusOK, dirValidateResponse{
		Valid:     true,
		Created:   created,
		TotalSize: total,
		FreeSize:  free,
	})
}
