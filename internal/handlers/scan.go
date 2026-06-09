package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"songloft/internal/services"
)

// ScanHandler 扫描处理器
//
// 除扫描动作外，还承载扫描相关业务设置端点（/settings/music-path 与
// /settings/scan-auto-create-include-subdirs），把 music_path /
// scan_auto_create_include_subdirs 两个 config key 的"业务化"读写收敛在此。
type ScanHandler struct {
	songService        *services.SongService
	scanner            *services.Scanner
	configService      *services.ConfigService
	fingerprintService *services.FingerprintService
	onMusicPathChanged func()                        // PUT /settings/music-path 完成后触发，重建 Scanner 等副作用
	onAutoScanChanged  func(services.AutoScanConfig) // PUT /settings/auto-scan 完成后触发，重启自动扫描调度
}

// NewScanHandler 创建扫描处理器。configService 可为 nil（仅纯扫描端点的测试场景）。
func NewScanHandler(songService *services.SongService, scanner *services.Scanner, configService *services.ConfigService) *ScanHandler {
	return &ScanHandler{
		songService:   songService,
		scanner:       scanner,
		configService: configService,
	}
}

// SetFingerprintService 注入指纹服务。
func (h *ScanHandler) SetFingerprintService(fs *services.FingerprintService) {
	h.fingerprintService = fs
}

// SetScanner 更新扫描器引用（配置变更时调用）
func (h *ScanHandler) SetScanner(scanner *services.Scanner) {
	h.scanner = scanner
}

// SetOnMusicPathChanged 注入 music_path 写后回调（重建 Scanner + 清排除目录中的歌曲）。
// 同一回调也注册到通用 /configs/{key} 的 onConfigChanged，让 admin 工具直改 music_path 时
// 副作用同样生效（保持两条入口语义对齐）。
func (h *ScanHandler) SetOnMusicPathChanged(cb func()) {
	h.onMusicPathChanged = cb
}

// SetOnAutoScanChanged 注入 auto_scan 写后回调（重启自动扫描调度器）。
func (h *ScanHandler) SetOnAutoScanChanged(cb func(services.AutoScanConfig)) {
	h.onAutoScanChanged = cb
}

// ScanRequest 扫描请求参数
type ScanRequest struct {
	Reimport bool `json:"reimport"`
}

// ScanAndImport 扫描并导入本地音乐（异步）
// @Summary 扫描并导入本地音乐
// @Description 异步扫描音乐目录并导入新发现的音乐文件到数据库，立即返回，可通过进度接口查询状态
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body ScanRequest false "扫描请求参数"
// @Success 200 {object} map[string]interface{} "扫描任务已启动"
// @Failure 409 {object} map[string]string "扫描正在进行中"
// @Failure 500 {object} map[string]string "启动扫描失败"
// @Security BearerAuth
// @Router /scan [post]
func (h *ScanHandler) ScanAndImport(w http.ResponseWriter, r *http.Request) {
	// 解析请求参数
	var req ScanRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "无效的请求参数", err)
			return
		}
	}

	err := h.songService.ScanAndImportAsync(req.Reimport)
	if err != nil {
		respondError(w, http.StatusConflict, "扫描正在进行中", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "扫描任务已启动",
	})
}

// GetScanProgress 获取扫描进度
// @Summary 获取扫描进度
// @Description 获取当前扫描任务的进度信息
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Success 200 {object} services.ScanProgress "扫描进度信息"
// @Security BearerAuth
// @Router /scan/progress [get]
func (h *ScanHandler) GetScanProgress(w http.ResponseWriter, r *http.Request) {
	progress := h.songService.GetScanProgress()
	respondJSON(w, http.StatusOK, progress)
}

// CancelScan 取消扫描
// @Summary 取消扫描
// @Description 取消正在进行的扫描任务
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "取消成功"
// @Failure 400 {object} map[string]string "没有正在进行的扫描任务"
// @Security BearerAuth
// @Router /scan/cancel [post]
func (h *ScanHandler) CancelScan(w http.ResponseWriter, r *http.Request) {
	if !h.songService.CancelScan() {
		respondError(w, http.StatusBadRequest, "没有正在进行的扫描任务", nil)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"message": "扫描任务已取消",
	})
}

