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

package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

// GitLabServer is an httptest-based mock GitLab API server for testing.
type GitLabServer struct {
	Server *httptest.Server

	mu            sync.RWMutex
	groups        map[string]GitLabGroup
	validAppID    string
	validSecret   string
	tokenResponse string
	tokenExpiry   int
	TokenCounter  int
}

// GitLabGroup represents a GitLab group.
type GitLabGroup struct {
	ID       int    `json:"id"`
	Path     string `json:"path"`
	FullPath string `json:"full_path"`
	Name     string `json:"name"`
}

// NewGitLabServer creates a new mock GitLab API server.
func NewGitLabServer(appID, clientSecret string) *GitLabServer {
	s := &GitLabServer{
		groups:        make(map[string]GitLabGroup),
		validAppID:    appID,
		validSecret:   clientSecret,
		tokenResponse: "glpat-mock-token-12345",
		tokenExpiry:   7200,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", s.handleOAuthToken)
	mux.HandleFunc("GET /api/v4/groups/{path}", s.handleGetGroup)
	s.Server = httptest.NewServer(mux)
	return s
}

// AddGroup adds a group to the mock server.
func (s *GitLabServer) AddGroup(path, name string, id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[path] = GitLabGroup{
		ID:       id,
		Path:     path,
		FullPath: path,
		Name:     name,
	}
}

// RemoveGroup removes a group from the mock server.
func (s *GitLabServer) RemoveGroup(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, path)
}

// SetTokenResponse sets the token that will be returned by the OAuth endpoint.
func (s *GitLabServer) SetTokenResponse(token string, expiresIn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenResponse = token
	s.tokenExpiry = expiresIn
}

// URL returns the base URL of the mock server.
func (s *GitLabServer) URL() string {
	return s.Server.URL
}

// Close shuts down the mock server.
func (s *GitLabServer) Close() {
	s.Server.Close()
}

func (s *GitLabServer) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")

	if grantType != "client_credentials" {
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}

	if clientID != s.validAppID || clientSecret != s.validSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	s.TokenCounter++
	token := s.tokenResponse
	expiry := s.tokenExpiry
	s.mu.Unlock()

	resp := map[string]interface{}{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   expiry,
		"scope":        "api",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *GitLabServer) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("PRIVATE-TOKEN")
	if token == "" {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		return
	}

	pathParam := r.PathValue("path")
	groupPath, _ := url.PathUnescape(pathParam)
	if groupPath == "" {
		groupPath = pathParam
	}

	// Also handle URL-encoded slashes in group paths
	groupPath = strings.ReplaceAll(groupPath, "%2F", "/")

	s.mu.RLock()
	group, exists := s.groups[groupPath]
	s.mu.RUnlock()

	if !exists {
		http.Error(w, `{"message":"404 Group Not Found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(group)
}
