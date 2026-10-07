// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package gcsx

import (
	"context"
	"net/http"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// AuthClient adds credentials to requests for GCS objects over HTTP (see
// HTTPURL), so that a private bucket reads like a public one. Requests to
// other hosts are sent without them.
type AuthClient struct {
	httpx.BasicClient
	TokenSource oauth2.TokenSource
}

var _ httpx.BasicClient = &AuthClient{}

// NewReadAuthClient is an AuthClient with read-only application default
// credentials.
func NewReadAuthClient(ctx context.Context, c httpx.BasicClient) (*AuthClient, error) {
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/devstorage.read_only")
	if err != nil {
		return nil, errors.Wrap(err, "finding default credentials")
	}
	return &AuthClient{BasicClient: c, TokenSource: oauth2.ReuseTokenSource(nil, ts)}, nil
}

// Do sends req, with credentials if it is for storage.googleapis.com.
func (c *AuthClient) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "storage.googleapis.com" {
		return c.BasicClient.Do(req)
	}
	tok, err := c.TokenSource.Token()
	if err != nil {
		return nil, errors.Wrap(err, "getting GCS token")
	}
	req = req.Clone(req.Context())
	tok.SetAuthHeader(req)
	return c.BasicClient.Do(req)
}