// ListDirectories 获取子目录列表（目录树懒加载）
// @Summary 获取子目录列表
// @Description 返回指定路径下的一级子目录列表，用于目录树懒加载。path 为空时返回音乐根目录下的子目录
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param path query string false "目录路径（为空时使用音乐根目录）"
// @Success 200 {object} map[string]interface{} "子目录列表"
// @Failure 400 {object} map[string]string "无效的路径"
// @Failure 500 {object} map[string]string "读取目录失败"
// @Security BearerAuth
// @Router /scan/directories [get]
func (h *ScanHandler) ListDirectories(w http.ResponseWriter, r *http.Request) {
	requestPath := r.URL.Query().Get("path")
	musicRoot := h.scanner.GetMusicPath()

	// 如果未指定路径，使用音乐根目录
	targetPath := musicRoot
	if requestPath != "" {
		targetPath = requestPath
	}

	// 安全校验：确保请求路径在音乐根目录下，防止目录遍历攻击
	cleanTarget := filepath.Clean(targetPath)
	cleanRoot := filepath.Clean(musicRoot)
	if cleanTarget != cleanRoot && !strings.HasPrefix(cleanTarget, cleanRoot+string(filepath.Separator)) {
		respondError(w, http.StatusBadRequest, "路径必须在音乐目录下", nil)
		return
	}

	dirs, err := h.scanner.ListSubDirs(targetPath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "读取目录失败", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"directories": dirs,
		"root":        musicRoot,
	})
}

// ListDirNames 获取所有目录名称（自动补全用）
// @Summary 获取所有目录名称
// @Description 递归收集音乐目录下所有唯一的目录名称，按字母排序返回，用于排除目录名称的自动补全
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{} "目录名称列表"
// @Failure 500 {object} map[string]string "收集目录名称失败"
// @Security BearerAuth
// @Router /scan/dir-names [get]
func (h *ScanHandler) ListDirNames(w http.ResponseWriter, r *http.Request) {
	names, err := h.scanner.CollectAllDirNames(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "收集目录名称失败", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"names": names,
	})
}

// ============================================================
// 业务化配置端点
// ============================================================
//
// 与 /api/v1/configs/{key} 的关系：
//   - /configs/{key} 是通用 KV，保留为 admin 入口（config_manager 编辑器）。
//   - 客户端业务功能一律走下方业务端点：强类型、自带默认值、PUT 后内联触发副作用。
//   - 详见 AGENTS.md「配置接口规范」章节。

const (
	autoScanConfigKey                = "auto_scan"
	musicPathConfigKey               = "music_path"
	scanAutoCreateSubdirsConfigKey   = "scan_auto_create_include_subdirs"
	scanAutoCreatePlaylistsConfigKey = "scan_auto_create_playlists"
	scanTitleSourceConfigKey         = "scan_title_source"
)

// MusicPathSetting /settings/music-path 的请求与响应体。
// 与 config 表 music_path 行的 JSON value 结构完全一致，便于 admin 工具与业务端点互通。
type MusicPathSetting struct {
	Path         string   `json:"path"`
	ExcludeDirs  []string `json:"exclude_dirs"`
	ExcludePaths []string `json:"exclude_paths"`
}

// GetMusicPathSetting GET /api/v1/settings/music-path
// @Summary 获取音乐路径与扫描排除配置
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} MusicPathSetting
// @Security BearerAuth
// @Router /settings/music-path [get]
func (h *ScanHandler) GetMusicPathSetting(w http.ResponseWriter, r *http.Request) {
	cfg := MusicPathSetting{
		Path:         "music",
		ExcludeDirs:  []string{"@eaDir", "tmp"},
		ExcludePaths: []string{},
	}
	if h.configService != nil {
		// 未命中时保留默认值；命中则覆盖
		_ = h.configService.GetJSON(musicPathConfigKey, &cfg)
	}
	if cfg.ExcludeDirs == nil {
		cfg.ExcludeDirs = []string{}
	}
	if cfg.ExcludePaths == nil {
		cfg.ExcludePaths = []string{}
	}
	respondJSON(w, http.StatusOK, cfg)
}

