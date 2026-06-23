package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"songloft/internal/database/sqlc"
	"songloft/internal/models"
)

// NeedsMetadata 判断歌曲是否缺少技术元数据（与 ListSongsNeedingMetadata SQL 条件一致）。
func NeedsMetadata(song *models.Song) bool {
	return song.Type == models.TypeRemote &&
		(song.Duration == 0 || song.BitRate == 0 || song.SampleRate == 0 || song.Format == "")
}

type MetadataRefreshStatus = string

const (
	MetadataRefreshIdle       MetadataRefreshStatus = "idle"
	MetadataRefreshRunning    MetadataRefreshStatus = "running"
	MetadataRefreshCancelling MetadataRefreshStatus = "cancelling"
	MetadataRefreshDone       MetadataRefreshStatus = "done"
	MetadataRefreshCancelled  MetadataRefreshStatus = "cancelled"
	MetadataRefreshFailed     MetadataRefreshStatus = "failed"
)

type MetadataRefreshProgress struct {
	Status    string `json:"status"`
	Total     int    `json:"total"`
	Processed int    `json:"processed"`
	Failed    int    `json:"failed"`
}

type MetadataRefresher struct {
	mu       sync.Mutex
	progress MetadataRefreshProgress
	cancelFn context.CancelFunc

	listSongs         func(ctx context.Context) ([]sqlc.ListSongsNeedingMetadataRow, error)
	updateMeta        func(ctx context.Context, params sqlc.UpdateSongMetadataParams) error
	updateTags        func(ctx context.Context, params sqlc.UpdateSongTagFieldsParams) error
	resolveURL        func(ctx context.Context, song *models.Song) (string, error)
	extractor         *MetadataExtractor
	remoteTitleSource func() string // "tag": 用标签覆盖 title; "filename"(默认): 不覆盖

	refreshInflight sync.Map // songID -> struct{}, 防止同一首歌并发提取
}

func NewMetadataRefresher(
	listSongs func(ctx context.Context) ([]sqlc.ListSongsNeedingMetadataRow, error),
	updateMeta func(ctx context.Context, params sqlc.UpdateSongMetadataParams) error,
	updateTags func(ctx context.Context, params sqlc.UpdateSongTagFieldsParams) error,
	resolveURL func(ctx context.Context, song *models.Song) (string, error),
	extractor *MetadataExtractor,
) *MetadataRefresher {
	return &MetadataRefresher{
		progress:   MetadataRefreshProgress{Status: MetadataRefreshIdle},
		listSongs:  listSongs,
		updateMeta: updateMeta,
		updateTags: updateTags,
		resolveURL: resolveURL,
		extractor:  extractor,
	}
}

// SetRemoteTitleSource 注入远程歌曲标题来源配置回调。
func (d *MetadataRefresher) SetRemoteTitleSource(fn func() string) {
	d.remoteTitleSource = fn
}

// shouldOverrideTitle 返回是否应用 tag 标题覆盖。
func (d *MetadataRefresher) shouldOverrideTitle() bool {
	if d.remoteTitleSource == nil {
		return false
	}
	return d.remoteTitleSource() == "tag"
}

func (d *MetadataRefresher) Start() error {
	d.mu.Lock()
	if d.progress.Status == MetadataRefreshRunning || d.progress.Status == MetadataRefreshCancelling {
		d.mu.Unlock()
		return ErrMetadataRefreshRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancelFn = cancel
	d.progress = MetadataRefreshProgress{Status: MetadataRefreshRunning}
	d.mu.Unlock()

	go d.run(ctx)
	return nil
}

func (d *MetadataRefresher) GetProgress() MetadataRefreshProgress {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.progress
}

func (d *MetadataRefresher) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancelFn != nil && d.progress.Status == MetadataRefreshRunning {
		d.cancelFn()
		d.progress.Status = MetadataRefreshCancelling
	}
}

func (d *MetadataRefresher) run(ctx context.Context) {
	defer func() {
		d.mu.Lock()
		switch d.progress.Status {
		case MetadataRefreshRunning:
			d.progress.Status = MetadataRefreshDone
		case MetadataRefreshCancelling:
			d.progress.Status = MetadataRefreshCancelled
		}
		d.cancelFn = nil
		d.mu.Unlock()
	}()

	songs, err := d.listSongs(ctx)
	if err != nil {
		slog.Warn("metadata refresh: list songs failed", "error", err)
		d.mu.Lock()
		d.progress.Status = MetadataRefreshFailed
		d.mu.Unlock()
		return
	}

	d.mu.Lock()
	d.progress.Total = len(songs)
	d.mu.Unlock()

	if len(songs) == 0 {
		return
	}

	for _, row := range songs {
		if ctx.Err() != nil {
			break
		}
		d.processOne(ctx, row)
	}
}

