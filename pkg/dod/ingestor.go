package dod

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPGetter defines the HTTP request interface for fetching remote repository data.
type HTTPGetter interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// DefaultHTTPGetter implements HTTPGetter with timeouts.
type DefaultHTTPGetter struct {
	client *http.Client
}

// NewDefaultHTTPGetter creates a standard HTTP getter with a 30s timeout.
func NewDefaultHTTPGetter() *DefaultHTTPGetter {
	return &DefaultHTTPGetter{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Get performs an HTTP GET request and returns response body bytes.
func (g *DefaultHTTPGetter) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating request for %s: %w", url, err)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP error %d fetching %s", resp.StatusCode, url)
	}

	// #nosec G110 -- reading bounded remote metadata stream
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed reading response from %s: %w", url, err)
	}
	return data, nil
}

type repomdData struct {
	Type     string `xml:"type,attr"`
	Location struct {
		Href string `xml:"href,attr"`
	} `xml:"location"`
}

type repomdXML struct {
	XMLName xml.Name     `xml:"repomd"`
	Data    []repomdData `xml:"data"`
}

// ExternalRepoIngestor fetches and caches external RPM-MD primary metadata.
type ExternalRepoIngestor struct {
	mu     sync.RWMutex
	getter HTTPGetter
	cache  map[string][]byte
}

// NewExternalRepoIngestor creates a new metadata ingestor.
func NewExternalRepoIngestor(getter HTTPGetter) *ExternalRepoIngestor {
	if getter == nil {
		getter = NewDefaultHTTPGetter()
	}
	return &ExternalRepoIngestor{
		getter: getter,
		cache:  make(map[string][]byte),
	}
}

// FetchPrimary downloads the primary.xml or primary.xml.gz metadata for a remote repository URL.
func (i *ExternalRepoIngestor) FetchPrimary(ctx context.Context, repoURL string) ([]byte, error) {
	repoURL = strings.TrimSpace(repoURL)
	if !strings.HasSuffix(repoURL, "/") {
		repoURL += "/"
	}

	i.mu.RLock()
	if cached, ok := i.cache[repoURL]; ok {
		i.mu.RUnlock()
		return cached, nil
	}
	i.mu.RUnlock()

	repomdURL := repoURL + "repodata/repomd.xml"
	repomdBytes, err := i.getter.Get(ctx, repomdURL)
	if err != nil {
		return nil, fmt.Errorf("failed fetching repomd.xml from %s: %w", repomdURL, err)
	}

	primaryHref, err := parsePrimaryLocation(repomdBytes)
	if err != nil {
		return nil, fmt.Errorf("failed parsing primary location from %s: %w", repomdURL, err)
	}

	primaryURL := repoURL + primaryHref
	primaryBytes, err := i.getter.Get(ctx, primaryURL)
	if err != nil {
		return nil, fmt.Errorf("failed fetching primary metadata from %s: %w", primaryURL, err)
	}

	i.mu.Lock()
	i.cache[repoURL] = primaryBytes
	i.mu.Unlock()

	return primaryBytes, nil
}

func parsePrimaryLocation(repomdBytes []byte) (string, error) {
	var r repomdXML
	if err := xml.Unmarshal(repomdBytes, &r); err != nil {
		return "", fmt.Errorf("unmarshal error: %w", err)
	}

	for _, d := range r.Data {
		if d.Type == "primary" && d.Location.Href != "" {
			return strings.TrimPrefix(d.Location.Href, "/"), nil
		}
	}
	return "", fmt.Errorf("no primary metadata section found in repomd.xml")
}
