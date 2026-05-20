package mock

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GitHubServer is an httptest-based mock of the GitHub API for testing the git-connection service.
// It handles App installation listing and access token generation.
type GitHubServer struct {
	Server *httptest.Server

	mu            sync.RWMutex
	installations map[int64]*Installation
	appID         int64
	publicKey     *rsa.PublicKey

	// TokenLifetime controls the expiration of generated tokens.
	TokenLifetime time.Duration

	// TokenCounter tracks how many tokens have been issued (for assertions).
	TokenCounter int
}

// Installation represents a GitHub App installation on an organization.
type Installation struct {
	ID             int64  `json:"id"`
	Account        Account `json:"account"`
	AppID          int64  `json:"app_id"`
	TargetType     string `json:"target_type"`
	Permissions    map[string]string `json:"permissions"`
	RepositorySelection string `json:"repository_selection"`
}

// Account represents the GitHub account (org or user) where the App is installed.
type Account struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

// AccessToken is the response returned when generating an installation access token.
type AccessToken struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	Permissions map[string]string `json:"permissions"`
}

// NewGitHubServer creates a new mock GitHub API server.
// The publicKey is used to validate incoming JWT tokens (pass the public half of your test RSA key).
func NewGitHubServer(appID int64, publicKey *rsa.PublicKey) *GitHubServer {
	s := &GitHubServer{
		installations: make(map[int64]*Installation),
		appID:         appID,
		publicKey:     publicKey,
		TokenLifetime: 1 * time.Hour,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /app/installations", s.handleListInstallations)
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.handleCreateAccessToken)
	mux.HandleFunc("GET /app", s.handleGetApp)

	s.Server = httptest.NewServer(mux)
	return s
}

// AddInstallation registers an installation for a given org. The App will appear "installed" on this org.
func (s *GitHubServer) AddInstallation(id int64, orgLogin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installations[id] = &Installation{
		ID:    id,
		AppID: s.appID,
		Account: Account{
			Login: orgLogin,
			ID:    id * 100,
			Type:  "Organization",
		},
		TargetType:          "Organization",
		Permissions:         map[string]string{"contents": "read", "metadata": "read"},
		RepositorySelection: "all",
	}
}

// RemoveInstallation removes an installation, simulating the App being uninstalled from an org.
func (s *GitHubServer) RemoveInstallation(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.installations, id)
}

// HasInstallationForOrg checks if there's an installation for the given org login.
func (s *GitHubServer) HasInstallationForOrg(orgLogin string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, inst := range s.installations {
		if strings.EqualFold(inst.Account.Login, orgLogin) {
			return inst.ID, true
		}
	}
	return 0, false
}

// URL returns the base URL of the mock server.
func (s *GitHubServer) URL() string {
	return s.Server.URL
}

// Close shuts down the mock server.
func (s *GitHubServer) Close() {
	s.Server.Close()
}

func (s *GitHubServer) handleGetApp(w http.ResponseWriter, r *http.Request) {
	if err := s.validateJWT(r); err != nil {
		http.Error(w, fmt.Sprintf(`{"message":"%s"}`, err.Error()), http.StatusUnauthorized)
		return
	}

	resp := map[string]interface{}{
		"id":   s.appID,
		"slug": "openmcp-e2e-git-connection",
		"name": "OpenMCP E2E Git Connection",
		"html_url": fmt.Sprintf("%s/apps/openmcp-e2e-git-connection", s.Server.URL),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *GitHubServer) handleListInstallations(w http.ResponseWriter, r *http.Request) {
	if err := s.validateJWT(r); err != nil {
		http.Error(w, fmt.Sprintf(`{"message":"%s"}`, err.Error()), http.StatusUnauthorized)
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	installs := make([]*Installation, 0, len(s.installations))
	for _, inst := range s.installations {
		installs = append(installs, inst)
	}

	writeJSON(w, http.StatusOK, installs)
}

func (s *GitHubServer) handleCreateAccessToken(w http.ResponseWriter, r *http.Request) {
	if err := s.validateJWT(r); err != nil {
		http.Error(w, fmt.Sprintf(`{"message":"%s"}`, err.Error()), http.StatusUnauthorized)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, `{"message":"invalid installation id"}`, http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	inst, exists := s.installations[id]
	s.mu.RUnlock()

	if !exists {
		http.Error(w, `{"message":"installation not found"}`, http.StatusNotFound)
		return
	}

	s.mu.Lock()
	s.TokenCounter++
	counter := s.TokenCounter
	s.mu.Unlock()

	token := AccessToken{
		Token:       fmt.Sprintf("ghs_mock_token_%s_%d", inst.Account.Login, counter),
		ExpiresAt:   time.Now().Add(s.TokenLifetime),
		Permissions: inst.Permissions,
	}

	writeJSON(w, http.StatusCreated, token)
}

func (s *GitHubServer) validateJWT(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return fmt.Errorf("missing Authorization header")
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		return fmt.Errorf("Authorization header must use Bearer scheme")
	}

	tokenString := strings.TrimPrefix(auth, "Bearer ")

	if s.publicKey == nil {
		return nil
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.publicKey, nil
	})
	if err != nil {
		return fmt.Errorf("invalid JWT: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return fmt.Errorf("invalid JWT claims")
	}

	iss, _ := claims["iss"].(string)
	if iss != strconv.FormatInt(s.appID, 10) {
		return fmt.Errorf("JWT issuer mismatch: got %s, want %d", iss, s.appID)
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