// UpdateMusicPathSetting PUT /api/v1/settings/music-path
// @Summary 更新音乐路径与扫描排除配置
// @Description 写入 music_path 配置并触发 Scanner 重建 + 清理排除目录中的歌曲（与 admin /configs PUT 的副作用一致）。
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body MusicPathSetting true "配置内容"
// @Success 200 {object} MusicPathSetting
// @Failure 400 {object} map[string]string "请求格式错误"
// @Failure 500 {object} map[string]string "保存配置失败"
// @Security BearerAuth
// @Router /settings/music-path [put]
func (h *ScanHandler) UpdateMusicPathSetting(w http.ResponseWriter, r *http.Request) {
	if h.configService == nil {
		respondError(w, http.StatusInternalServerError, "configService 未注入", nil)
		return
	}
	var req MusicPathSetting
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求格式错误", err)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		respondError(w, http.StatusBadRequest, "path 不能为空", nil)
		return
	}
	if req.ExcludeDirs == nil {
		req.ExcludeDirs = []string{}
	}
	if req.ExcludePaths == nil {
		req.ExcludePaths = []string{}
	}
	if err := h.configService.SetJSON(musicPathConfigKey, req); err != nil {
		respondError(w, http.StatusInternalServerError, "保存配置失败", err)
		return
	}
	// 副作用与通用 PUT /configs/music_path 完全一致（onConfigChanged 回调），异步触发不阻塞 PUT 响应。
	if h.onMusicPathChanged != nil {
		go h.onMusicPathChanged()
	}
	respondJSON(w, http.StatusOK, req)
}

// scanAutoCreateSubdirsRequest /settings/scan-auto-create-include-subdirs PUT 请求体
type scanAutoCreateSubdirsRequest struct {
	Enabled bool `json:"enabled"`
}