func (d *MetadataRefresher) processOne(ctx context.Context, row sqlc.ListSongsNeedingMetadataRow) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	url := row.Url
	if row.PluginEntryPath != "" && row.SourceData != "" {
		song := &models.Song{
			ID:              row.ID,
			PluginEntryPath: row.PluginEntryPath,
			SourceData:      row.SourceData,
			URL:             row.Url,
		}
		resolved, err := d.resolveURL(probeCtx, song)
		if err != nil {
			slog.Debug("metadata refresh: resolve url failed", "songId", row.ID, "error", err)
			d.incFailed()
			return
		}
		url = resolved
	}

	if url == "" {
		d.incFailed()
		return
	}

	probe, err := d.extractor.ProbeMetadataFromURL(probeCtx, url)
	if err != nil {
		slog.Debug("metadata refresh: probe failed", "songId", row.ID, "error", err)
		d.incFailed()
		return
	}

	// 封面处理：tag 库已在 ProbeMetadataFromURL 内提取，此处用 ffmpeg 兜底
	coverPath := probe.CoverPath
	if coverPath == "" && row.CoverPath == "" && row.CoverUrl == "" {
		if cp, err := d.extractor.ExtractCoverFromURL(probeCtx, url); err == nil {
			coverPath = cp
		} else {
			slog.Debug("metadata refresh: cover extraction skipped", "songId", row.ID, "error", err)
		}
	}

	if err := d.updateMeta(ctx, sqlc.UpdateSongMetadataParams{
		Column1:    probe.Duration,
		Duration:   probe.Duration,
		Column3:    int64(probe.BitRate),
		BitRate:    int64(probe.BitRate),
		Column5:    int64(probe.SampleRate),
		SampleRate: int64(probe.SampleRate),
		Column7:    probe.Format,
		Format:     probe.Format,
		Column9:    probe.Title,
		Title:      probe.Title,
		Column11:   probe.Artist,
		Artist:     probe.Artist,
		Column13:   probe.Album,
		Album:      probe.Album,
		Column15:   coverPath,
		CoverPath:  coverPath,
		ID:         row.ID,
	}); err != nil {
		slog.Warn("metadata refresh: update failed", "songId", row.ID, "error", err)
		d.incFailed()
		return
	}

	if d.updateTags != nil && (probe.Artist != "" || probe.Album != "" || (d.shouldOverrideTitle() && probe.Title != "")) {
		tagTitle := ""
		if d.shouldOverrideTitle() {
			tagTitle = probe.Title
		}
		if err := d.updateTags(ctx, sqlc.UpdateSongTagFieldsParams{
			Column1: tagTitle,
			Title:   tagTitle,
			Column3: probe.Artist,
			Artist:  probe.Artist,
			Column5: probe.Album,
			Album:   probe.Album,
			ID:      row.ID,
		}); err != nil {
			slog.Warn("metadata refresh: update tag fields failed", "songId", row.ID, "error", err)
		}
	}

	d.mu.Lock()
	d.progress.Processed++
	d.mu.Unlock()
}

func (d *MetadataRefresher) incFailed() {
	d.mu.Lock()
	d.progress.Failed++
	d.mu.Unlock()
}

var ErrMetadataRefreshRunning = fmt.Errorf("metadata refresh is already running")

