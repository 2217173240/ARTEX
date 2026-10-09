package selfupdate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type githubFixtureTransport func(*http.Request) (*http.Response, error)

func (f githubFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestForkReleaseSourceAndNoRelease(t *testing.T) {
	client := &http.Client{Transport: githubFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/repos/2217173240/ARTEX/releases/latest" {
			t.Fatalf("unexpected release source: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	_, err := FetchLatest(context.Background(), client)
	if err == nil || !strings.Contains(err.Error(), "2217173240/ARTEX 尚未发布任何正式版本") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestForkReleasePackageAvailability(t *testing.T) {
	rel := Release{TagName: "v0.3.17", Assets: []Asset{{Name: "artex-0.3.17-linux-amd64.zip"}}}
	if _, ok := rel.FindAsset(AssetName(rel.TagName, "linux", "amd64")); !ok {
		t.Fatal("published package missing")
	}
	if _, ok := rel.FindAsset(AssetName(rel.TagName, "darwin", "arm64")); ok {
		t.Fatal("unpublished package treated as available")
	}
}