// GetAutoCreateIncludeSubdirsSetting GET /api/v1/settings/scan-auto-create-include-subdirs
// @Summary 获取「扫描后自动创建歌单是否包含子目录」开关
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} map[string]bool "返回 enabled 字段"
// @Security BearerAuth
// @Router /settings/scan-auto-create-include-subdirs [get]
func (h *ScanHandler) GetAutoCreateIncludeSubdirsSetting(w http.ResponseWriter, r *http.Request) {
	enabled := false
	if h.configService != nil {
		enabled = h.configService.GetBool(scanAutoCreateSubdirsConfigKey, false)
	}
	respondJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// UpdateAutoCreateIncludeSubdirsSetting PUT /api/v1/settings/scan-auto-create-include-subdirs
// @Summary 更新「扫描后自动创建歌单是否包含子目录」开关
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body scanAutoCreateSubdirsRequest true "开关请求"
// @Success 200 {object} map[string]bool "返回 enabled 字段"
// @Failure 400 {object} map[string]string "请求格式错误"
// @Failure 500 {object} map[string]string "保存配置失败"
// @Security BearerAuth
// @Router /settings/scan-auto-create-include-subdirs [put]
func (h *ScanHandler) UpdateAutoCreateIncludeSubdirsSetting(w http.ResponseWriter, r *http.Request) {
	if h.configService == nil {
		respondError(w, http.StatusInternalServerError, "configService 未注入", nil)
		return
	}
	var req scanAutoCreateSubdirsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求格式错误", err)
		return
	}
	val := "false"
	if req.Enabled {
		val = "true"
	}
	if err := h.configService.Set(scanAutoCreateSubdirsConfigKey, val); err != nil {
		respondError(w, http.StatusInternalServerError, "保存配置失败", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

// scanAutoCreatePlaylistsRequest /settings/scan-auto-create-playlists PUT 请求体
type scanAutoCreatePlaylistsRequest struct {
	Enabled bool `json:"enabled"`
}

// GetAutoCreatePlaylistsSetting GET /api/v1/settings/scan-auto-create-playlists
// @Summary 获取「扫描后自动创建歌单」开关
// @Description 控制扫描完成后是否根据音乐目录结构自动创建歌单。默认启用（true）。关闭后扫描仅入库歌曲，不再自动建歌单。
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} map[string]bool "返回 enabled 字段"
// @Security BearerAuth
// @Router /settings/scan-auto-create-playlists [get]
func (h *ScanHandler) GetAutoCreatePlaylistsSetting(w http.ResponseWriter, r *http.Request) {
	enabled := true
	if h.configService != nil {
		enabled = h.configService.GetBool(scanAutoCreatePlaylistsConfigKey, true)
	}
	respondJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

// UpdateAutoCreatePlaylistsSetting PUT /api/v1/settings/scan-auto-create-playlists
// @Summary 更新「扫描后自动创建歌单」开关
// @Description 控制扫描完成后是否根据音乐目录结构自动创建歌单。
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body scanAutoCreatePlaylistsRequest true "开关请求"
// @Success 200 {object} map[string]bool "返回 enabled 字段"
// @Failure 400 {object} map[string]string "请求格式错误"
// @Failure 500 {object} map[string]string "保存配置失败"
// @Security BearerAuth
// @Router /settings/scan-auto-create-playlists [put]
func (h *ScanHandler) UpdateAutoCreatePlaylistsSetting(w http.ResponseWriter, r *http.Request) {
	if h.configService == nil {
		respondError(w, http.StatusInternalServerError, "configService 未注入", nil)
		return
	}
	var req scanAutoCreatePlaylistsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求格式错误", err)
		return
	}
	val := "false"
	if req.Enabled {
		val = "true"
	}
	if err := h.configService.Set(scanAutoCreatePlaylistsConfigKey, val); err != nil {
		respondError(w, http.StatusInternalServerError, "保存配置失败", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

// scanTitleSourceRequest /settings/scan-title-source PUT 请求体
type scanTitleSourceRequest struct {
	TitleSource string `json:"title_source" example:"tag" enums:"tag,filename"`
}

// GetScanTitleSourceSetting GET /api/v1/settings/scan-title-source
// @Summary 获取扫描标题来源配置
// @Description tag：优先使用音频标签中的标题（默认）；filename：始终使用文件名（不含扩展名）作为标题。切换后需以「重新导入」模式扫描才能生效。
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} scanTitleSourceRequest "返回 title_source 字段"
// @Security BearerAuth
// @Router /settings/scan-title-source [get]
func (h *ScanHandler) GetScanTitleSourceSetting(w http.ResponseWriter, r *http.Request) {
	titleSource := "tag"
	if h.configService != nil {
		titleSource = h.configService.GetString(scanTitleSourceConfigKey, "tag")
	}
	respondJSON(w, http.StatusOK, scanTitleSourceRequest{TitleSource: titleSource})
}

// UpdateScanTitleSourceSetting PUT /api/v1/settings/scan-title-source
// @Summary 更新扫描标题来源配置
// @Description tag：优先使用音频标签中的标题；filename：始终使用文件名（不含扩展名）作为标题。切换后需以「重新导入」模式扫描才能生效。
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body scanTitleSourceRequest true "标题来源配置"
// @Success 200 {object} scanTitleSourceRequest "返回 title_source 字段"
// @Failure 400 {object} map[string]string "请求格式错误或参数无效"
// @Failure 500 {object} map[string]string "保存配置失败"
// @Security BearerAuth
// @Router /settings/scan-title-source [put]
func (h *ScanHandler) UpdateScanTitleSourceSetting(w http.ResponseWriter, r *http.Request) {
	if h.configService == nil {
		respondError(w, http.StatusInternalServerError, "configService 未注入", nil)
		return
	}
	var req scanTitleSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求格式错误", err)
		return
	}
	if req.TitleSource != "tag" && req.TitleSource != "filename" {
		respondError(w, http.StatusBadRequest, "title_source 必须为 tag 或 filename", nil)
		return
	}
	if err := h.configService.Set(scanTitleSourceConfigKey, req.TitleSource); err != nil {
		respondError(w, http.StatusInternalServerError, "保存配置失败", err)
		return
	}
	if h.onMusicPathChanged != nil {
		go h.onMusicPathChanged()
	}
	respondJSON(w, http.StatusOK, scanTitleSourceRequest{TitleSource: req.TitleSource})
}

// GetFingerprintStatus 获取指纹计算状态
// @Summary 获取指纹计算状态
// @Description 返回 ffmpeg chromaprint 可用性以及本地歌曲指纹计算统计
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} map[string]interface{} "指纹状态"
// @Security BearerAuth
// @Router /scan/fingerprints/status [get]
func (h *ScanHandler) GetFingerprintStatus(w http.ResponseWriter, r *http.Request) {
	available := services.IsChromaprintAvailable()
	var total, computed int64
	if h.fingerprintService != nil {
		var err error
		total, computed, err = h.songService.CountLocalFingerprints(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, "查询指纹统计失败", err)
			return
		}
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"chromaprint_available": available,
		"total":                 total,
		"computed":              computed,
		"missing":               total - computed,
	})
}

