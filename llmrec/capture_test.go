package llmrec

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCaptureFromNilAndMissing(t *testing.T) {
	var nilCtx context.Context // the transport reaches here on any unrecorded call
	if CaptureFrom(nilCtx) != nil {
		t.Fatal("nil context should yield no capture")
	}
	if CaptureFrom(context.Background()) != nil {
		t.Fatal("bare context should yield no capture")
	}
}

// A nil *Capture is the "recording off" path: the transport calls these on every
// request, so they must all be no-ops rather than panics.
func TestNilCaptureIsInert(t *testing.T) {
	var c *Capture
	c.SetRequest("body")
	if got := c.RawRequest(); got != "" {
		t.Fatalf("RawRequest()=%q want empty", got)
	}
	if got := c.RawResponse(); got != "" {
		t.Fatalf("RawResponse()=%q want empty", got)
	}
	rc := io.NopCloser(strings.NewReader("x"))
	if c.TeeResponse(200, rc) != rc {
		t.Fatal("nil capture must pass the body through untouched")
	}
}

func TestCaptureSingleAttemptKeepsBytesVerbatim(t *testing.T) {
	ctx, c := NewCapture(context.Background())
	if CaptureFrom(ctx) != c {
		t.Fatal("capture not retrievable from its own context")
	}
	c.SetRequest(`{"model":"x"}`)
	// Retries re-send identical bytes; only the first is kept.
	c.SetRequest(`{"model":"ignored"}`)

	const sse = "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {}\n\n"
	body := c.TeeResponse(200, io.NopCloser(strings.NewReader(sse)))
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != sse {
		t.Fatal("tee altered the stream delivered to the caller")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if c.RawRequest() != `{"model":"x"}` {
		t.Fatalf("RawRequest()=%q", c.RawRequest())
	}
	// A lone attempt must stay byte-identical — no headers, no framing added, so
	// it can be replayed as-is.
	if c.RawResponse() != sse {
		t.Fatalf("RawResponse()=%q want verbatim SSE", c.RawResponse())
	}
}

// norma's doStream discards the bodies of retried attempts (retry.go closes them
// unread), so the capture is the only place a 429 body survives.
func TestCaptureRetriedAttemptsAreAllKept(t *testing.T) {
	_, c := NewCapture(context.Background())

	first := c.TeeResponse(429, io.NopCloser(strings.NewReader(`{"error":"rate_limit"}`)))
	if _, err := io.ReadAll(first); err != nil {
		t.Fatalf("read first: %v", err)
	}
	second := c.TeeResponse(200, io.NopCloser(strings.NewReader("data: ok\n")))
	if _, err := io.ReadAll(second); err != nil {
		t.Fatalf("read second: %v", err)
	}

	raw := c.RawResponse()
	if !strings.Contains(raw, `{"error":"rate_limit"}`) {
		t.Fatalf("dropped the retried attempt body: %q", raw)
	}
	if !strings.Contains(raw, "data: ok") {
		t.Fatalf("dropped the final attempt body: %q", raw)
	}
	if !strings.Contains(raw, "attempt 1/2 — HTTP 429") || !strings.Contains(raw, "attempt 2/2 — HTTP 200") {
		t.Fatalf("attempts not delimited: %q", raw)
	}
	if strings.Index(raw, "rate_limit") > strings.Index(raw, "data: ok") {
		t.Fatal("attempts stored out of order")
	}
}

func TestCaptureEmptyWhenNoHTTPHappened(t *testing.T) {
	_, c := NewCapture(context.Background())
	if c.RawRequest() != "" || c.RawResponse() != "" {
		t.Fatal("unused capture should be empty")
	}
}

func TestCaptureMetadataAndFullModes(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "metadata"}[metadataOnly], func(t *testing.T) {
			constructor := NewCapture
			if metadataOnly {
				constructor = NewMetadataCapture
			}
			ctx, c := constructor(context.Background())
			if CaptureFrom(ctx) != c {
				t.Fatal("capture missing from context")
			}
			c.SetRequest("synthetic-request-credential")
			const body = "synthetic-response-credential"
			unread := c.TeeResponse(429, io.NopCloser(strings.NewReader(body)))
			if err := unread.Close(); err != nil {
				t.Fatal(err)
			}
			partial := c.TeeResponse(401, io.NopCloser(strings.NewReader(body)))
			buf := make([]byte, 3)
			if _, err := io.ReadFull(partial, buf); err != nil {
				t.Fatal(err)
			}
			if string(buf) != body[:3] {
				t.Fatal("capture changed response bytes")
			}
			if err := partial.Close(); err != nil {
				t.Fatal(err)
			}
			full := c.TeeResponse(200, io.NopCloser(strings.NewReader(body)))
			got, err := io.ReadAll(full)
			if err != nil || string(got) != body {
				t.Fatalf("response changed: %q, %v", got, err)
			}
			if err := full.Close(); err != nil {
				t.Fatal(err)
			}
			attempts := c.Attempts()
			if len(attempts) != 3 || attempts[0].Status != 429 || attempts[1].Status != 401 || attempts[2].Status != 200 {
				t.Fatalf("attempt metadata: %+v", attempts)
			}
			for i, want := range []int64{0, 3, int64(len(body))} {
				if attempts[i].BytesRead != want {
					t.Fatalf("attempt %d consumed %d bytes, want %d", i, attempts[i].BytesRead, want)
				}
				if metadataOnly && (attempts[i].Body != "" || c.attempts[i].body.Len() != 0) {
					t.Fatal("metadata capture retained response bytes")
				}
			}
			if metadataOnly {
				if c.request != "" || c.RawRequest() != "" || c.RawResponse() != "" {
					t.Fatal("metadata capture retained raw content")
				}
			} else if c.RawRequest() != "synthetic-request-credential" || attempts[1].Body != body[:3] || attempts[2].Body != body || !strings.Contains(c.RawResponse(), body) {
				t.Fatal("full capture did not retain original content")
			}
		})
	}
}
