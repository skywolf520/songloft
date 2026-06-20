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

	listSongs  func(ctx context.Context) ([]sqlc.ListSongsNeedingMetadataRow, error)
	updateMeta func(ctx context.Context, params sqlc.UpdateSongMetadataParams) error
	resolveURL func(ctx context.Context, song *models.Song) (string, error)
	extractor  *MetadataExtractor
}

func NewMetadataRefresher(
	listSongs func(ctx context.Context) ([]sqlc.ListSongsNeedingMetadataRow, error),
	updateMeta func(ctx context.Context, params sqlc.UpdateSongMetadataParams) error,
	resolveURL func(ctx context.Context, song *models.Song) (string, error),
	extractor *MetadataExtractor,
) *MetadataRefresher {
	return &MetadataRefresher{
		progress:   MetadataRefreshProgress{Status: MetadataRefreshIdle},
		listSongs:  listSongs,
		updateMeta: updateMeta,
		resolveURL: resolveURL,
		extractor:  extractor,
	}
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
