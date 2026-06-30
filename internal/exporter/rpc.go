package exporter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// dialRPC connects to the JSON-RPC endpoint with optional Bearer-token auth and
// a transport that transparently retries rate-limited (HTTP 429) and transient
// 5xx responses. Because the retry lives at the HTTP transport layer, every RPC
// call — ethclient balance lookups and all abigen contract calls alike — gets
// the same backoff behaviour without touching individual call sites.
func dialRPC(ctx context.Context, url, token string, maxRetries int, logger *slog.Logger) (*ethclient.Client, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()

	httpClient := &http.Client{
		// Bounds the whole exchange (including retries) so a hung endpoint can
		// never block a scrape indefinitely. abigen calls pass a nil CallOpts,
		// i.e. context.Background(), so this timeout is their only guard.
		Timeout: 30 * time.Second,
		Transport: &retryRoundTripper{
			base:       base,
			token:      token,
			maxRetries: maxRetries,
			logger:     logger,
		},
	}

	rpcClient, err := rpc.DialOptions(ctx, url, rpc.WithHTTPClient(httpClient))
	if err != nil {
		return nil, err
	}
	return ethclient.NewClient(rpcClient), nil
}

// retryRoundTripper adds the Authorization header (if a token is set) and
// retries on 429 / 5xx with exponential backoff + jitter, honouring Retry-After.
type retryRoundTripper struct {
	base       http.RoundTripper
	token      string
	maxRetries int
	logger     *slog.Logger
}

const (
	retryBaseDelay = 500 * time.Millisecond
	retryMaxDelay  = 30 * time.Second
)

func (rt *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Buffer the body so each attempt can re-send it (JSON-RPC is a POST).
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
	}

	delay := retryBaseDelay
	for attempt := 0; ; attempt++ {
		// Clone per attempt so we don't mutate the caller's request between tries.
		attemptReq := req.Clone(req.Context())
		if bodyBytes != nil {
			attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			attemptReq.ContentLength = int64(len(bodyBytes))
		}
		if rt.token != "" {
			attemptReq.Header.Set("Authorization", "Bearer "+rt.token)
		}

		resp, err := rt.base.RoundTrip(attemptReq)

		retryable := err != nil || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !retryable || attempt >= rt.maxRetries {
			// Return the final result (success, or the last error/429 so the
			// caller still sees the real failure once retries are exhausted).
			return resp, err
		}

		wait := backoffDelay(delay)
		if resp != nil {
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				wait = ra
			}
			// Drain + close so the connection can be reused.
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}

		if rt.logger != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			rt.logger.Debug("retrying RPC request",
				"attempt", attempt+1, "max", rt.maxRetries, "status", status,
				"wait_ms", wait.Milliseconds(), "err", err)
		}

		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}

		delay = min(delay*2, retryMaxDelay)
	}
}

// backoffDelay adds up to 50% jitter to spread out concurrent retries and avoid
// a thundering herd when many goroutines hit 429 at once.
func backoffDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d + time.Duration(rand.Int63n(int64(d/2)+1))
}

// parseRetryAfter parses the integer-seconds form of the Retry-After header.
// HTTP-date form is ignored (falls back to exponential backoff). Capped to keep
// a misbehaving server from stalling a scrape.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, retryMaxDelay)
}
