package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeChannel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty defaults to release", input: "", want: ChannelRelease},
		{name: "release", input: "release", want: ChannelRelease},
		{name: "latest alias", input: "latest", want: ChannelRelease},
		{name: "main", input: "main", want: ChannelMain},
		{name: "master alias", input: "master", want: ChannelMain},
		{name: "invalid", input: "nightly", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeChannel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeChannel returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeChannel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectAssetChoosesPlatformTarball(t *testing.T) {
	assets := []githubAsset{
		{Name: "clanker_v1.2.3_darwin_amd64.tar.gz", BrowserDownloadURL: "https://example.com/amd64"},
		{Name: "clanker_v1.2.3_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/arm64"},
	}

	got, err := SelectAsset(assets, "darwin", "arm64")
	if err != nil {
		t.Fatalf("SelectAsset returned error: %v", err)
	}
	if got.BrowserDownloadURL != "https://example.com/arm64" {
		t.Fatalf("selected %q, want arm64 asset", got.BrowserDownloadURL)
	}
}

func TestSelectAssetRejectsMissingPlatformTarball(t *testing.T) {
	_, err := SelectAsset([]githubAsset{
		{Name: "clanker_v1.2.3_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/arm64"},
	}, "linux", "arm64")
	if err == nil {
		t.Fatal("expected missing asset error")
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	const want = "fake binary"
	tarball := makeTarGz(t, "clanker", want)

	got, err := extractBinaryFromTarGz(bytes.NewReader(tarball))
	if err != nil {
		t.Fatalf("extractBinaryFromTarGz returned error: %v", err)
	}
	if string(got) != want {
		t.Fatalf("extracted %q, want %q", string(got), want)
	}
}

func TestUpdateMainUsesRepositoryDefaultBranch(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{}`
		switch req.URL.Path {
		case "/repos/bgdnvk/clanker":
			body = `{"default_branch":"master"}`
		case "/repos/bgdnvk/clanker/branches/master":
			body = `{"commit":{"sha":"1234567890abcdef"}}`
		default:
			t.Fatalf("unexpected request path: %s", req.URL.Path)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	result, err := Update(context.Background(), Options{
		Channel:     ChannelMain,
		HTTPClient:  client,
		DryRun:      true,
		InstallPath: "/tmp/clanker",
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if result.TargetSHA != "1234567890abcdef" {
		t.Fatalf("TargetSHA = %q, want repository default branch SHA", result.TargetSHA)
	}
	if !strings.HasSuffix(result.SourceURL, "/tree/master") {
		t.Fatalf("SourceURL = %q, want master branch URL", result.SourceURL)
	}
}

func makeTarGz(t *testing.T, name string, content string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
