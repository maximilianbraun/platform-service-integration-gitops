/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Installation struct {
	ID      int64              `json:"id"`
	Account InstallationOwner `json:"account"`
	AppSlug string            `json:"app_slug"`
}

type InstallationOwner struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type InstallationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Client struct {
	baseURL    string
	jwtToken   string
	httpClient *http.Client
}

func NewClient(baseURL, jwtToken string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{
		baseURL:    baseURL,
		jwtToken:   jwtToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) ListInstallations(ctx context.Context) ([]Installation, error) {
	var all []Installation
	page := 1

	for {
		url := fmt.Sprintf("%s/app/installations?per_page=100&page=%d", c.baseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		var installations []Installation
		if err := c.do(req, &installations); err != nil {
			return nil, err
		}

		all = append(all, installations...)
		if len(installations) < 100 {
			break
		}
		page++
	}

	return all, nil
}

func (c *Client) CreateInstallationToken(ctx context.Context, installationID int64, repos []string) (*InstallationToken, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, installationID)

	var body io.Reader
	if len(repos) > 0 {
		payload := struct {
			Repositories []string `json:"repositories"`
		}{Repositories: repos}
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		body = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	var token InstallationToken
	if err := c.do(req, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func (c *Client) do(req *http.Request, result interface{}) error {
	req.Header.Set("Authorization", "Bearer "+c.jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
			resetUnix, _ := strconv.ParseInt(resetStr, 10, 64)
			resetTime := time.Unix(resetUnix, 0)
			wait := time.Until(resetTime)
			if wait > 0 && wait < 5*time.Minute {
				return &RateLimitError{RetryAfter: wait}
			}
		}
		return &RateLimitError{RetryAfter: 60 * time.Second}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}
