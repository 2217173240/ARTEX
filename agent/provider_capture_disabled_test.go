package agent

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
)

func TestRequestSnapshotOnlyWhenRecording(t *testing.T) {
	for _, recording := range []bool{false, true} {
		name := "disabled"
		if recording {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			const body = `{"messages":[{"role":"user","content":"snapshot fixture"}]}`
			req, err := http.NewRequest(http.MethodPost, "https://example.invalid", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			originalGetBody := req.GetBody
			snapshots := 0
			req.GetBody = func() (io.ReadCloser, error) {
				snapshots++
				return originalGetBody()
			}
			var capture *llmrec.Capture
			if recording {
				ctx, capt := llmrec.NewCapture(req.Context())
				capture = capt
				req = req.WithContext(ctx)
			}
			transport := quotaAwareTransport{base: roundTripperFunc(func(forwarded *http.Request) (*http.Response, error) {
				got, err := io.ReadAll(forwarded.Body)
				if err != nil {
					return nil, err
				}
				if string(got) != body {
					t.Fatalf("forwarded body = %q, want %q", got, body)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})}
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			wantSnapshots := 0
			if recording {
				wantSnapshots = 1
				if capture.RawRequest() != body {
					t.Fatalf("recorded body = %q, want %q", capture.RawRequest(), body)
				}
			}
			if snapshots != wantSnapshots {
				t.Fatalf("request body copied %d times, want %d with recording=%v", snapshots, wantSnapshots, recording)
			}
		})
	}
}