// RefreshSong 对单首歌曲提取远程元数据并更新数据库。
// resolvedURL 非空时直接使用，否则内部解析。
// 内置 inflight 去重，同一 songID 不会并发执行。
func (d *MetadataRefresher) RefreshSong(ctx context.Context, song *models.Song, resolvedURL string) {
	if _, loaded := d.refreshInflight.LoadOrStore(song.ID, struct{}{}); loaded {
		return
	}
	defer d.refreshInflight.Delete(song.ID)

	url := resolvedURL
	if url == "" {
		var err error
		url, err = d.resolveURL(ctx, song)
		if err != nil {
			slog.Warn("auto refresh: resolve url failed", "songID", song.ID, "error", err)
			return
		}
	}

	probe, err := d.extractor.ProbeMetadataFromURL(ctx, url)
	if err != nil {
		slog.Warn("auto refresh: probe failed", "songID", song.ID, "error", err)
		return
	}

	coverPath := probe.CoverPath
	if coverPath == "" && song.CoverPath == "" && song.CoverURL == "" {
		if cp, err := d.extractor.ExtractCoverFromURL(ctx, url); err == nil {
			coverPath = cp
		}
	}

	if err := d.updateMeta(ctx, sqlc.UpdateSongMetadataParams{
		Column1:    probe.Duration,
		Duration:   probe.Duration,
		Column3:    int64(probe.BitRate),
		BitRate:    int64(probe.BitRate),
		Column5:    int64(probe.SampleRate),
		SampleRate: int64(probe.SampleRate),
		Column7:    probe.Format,
		Format:     probe.Format,
		Column9:    probe.Title,
		Title:      probe.Title,
		Column11:   probe.Artist,
		Artist:     probe.Artist,
		Column13:   probe.Album,
		Album:      probe.Album,
		Column15:   coverPath,
		CoverPath:  coverPath,
		ID:         song.ID,
	}); err != nil {
		slog.Warn("auto refresh: update metadata failed", "songID", song.ID, "error", err)
	}

	if d.updateTags != nil && (probe.Artist != "" || probe.Album != "" || (d.shouldOverrideTitle() && probe.Title != "")) {
		tagTitle := ""
		if d.shouldOverrideTitle() {
			tagTitle = probe.Title
		}
		if err := d.updateTags(ctx, sqlc.UpdateSongTagFieldsParams{
			Column1: tagTitle,
			Title:   tagTitle,
			Column3: probe.Artist,
			Artist:  probe.Artist,
			Column5: probe.Album,
			Album:   probe.Album,
			ID:      song.ID,
		}); err != nil {
			slog.Warn("auto refresh: update tag fields failed", "songID", song.ID, "error", err)
		}
	}

	slog.Info("auto refresh: metadata updated", "songID", song.ID,
		"title", probe.Title, "artist", probe.Artist, "duration", probe.Duration)
}

// RefreshSongFromFile 从本地文件提取元数据并更新数据库。
// 作为 FinalizeCache 的兜底路径，当 ProbeMetadataFromURL 失败时使用。
func (d *MetadataRefresher) RefreshSongFromFile(ctx context.Context, song *models.Song, filePath string) {
	if _, loaded := d.refreshInflight.LoadOrStore(song.ID, struct{}{}); loaded {
		return
	}
	defer d.refreshInflight.Delete(song.ID)

	metadata, err := d.extractor.Extract(ctx, filePath)
	if err != nil {
		slog.Warn("cache backfill: extract failed", "songID", song.ID, "error", err)
		return
	}

	coverPath := ""
	if metadata.HasCover && song.CoverPath == "" && song.CoverURL == "" {
		if cp, err := d.extractor.SaveCover(song.ID, metadata); err == nil {
			coverPath = cp
		}
	}

	if err := d.updateMeta(ctx, sqlc.UpdateSongMetadataParams{
		Column1:    metadata.Duration,
		Duration:   metadata.Duration,
		Column3:    int64(metadata.BitRate),
		BitRate:    int64(metadata.BitRate),
		Column5:    int64(metadata.SampleRate),
		SampleRate: int64(metadata.SampleRate),
		Column7:    metadata.Format,
		Format:     metadata.Format,
		Column9:    metadata.Title,
		Title:      metadata.Title,
		Column11:   metadata.Artist,
		Artist:     metadata.Artist,
		Column13:   metadata.Album,
		Album:      metadata.Album,
		Column15:   coverPath,
		CoverPath:  coverPath,
		ID:         song.ID,
	}); err != nil {
		slog.Warn("cache backfill: update metadata failed", "songID", song.ID, "error", err)
	}

	if d.updateTags != nil && (metadata.Artist != "" || metadata.Album != "" || (d.shouldOverrideTitle() && metadata.Title != "")) {
		tagTitle := ""
		if d.shouldOverrideTitle() {
			tagTitle = metadata.Title
		}
		if err := d.updateTags(ctx, sqlc.UpdateSongTagFieldsParams{
			Column1: tagTitle,
			Title:   tagTitle,
			Column3: metadata.Artist,
			Artist:  metadata.Artist,
			Column5: metadata.Album,
			Album:   metadata.Album,
			ID:      song.ID,
		}); err != nil {
			slog.Warn("cache backfill: update tag fields failed", "songID", song.ID, "error", err)
		}
	}

	slog.Info("cache backfill: metadata updated", "songID", song.ID)
}
