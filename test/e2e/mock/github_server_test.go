package mock

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestGitHubServer_ListInstallations(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewGitHubServer(123456, &key.PublicKey)
	defer srv.Close()

	srv.AddInstallation(1001, "test-org")

	token := makeJWT(t, key, 123456)

	req, _ := http.NewRequest("GET", srv.URL()+"/app/installations", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestGitHubServer_CreateAccessToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewGitHubServer(123456, &key.PublicKey)
	defer srv.Close()

	srv.AddInstallation(1001, "test-org")

	token := makeJWT(t, key, 123456)

	req, _ := http.NewRequest("POST", srv.URL()+"/app/installations/1001/access_tokens", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	if srv.TokenCounter != 1 {
		t.Fatalf("expected TokenCounter=1, got %d", srv.TokenCounter)
	}
}

func TestGitHubServer_UnauthorizedWithoutJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewGitHubServer(123456, &key.PublicKey)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL()+"/app/installations", nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestGitHubServer_InstallationNotFound(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewGitHubServer(123456, &key.PublicKey)
	defer srv.Close()

	token := makeJWT(t, key, 123456)

	req, _ := http.NewRequest("POST", srv.URL()+"/app/installations/9999/access_tokens", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGitHubServer_RemoveInstallation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewGitHubServer(123456, &key.PublicKey)
	defer srv.Close()

	srv.AddInstallation(1001, "test-org")

	if _, found := srv.HasInstallationForOrg("test-org"); !found {
		t.Fatal("expected installation to exist")
	}

	srv.RemoveInstallation(1001)

	if _, found := srv.HasInstallationForOrg("test-org"); found {
		t.Fatal("expected installation to be removed")
	}
}

func makeJWT(t *testing.T, key *rsa.PrivateKey, appID int64) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": strconv.FormatInt(appID, 10),
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

