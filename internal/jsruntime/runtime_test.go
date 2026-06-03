package jsruntime

import (
	"context"
	"testing"
	"time"
)

func TestProcessTimers_ReturnsTrue_WhenTimerFires(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-timer-fire"
	pluginID := int64(1)

	// Create environment with a timer that fires immediately
	code := polyfillJS + `
		var fired = false;
		setTimeout(function(){ fired = true; }, 0);
	`
	if err := manager.CreateEnv(envID, code, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	// Process timers - should return true because timer fires
	time.Sleep(10 * time.Millisecond) // Give timer a chance to be ready
	didFire := manager.ProcessTimers(envID)

	if !didFire {
		t.Error("Expected ProcessTimers to return true when timer fires")
	}

	// Verify the timer actually executed
	result, err := manager.ExecuteJS(context.Background(), envID, "fired", 1000)
	if err != nil {
		t.Fatalf("Failed to check fired variable: %v", err)
	}

	if result.Result != "true" {
		t.Errorf("Expected timer to have fired, got fired=%s", result.Result)
	}
}

func TestProcessTimers_ReturnsFalse_WhenNoTimerFires(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-no-timer-fire"
	pluginID := int64(1)

	// Create environment with no timers
	code := polyfillJS
	if err := manager.CreateEnv(envID, code, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	// Process timers - should return false because no timers exist
	didFire := manager.ProcessTimers(envID)

	if didFire {
		t.Error("Expected ProcessTimers to return false when no timers exist")
	}
}

func TestGetNextTimerDeadline_NoTimers(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-next-empty"
	pluginID := int64(1)

	if err := manager.CreateEnv(envID, polyfillJS, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	deadline := manager.GetNextTimerDeadline(envID)
	if !deadline.IsZero() {
		t.Errorf("expected zero time when no timers, got %v", deadline)
	}
}

func TestGetNextTimerDeadline_SingleTimer(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-next-single"
	pluginID := int64(1)

	if err := manager.CreateEnv(envID, polyfillJS, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	before := time.Now()
	if _, err := manager.ExecuteJS(context.Background(), envID, "setTimeout(function(){}, 60000);", 1000); err != nil {
		t.Fatalf("setTimeout failed: %v", err)
	}

	deadline := manager.GetNextTimerDeadline(envID)
	if deadline.IsZero() {
		t.Fatal("expected non-zero deadline after setTimeout")
	}

	// 期望 deadline 大约在 before+60s 附近（容差 5s 处理 CI 延迟）
	expectedMin := before.Add(55 * time.Second)
	expectedMax := before.Add(65 * time.Second)
	if deadline.Before(expectedMin) || deadline.After(expectedMax) {
		t.Errorf("deadline %v outside expected range [%v, %v]", deadline, expectedMin, expectedMax)
	}
}

func TestGetNextTimerDeadline_PicksEarliest(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-next-earliest"
	pluginID := int64(1)

	if err := manager.CreateEnv(envID, polyfillJS, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	before := time.Now()
	// 先注册一个 60s 的，再注册一个 10s 的，再注册一个 120s 的；期望选 10s 的。
	code := `
		setTimeout(function(){}, 60000);
		setTimeout(function(){}, 10000);
		setTimeout(function(){}, 120000);
	`
	if _, err := manager.ExecuteJS(context.Background(), envID, code, 1000); err != nil {
		t.Fatalf("setTimeout chain failed: %v", err)
	}

	deadline := manager.GetNextTimerDeadline(envID)
	if deadline.IsZero() {
		t.Fatal("expected non-zero deadline")
	}

	expectedMin := before.Add(5 * time.Second)
	expectedMax := before.Add(15 * time.Second)
	if deadline.Before(expectedMin) || deadline.After(expectedMax) {
		t.Errorf("deadline %v not picking the earliest (~10s) timer, expected in [%v, %v]",
			deadline, expectedMin, expectedMax)
	}
}

func TestGetNextTimerDeadline_IncludesInterval(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-next-interval"
	pluginID := int64(1)

	if err := manager.CreateEnv(envID, polyfillJS, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	if _, err := manager.ExecuteJS(context.Background(), envID, "setInterval(function(){}, 30000);", 1000); err != nil {
		t.Fatalf("setInterval failed: %v", err)
	}

	deadline := manager.GetNextTimerDeadline(envID)
	if deadline.IsZero() {
		t.Error("expected non-zero deadline for setInterval")
	}
}

func TestProcessTimers_ReturnsFalse_WhenTimerNotYetExpired(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-timer-not-expired"
	pluginID := int64(1)

	// Create environment with a timer that won't fire for a while
	code := polyfillJS + `
		setTimeout(function(){}, 10000);
	`
	if err := manager.CreateEnv(envID, code, pluginID); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	// Process timers immediately - should return false because timer hasn't expired
	didFire := manager.ProcessTimers(envID)

	if didFire {
		t.Error("Expected ProcessTimers to return false when timer hasn't expired yet")
	}
}

// TestExecuteJS_CtxCancel 验证客户端取消 ctx 时 ExecuteJS 立即返回（issue #79 的核心）。
// 构造一个永不 resolve 的 Promise（依赖 setTimeout 但 ts 远大于测试时长），
// 然后 cancel ctx，断言 ExecuteJS 在远小于 timeoutMs 的时间内返回 context.Canceled。
func TestExecuteJS_CtxCancel(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-ctx-cancel"
	if err := manager.CreateEnv(envID, polyfillJS, 1); err != nil {
		t.Fatalf("CreateEnv: %v", err)
	}
	defer manager.DestroyEnv(envID)

	ctx, cancel := context.WithCancel(context.Background())

	// 200ms 后取消
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// 60s 永不 resolve（实际不会等这么久）
	_, err := manager.ExecuteJS(ctx, envID,
		`new Promise(function(resolve){ setTimeout(resolve, 60000); })`,
		60000)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from canceled ctx, got nil")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("ExecuteJS took %v after ctx cancel; expected sub-second", elapsed)
	}

	// 验证 env 仍可用：下一次 ExecuteJS 不应因前一次取消导致 deadlock 或异常
	res, err2 := manager.ExecuteJS(context.Background(), envID, "1+1", 1000)
	if err2 != nil {
		t.Fatalf("post-cancel ExecuteJS failed: %v", err2)
	}
	if res.Result != "2" {
		t.Errorf("post-cancel eval expected 2, got %q", res.Result)
	}
}

// TestExecuteJS_CtxAlreadyCanceled 即使 ctx 进入时已取消，也应快速返回 context.Canceled。
func TestExecuteJS_CtxAlreadyCanceled(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-ctx-pre-cancel"
	if err := manager.CreateEnv(envID, polyfillJS, 1); err != nil {
		t.Fatalf("CreateEnv: %v", err)
	}
	defer manager.DestroyEnv(envID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := manager.ExecuteJS(ctx, envID,
		`new Promise(function(resolve){ setTimeout(resolve, 60000); })`,
		60000)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from pre-canceled ctx, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("ExecuteJS took %v with pre-canceled ctx; expected near-instant", elapsed)
	}
}

// --- URL Polyfill 测试 ---

func TestURLPolyfill_AbsoluteURL(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-url-absolute"
	code := polyfillJS + `
		var u = new URL('https://example.com:8080/path/to?q=1#frag');
		var result = JSON.stringify({
			protocol: u.protocol,
			host: u.host,
			hostname: u.hostname,
			port: u.port,
			pathname: u.pathname,
			search: u.search,
			hash: u.hash,
			origin: u.origin
		});
	`
	if err := manager.CreateEnv(envID, code, 1); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	res, err := manager.ExecuteJS(context.Background(), envID, "result", 1000)
	if err != nil {
		t.Fatalf("ExecuteJS failed: %v", err)
	}

	expected := `{"protocol":"https:","host":"example.com:8080","hostname":"example.com","port":"8080","pathname":"/path/to","search":"?q=1","hash":"#frag","origin":"https://example.com:8080"}`
	if res.Result != expected {
		t.Errorf("got %s\nwant %s", res.Result, expected)
	}
}

func TestURLPolyfill_RelativeWithBase(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-url-relative-base"
	code := polyfillJS + `
		var u1 = new URL('/path', 'https://example.com/dir/file');
		var u2 = new URL('sub', 'https://example.com/dir/');
		var r1 = u1.href;
		var r2 = u2.href;
	`
	if err := manager.CreateEnv(envID, code, 1); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	res1, _ := manager.ExecuteJS(context.Background(), envID, "r1", 1000)
	if res1.Result != "https://example.com/dir/path" {
		t.Errorf("relative with base '/path': got %s", res1.Result)
	}

	res2, _ := manager.ExecuteJS(context.Background(), envID, "r2", 1000)
	if res2.Result != "https://example.com/dir//sub" {
		t.Errorf("relative with base 'sub': got %s", res2.Result)
	}
}

func TestURLPolyfill_RelativeWithoutBase_ThrowsTypeError(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-url-relative-throws"
	code := polyfillJS + `
		var caught = false;
		var errorName = '';
		try {
			new URL('/relative/path');
		} catch(e) {
			caught = true;
			errorName = e.constructor.name || '';
		}
	`
	if err := manager.CreateEnv(envID, code, 1); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	res, _ := manager.ExecuteJS(context.Background(), envID, "caught", 1000)
	if res.Result != "true" {
		t.Error("new URL('/relative/path') should throw, but did not")
	}

	res2, _ := manager.ExecuteJS(context.Background(), envID, "errorName", 1000)
	if res2.Result != "TypeError" {
		t.Errorf("expected TypeError, got %s", res2.Result)
	}
}

func TestURLPolyfill_TryCatchDetectionPattern(t *testing.T) {
	manager := NewJSEnvManager()
	defer manager.SignalShutdown()

	envID := "test-url-trycatch-pattern"
	code := polyfillJS + `
		function isAbsoluteURL(path) {
			try { new URL(path); return true; }
			catch(e) { return false; }
		}
		var absResult = isAbsoluteURL('https://example.com/file.mp3');
		var relResult = isAbsoluteURL('/music/file.mp3');
		var bareResult = isAbsoluteURL('file.mp3');
	`
	if err := manager.CreateEnv(envID, code, 1); err != nil {
		t.Fatalf("CreateEnv failed: %v", err)
	}
	defer manager.DestroyEnv(envID)

	res1, _ := manager.ExecuteJS(context.Background(), envID, "absResult", 1000)
	if res1.Result != "true" {
		t.Error("absolute URL should return true")
	}
	res2, _ := manager.ExecuteJS(context.Background(), envID, "relResult", 1000)
	if res2.Result != "false" {
		t.Error("relative path should return false")
	}
	res3, _ := manager.ExecuteJS(context.Background(), envID, "bareResult", 1000)
	if res3.Result != "false" {
		t.Error("bare filename should return false")
	}
}