// StartFingerprintCompute 触发批量指纹计算
// @Summary 触发批量指纹计算
// @Description 异步为本地歌曲计算音频指纹，需要 ffmpeg 支持 chromaprint。若已有任务在运行则打断重启。传入 recompute_all=true 时清空已有指纹后重新计算全部。
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body handlers.startFingerprintRequest false "计算选项"
// @Success 200 {object} map[string]interface{} "任务已启动"
// @Failure 400 {object} map[string]string "chromaprint 不可用"
// @Security BearerAuth
// @Router /scan/fingerprints [post]
func (h *ScanHandler) StartFingerprintCompute(w http.ResponseWriter, r *http.Request) {
	if !services.IsChromaprintAvailable() {
		respondError(w, http.StatusBadRequest, "ffmpeg chromaprint 不可用，无法计算音频指纹", nil)
		return
	}
	if h.fingerprintService == nil {
		respondError(w, http.StatusInternalServerError, "fingerprint service not initialized", nil)
		return
	}

	var req startFingerprintRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "请求体格式错误", err)
			return
		}
	}

	var total int
	var err error
	if req.RecomputeAll {
		total, err = h.fingerprintService.RecomputeAll()
	} else {
		total, err = h.fingerprintService.ComputeMissing()
	}
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status": "started",
		"total":  total,
	})
}

type startFingerprintRequest struct {
	RecomputeAll bool `json:"recompute_all"`
}

// GetFingerprintProgress 获取指纹计算进度
// @Summary 获取指纹计算进度
// @Description 查询当前指纹计算任务的进度
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} services.FingerprintProgress "计算进度"
// @Security BearerAuth
// @Router /scan/fingerprints/progress [get]
func (h *ScanHandler) GetFingerprintProgress(w http.ResponseWriter, r *http.Request) {
	if h.fingerprintService == nil {
		respondJSON(w, http.StatusOK, services.FingerprintProgress{Status: "idle"})
		return
	}
	respondJSON(w, http.StatusOK, h.fingerprintService.GetProgress())
}

// AutoScanSetting /settings/auto-scan 的请求与响应体。
type AutoScanSetting struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"interval_seconds"`
}

// GetAutoScanSetting GET /api/v1/settings/auto-scan
// @Summary 获取自动扫描配置
// @Description 返回自动扫描的启用状态和扫描间隔（秒）。默认关闭，间隔 3600 秒（1 小时）。
// @Tags 扫描管理
// @Produce json
// @Success 200 {object} AutoScanSetting
// @Security BearerAuth
// @Router /settings/auto-scan [get]
func (h *ScanHandler) GetAutoScanSetting(w http.ResponseWriter, r *http.Request) {
	cfg := AutoScanSetting{
		Enabled:         false,
		IntervalSeconds: 3600,
	}
	if h.configService != nil {
		_ = h.configService.GetJSON(autoScanConfigKey, &cfg)
	}
	respondJSON(w, http.StatusOK, cfg)
}

// UpdateAutoScanSetting PUT /api/v1/settings/auto-scan
// @Summary 更新自动扫描配置
// @Description 设置自动扫描的启用状态和扫描间隔。interval_seconds 有效范围 [60, 86400]。更新后立即生效（无需重启）。
// @Tags 扫描管理
// @Accept json
// @Produce json
// @Param request body AutoScanSetting true "自动扫描配置"
// @Success 200 {object} AutoScanSetting
// @Failure 400 {object} map[string]string "请求格式错误或参数无效"
// @Failure 500 {object} map[string]string "保存配置失败"
// @Security BearerAuth
// @Router /settings/auto-scan [put]
func (h *ScanHandler) UpdateAutoScanSetting(w http.ResponseWriter, r *http.Request) {
	if h.configService == nil {
		respondError(w, http.StatusInternalServerError, "configService 未注入", nil)
		return
	}
	var req AutoScanSetting
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "请求格式错误", err)
		return
	}
	if req.IntervalSeconds < 60 || req.IntervalSeconds > 86400 {
		respondError(w, http.StatusBadRequest, "interval_seconds 必须在 60 到 86400 之间", nil)
		return
	}
	if err := h.configService.SetJSON(autoScanConfigKey, req); err != nil {
		respondError(w, http.StatusInternalServerError, "保存配置失败", err)
		return
	}
	if h.onAutoScanChanged != nil {
		cfg := services.AutoScanConfig{
			Enabled:         req.Enabled,
			IntervalSeconds: req.IntervalSeconds,
		}
		go h.onAutoScanChanged(cfg)
	}
	respondJSON(w, http.StatusOK, req)
}
