package httputil

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultHeadSize = 256 * 1024 // 256KB，覆盖大多数格式的头部标签+封面
	defaultTailSize = 32 * 1024  // 32KB，覆盖 ID3v1 (128B) 和 APEv2 footer
)

var ErrGapRead = errors.New("read position falls in unfetched gap between head and tail buffers")

// HTTPReadSeeker 通过 HTTP Range 请求预取文件首尾数据，实现 io.ReadSeeker。
// 适用于只需读取文件头部和尾部的场景（如音频标签解析）。
type HTTPReadSeeker struct {
	head    []byte
	tail    []byte
	size    int64
	pos     int64
	tailOff int64 // tail 缓冲区在文件中的起始偏移
}

// NewHTTPReadSeeker 创建 HTTPReadSeeker。
// 发起 HEAD 请求获取文件大小，再通过 Range GET 预取首尾数据。
// 若服务端不支持 Range 请求，返回错误（调用方应 fallback）。
func NewHTTPReadSeeker(client *http.Client, url string) (*HTTPReadSeeker, error) {
	size, err := fetchContentLength(client, url)
	if err != nil {
		return nil, fmt.Errorf("http read seeker: %w", err)
	}
	if size <= 0 {
		return nil, fmt.Errorf("http read seeker: unknown content length")
	}

	rs := &HTTPReadSeeker{size: size}

	if size <= int64(defaultHeadSize) {
		// 文件很小，一次性全部获取
		data, err := fetchRange(client, url, 0, size-1)
		if err != nil {
			return nil, fmt.Errorf("http read seeker: fetch small file: %w", err)
		}
		rs.head = data
		rs.tailOff = size // 无独立 tail
		return rs, nil
	}

	// 并行获取首尾（串行实现，简单可靠）
	head, err := fetchRange(client, url, 0, int64(defaultHeadSize)-1)
	if err != nil {
		return nil, fmt.Errorf("http read seeker: fetch head: %w", err)
	}
	rs.head = head

	tailStart := size - int64(defaultTailSize)
	if tailStart < int64(defaultHeadSize) {
		tailStart = int64(defaultHeadSize)
	}
	tail, err := fetchRange(client, url, tailStart, size-1)
	if err != nil {
		return nil, fmt.Errorf("http read seeker: fetch tail: %w", err)
	}
	rs.tail = tail
	rs.tailOff = tailStart

	return rs, nil
}

func (r *HTTPReadSeeker) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}

	total := 0
	for len(p) > 0 && r.pos < r.size {
		n, err := r.readAt(p, r.pos)
		r.pos += int64(n)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (r *HTTPReadSeeker) readAt(p []byte, off int64) (int, error) {
	headEnd := int64(len(r.head))

	// 在 head 范围内
	if off < headEnd {
		n := copy(p, r.head[off:])
		return n, nil
	}

	// 在 tail 范围内
	if off >= r.tailOff && len(r.tail) > 0 {
		idx := off - r.tailOff
		if idx >= int64(len(r.tail)) {
			return 0, io.EOF
		}
		n := copy(p, r.tail[idx:])
		return n, nil
	}

	// 在间隙中
	return 0, ErrGapRead
}

func (r *HTTPReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = r.pos + offset
	case io.SeekEnd:
		newPos = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if newPos < 0 {
		return 0, fmt.Errorf("negative seek position: %d", newPos)
	}
	r.pos = newPos
	return newPos, nil
}

// Size 返回文件总大小。
func (r *HTTPReadSeeker) Size() int64 {
	return r.size
}

func fetchContentLength(client *http.Client, url string) (int64, error) {
	resp, err := client.Head(url)
	if err != nil {
		return 0, fmt.Errorf("HEAD request failed: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD returned status %d", resp.StatusCode)
	}

	cl := resp.Header.Get("Content-Length")
	if cl == "" {
		return 0, fmt.Errorf("no Content-Length header")
	}

	size, err := strconv.ParseInt(cl, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid Content-Length %q: %w", cl, err)
	}
	return size, nil
}

func fetchRange(client *http.Client, url string, start, end int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("range request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// 服务端忽略了 Range，返回了完整内容
		if resp.ContentLength > 0 && resp.ContentLength == end-start+1 {
			return io.ReadAll(resp.Body)
		}
		// 检查是否真的不支持 Range
		if !strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes") &&
			resp.Header.Get("Content-Range") == "" {
			return nil, fmt.Errorf("server does not support range requests")
		}
		return io.ReadAll(io.LimitReader(resp.Body, end-start+1))
	}

	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("unexpected status %d for range request", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}
