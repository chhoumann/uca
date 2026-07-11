package detect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/chhoumann/uca/internal/version"
)

// The VS Code Marketplace has no local metadata for "latest", but its gallery
// API answers in one small HTTP round-trip - versus ~0.5-1s for a no-op
// `code --install-extension`. Used by the skip-if-current update path and by
// --check. Any failure returns "" (callers fall back to running the command /
// reporting unknown). Shares the UCA_NO_REGISTRY_HTTP kill switch with the npm
// registry fast path.
var marketplaceURL = "https://marketplace.visualstudio.com/_apis/public/gallery/extensionquery"

// VSCodeMarketplaceLatest returns the latest stable version of an extension,
// deduplicated per extension ID (a prefetch and an on-demand caller share one
// request). Failed lookups are retried on the next call, successes memoized.
func (e *Env) VSCodeMarketplaceLatest(ctx context.Context, extID string) string {
	extID = strings.TrimSpace(extID)
	if extID == "" || os.Getenv("UCA_NO_REGISTRY_HTTP") != "" {
		return ""
	}
	return e.lookupFlight("vscode\x00"+extID, func() string { return queryMarketplaceLatest(ctx, extID) })
}

// PrefetchMarketplaceLatest starts marketplace lookups in the background so the
// answers are (mostly) ready when the update path asks for them.
func (e *Env) PrefetchMarketplaceLatest(ctx context.Context, extIDs []string) {
	for _, id := range extIDs {
		if id = strings.TrimSpace(id); id != "" {
			go e.VSCodeMarketplaceLatest(ctx, id)
		}
	}
}

func queryMarketplaceLatest(ctx context.Context, extID string) string {
	payload := map[string]any{
		"filters": []map[string]any{{
			"criteria": []map[string]any{{"filterType": 7, "value": extID}},
			"pageSize": 1,
		}},
		"flags": 17, // IncludeVersions | IncludeVersionProperties
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, marketplaceURL, bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json;api-version=3.0-preview.1")
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var result struct {
		Results []struct {
			Extensions []struct {
				Versions []struct {
					Version    string `json:"version"`
					Properties []struct {
						Key   string `json:"key"`
						Value string `json:"value"`
					} `json:"properties"`
				} `json:"versions"`
			} `json:"extensions"`
		} `json:"results"`
	}
	if json.Unmarshal(data, &result) != nil {
		return ""
	}
	if len(result.Results) == 0 || len(result.Results[0].Extensions) == 0 {
		return ""
	}
	// Versions are newest-first; `code --install-extension` installs the latest
	// stable by default, so pick the first non-pre-release entry.
	for _, v := range result.Results[0].Extensions[0].Versions {
		preRelease := false
		for _, p := range v.Properties {
			if p.Key == "Microsoft.VisualStudio.Code.PreRelease" && strings.EqualFold(p.Value, "true") {
				preRelease = true
				break
			}
		}
		if preRelease {
			continue
		}
		// A malformed version string on one entry shouldn't abort the lookup;
		// an older stable entry may still be usable.
		if _, ok := version.ExtractToken(v.Version); !ok {
			continue
		}
		return v.Version
	}
	return ""
}
