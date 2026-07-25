package youtubepot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

func validRequestFixture(contextValue Context) Request {
	return Request{Context: contextValue, Client: "ANDROID", VisitorData: "visitor", VideoID: "fixture0001", PlayerURL: "https://www.youtube.com/s/player/fixture/base.js"}
}

type panickingNameProvider struct{}

func (*panickingNameProvider) Name() string { panic("provider name secret") }
func (*panickingNameProvider) Provide(context.Context, Request) (Response, error) {
	return Response{}, nil
}

func TestDirectorProviderFallbackCacheExpiryAndBypass(t *testing.T) {
	clock := &fixedClock{now: time.Unix(1_700_000_000, 0)}
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, CacheSize: 2, Clock: clock, RefreshSkew: time.Second, Providers: []Provider{
		ProviderFunc{ProviderName: "reject", Function: func(context.Context, Request) (Response, error) { return Response{}, ErrRejected }},
		ProviderFunc{ProviderName: "fixture", Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			return Response{Token: "Zm9v", ExpiresAt: clock.now.Add(time.Minute)}, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextGVS)
	for index := 0; index < 2; index++ {
		token, ok, err := director.Resolve(context.Background(), request, true)
		if err != nil || !ok || token != "Zm9v" {
			t.Fatalf("resolve = %q %v %v", token, ok, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
	request.BypassCache = true
	if _, _, err := director.Resolve(context.Background(), request, true); err != nil || calls.Load() != 2 {
		t.Fatalf("bypass calls=%d err=%v", calls.Load(), err)
	}
	request.BypassCache = false
	clock.now = clock.now.Add(2 * time.Minute)
	if _, _, err := director.Resolve(context.Background(), request, true); err != nil || calls.Load() != 3 {
		t.Fatalf("expiry calls=%d err=%v", calls.Load(), err)
	}
}

func TestDirectorExpirySkewRefreshesBeforeHardExpiry(t *testing.T) {
	clock := &fixedClock{now: time.Unix(1_700_000_000, 0)}
	var calls atomic.Int32
	director, err := New(Config{
		Policy: FetchAlways, Clock: clock, RefreshSkew: 30 * time.Second,
		Providers: []Provider{ProviderFunc{ProviderName: "fixture", Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			return Response{Token: "Zm9v", ExpiresAt: clock.now.Add(40 * time.Second)}, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextGVS)
	if _, _, err := director.Resolve(context.Background(), request, true); err != nil || calls.Load() != 1 {
		t.Fatalf("initial resolve calls=%d err=%v", calls.Load(), err)
	}
	clock.now = clock.now.Add(15 * time.Second)
	if _, _, err := director.Resolve(context.Background(), request, true); err != nil || calls.Load() != 2 {
		t.Fatalf("skew refresh calls=%d err=%v", calls.Load(), err)
	}
}

func TestDirectorRejectsInvalidRefreshSkew(t *testing.T) {
	if _, err := New(Config{RefreshSkew: MaxRefreshSkew + time.Second}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversize skew err=%v", err)
	}
	if _, err := New(Config{RefreshSkew: -time.Second}); !errors.Is(err, ErrLimit) {
		t.Fatalf("negative skew err=%v", err)
	}
}

func TestDirectorSingleFlightSharesCompatibleIdentity(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(ctx context.Context, _ Request) (Response, error) {
			calls.Add(1)
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Response{}, ctx.Err()
			}
			return Response{Token: "Zm9v"}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextGVS)
	var wait sync.WaitGroup
	results := make(chan string, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			token, ok, err := director.Resolve(context.Background(), request, true)
			if err != nil || !ok {
				t.Errorf("resolve err=%v ok=%v", err, ok)
				return
			}
			results <- token
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d want 1", calls.Load())
	}
	for token := range results {
		if token != "Zm9v" {
			t.Fatalf("token=%q", token)
		}
	}
}

func TestDirectorSingleFlightIncompatibleIdentitiesDoNotShare(t *testing.T) {
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return Response{Token: "Zm9v"}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	audio := validRequestFixture(ContextGVS)
	video := validRequestFixture(ContextGVS)
	video.Client = "WEB"
	if CacheKey(audio) == CacheKey(video) {
		t.Fatal("expected incompatible cache keys")
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); _, _, _ = director.Resolve(context.Background(), audio, true) }()
	go func() { defer wait.Done(); _, _, _ = director.Resolve(context.Background(), video, true) }()
	wait.Wait()
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d want 2", calls.Load())
	}
}

func TestDirectorCancelledWaiterDoesNotBreakFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(ctx context.Context, _ Request) (Response, error) {
			calls.Add(1)
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Response{}, ctx.Err()
			}
			return Response{Token: "Zm9v"}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextGVS)
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		token, ok, err := director.Resolve(context.Background(), request, true)
		if err != nil || !ok || token != "Zm9v" {
			t.Errorf("leader resolve=%q ok=%v err=%v", token, ok, err)
		}
	}()
	<-started
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := director.Resolve(cancelCtx, request, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err=%v", err)
	}
	close(release)
	<-leaderDone
	token, ok, err := director.Resolve(context.Background(), request, true)
	if err != nil || !ok || token != "Zm9v" {
		t.Fatalf("follow-up resolve=%q ok=%v err=%v", token, ok, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}

func TestEpisodeForcedRefreshCapsAndCompatibleShare(t *testing.T) {
	clock := &fixedClock{now: time.Unix(1_700_000_000, 0)}
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, Clock: clock, Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, Request) (Response, error) {
			n := calls.Add(1)
			return Response{Token: base64Token(byte('A' + byte(n-1))), ExpiresAt: clock.now.Add(time.Hour)}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	episode := NewEpisode(2)
	request := validRequestFixture(ContextGVS)
	first, _, err := director.ResolveEpisode(context.Background(), request, true, false, episode)
	if err != nil || first == "" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	if err := episode.SignalRejection(ErrTokenRejected); err != nil {
		t.Fatal(err)
	}
	second, _, err := director.ResolveEpisode(context.Background(), request, true, false, episode)
	if err != nil || second == first || calls.Load() != 2 {
		t.Fatalf("forced second=%q first=%q calls=%d err=%v", second, first, calls.Load(), err)
	}
	audio := request
	video := request
	if CacheKey(audio) != CacheKey(video) {
		t.Fatal("compatible A/V must share identity")
	}
	third, _, err := director.ResolveEpisode(context.Background(), video, true, false, episode)
	if err != nil || third != second || calls.Load() != 2 {
		t.Fatalf("shared cache third=%q calls=%d err=%v", third, calls.Load(), err)
	}
	if err := episode.SignalRejection(ErrTokenRejected); err != nil {
		t.Fatal(err)
	}
	fourth, _, err := director.ResolveEpisode(context.Background(), request, true, false, episode)
	if err != nil || fourth == second || calls.Load() != 3 {
		t.Fatalf("second forced fourth=%q calls=%d err=%v", fourth, calls.Load(), err)
	}
	if err := episode.SignalRejection(ErrTokenRejected); !errors.Is(err, ErrForcedRefreshBudget) {
		t.Fatalf("budget err=%v", err)
	}
	if err := episode.SignalRejection(errors.New("untyped")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("untyped rejection err=%v", err)
	}
}

func TestDirectorPolicyRequiredCancellationPanicAndRedaction(t *testing.T) {
	auto, err := New(Config{Providers: []Provider{ProviderFunc{ProviderName: "panic", Function: func(context.Context, Request) (Response, error) { panic("secret-token") }}}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextPlayer)
	if token, ok, err := auto.Resolve(context.Background(), request, false); err != nil || ok || token != "" {
		t.Fatalf("optional auto resolve = %q %v %v", token, ok, err)
	}
	if _, _, err := auto.Resolve(context.Background(), request, true); !errors.Is(err, ErrUnavailable) || fmt.Sprint(err) != ErrUnavailable.Error() {
		t.Fatalf("required panic error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := auto.Resolve(ctx, request, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if text := fmt.Sprintf("%v %#v %v %#v", request, request, Response{Token: "secret-token"}, Response{Token: "secret-token"}); strings.Contains(text, "secret-token") || strings.Contains(text, "visitor") {
		t.Fatalf("secret leaked through formatting: %s", text)
	}
}

func TestDirectorMalformedProviderOutputRecovered(t *testing.T) {
	var calls atomic.Int32
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{
		ProviderFunc{ProviderName: "bad", Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			return Response{Token: "not base64!!"}, nil
		}},
		ProviderFunc{ProviderName: "good", Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			return Response{Token: "Zm9v"}, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	token, ok, err := director.Resolve(context.Background(), validRequestFixture(ContextGVS), true)
	if err != nil || !ok || token != "Zm9v" || calls.Load() != 2 {
		t.Fatalf("token=%q ok=%v calls=%d err=%v", token, ok, calls.Load(), err)
	}
}

func TestDirectorFetchPolicies(t *testing.T) {
	request := validRequestFixture(ContextPlayer)
	for _, test := range []struct {
		name     string
		policy   FetchPolicy
		required bool
		wantCall bool
		wantOK   bool
		wantErr  error
	}{
		{name: "never-required", policy: FetchNever, required: true, wantErr: ErrUnavailable},
		{name: "auto-optional", policy: FetchAuto},
		{name: "auto-required", policy: FetchAuto, required: true, wantCall: true, wantOK: true},
		{name: "always-optional", policy: FetchAlways, wantCall: true, wantOK: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			director, err := New(Config{Policy: test.policy, Providers: []Provider{ProviderFunc{
				ProviderName: "fixture",
				Function: func(context.Context, Request) (Response, error) {
					called = true
					return Response{Token: "Zm9v"}, nil
				},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			token, ok, err := director.Resolve(context.Background(), request, test.required)
			if !errors.Is(err, test.wantErr) || called != test.wantCall || ok != test.wantOK {
				t.Fatalf("resolve token=%q ok=%v called=%v error=%v", token, ok, called, err)
			}
		})
	}
}

func TestDirectorRecommendedPolicyFetchesWithoutRequiringSuccess(t *testing.T) {
	var calls atomic.Int32
	director, err := New(Config{Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, Request) (Response, error) {
			calls.Add(1)
			return Response{}, ErrRejected
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextPlayer)
	if token, ok, err := director.ResolvePolicy(context.Background(), request, false, true); err != nil || ok || token != "" || calls.Load() != 1 {
		t.Fatalf("recommended resolve = %q %v %v calls=%d", token, ok, err, calls.Load())
	}
}

func TestDirectorRequiredMissSanitizesProviderError(t *testing.T) {
	const secret = "secret-provider-detail"
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, Request) (Response, error) {
			return Response{}, errors.New(secret)
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	request := validRequestFixture(ContextGVS)
	_, _, err = director.Resolve(context.Background(), request, true)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("provider error leaked: %v", err)
	}
}

func TestValidationBoundsAndTokenNormalization(t *testing.T) {
	for _, token := range []string{"Zm9v", "Zm9v==", "a?b", "a b", "%61", "", strings.Repeat("a", MaxTokenBytes+1)} {
		normalized, err := NormalizeToken(token)
		valid := token == "Zm9v"
		if valid && (err != nil || normalized != "Zm9v") || !valid && err == nil {
			t.Fatalf("NormalizeToken(%q) = %q, %v", token, normalized, err)
		}
	}
	if _, err := New(Config{CacheSize: MaxCacheItems + 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("cache limit error = %v", err)
	}
	for _, providers := range [][]Provider{
		{(*panickingNameProvider)(nil)},
		{&panickingNameProvider{}},
		{ProviderFunc{ProviderName: "fixture"}, ProviderFunc{ProviderName: "fixture"}},
	} {
		if _, err := New(Config{Providers: providers}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("unsafe provider error = %v", err)
		}
	}
	director, err := New(Config{Policy: FetchAlways, Providers: []Provider{ProviderFunc{ProviderName: "fixture", Function: func(context.Context, Request) (Response, error) { return Response{Token: "Zm9v"}, nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := validRequestFixture(ContextPlayer)
	invalid.VideoID = "bad"
	if _, _, err := director.Resolve(context.Background(), invalid, true); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid request error = %v", err)
	}
}

func TestConcurrentResolveIsSafeAndBounded(t *testing.T) {
	director, err := New(Config{Policy: FetchAlways, CacheSize: 4, Providers: []Provider{ProviderFunc{ProviderName: "fixture", Function: func(context.Context, Request) (Response, error) { return Response{Token: "Zm9v"}, nil }}}})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := validRequestFixture(ContextGVS)
			request.VideoID = fmt.Sprintf("fixture%04d", index)
			_, _, _ = director.Resolve(context.Background(), request, true)
		}(index)
	}
	wait.Wait()
	director.mu.Lock()
	defer director.mu.Unlock()
	if len(director.entries) > 4 {
		t.Fatalf("cache entries = %d", len(director.entries))
	}
}

func FuzzNormalizeToken(f *testing.F) {
	f.Add("Zm9v")
	f.Add("token?pot=secret")
	f.Add("")
	f.Fuzz(func(t *testing.T, token string) {
		normalized, err := NormalizeToken(token)
		if err == nil {
			if normalized == "" || len(normalized) > MaxTokenBytes || strings.ContainsAny(normalized, "?&#% \r\n\t") {
				t.Fatalf("unsafe normalized token %q", normalized)
			}
		}
	})
}

func base64Token(b byte) string {
	token, err := NormalizeToken(base64.RawURLEncoding.EncodeToString([]byte{b}))
	if err != nil {
		panic(err)
	}
	return token
}
