package codexlogin

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// keyMaxAge bounds how long a signing key OpenAI has withdrawn stays trusted.
	keyMaxAge = 24 * time.Hour
	// refetchGap spaces key fetches, bounding those forced by made-up key IDs or an unreachable endpoint.
	refetchGap = time.Minute
	// keyFetchTimeout keeps a slow key endpoint from stalling request authentication.
	keyFetchTimeout = 10 * time.Second
	// keySetBodyLimit caps the key document read; OpenAI's is about 2 KB.
	keySetBodyLimit = 1 << 20
)

// keySet caches OpenAI's token signing keys, fetched from its JWKS endpoint.
type keySet struct {
	url string
	now func() time.Time

	mu        sync.Mutex
	client    *http.Client
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

func newKeySet(url string, client *http.Client, now func() time.Time) *keySet {
	return &keySet{url: url, client: client, now: now}
}

func (s *keySet) setClient(client *http.Client) {
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
}

// key returns the public key for kid, fetching when it is unknown or the cache is old.
// A failed fetch keeps the cached keys so a network blip doesn't lock clients out.
func (s *keySet) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	key, known := s.keys[kid]
	stale := now.Sub(s.fetchedAt) >= keyMaxAge
	if known && !stale {
		return key, nil
	}
	if now.Sub(s.triedAt) < refetchGap {
		if known {
			return key, nil
		}
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	s.triedAt = now
	keys, errFetch := s.fetch(ctx)
	if errFetch != nil {
		log.Warnf("codex login: fetch OpenAI signing keys: %v", errFetch)
		if known {
			return key, nil
		}
		return nil, errFetch
	}
	s.keys = keys
	s.fetchedAt = now
	if key, known = keys[kid]; !known {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	return key, nil
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (s *keySet) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, keyFetchTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if errReq != nil {
		return nil, errReq
	}
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if errDecode := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, keySetBodyLimit)).Decode(&doc); errDecode != nil {
		return nil, errDecode
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, errKey := rsaPublicKey(k)
		if errKey != nil {
			return nil, fmt.Errorf("key %q: %w", k.Kid, errKey)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no RSA signing keys")
	}
	return keys, nil
}

func rsaPublicKey(k jwk) (*rsa.PublicKey, error) {
	n, errN := base64.RawURLEncoding.DecodeString(k.N)
	if errN != nil {
		return nil, errN
	}
	e, errE := base64.RawURLEncoding.DecodeString(k.E)
	if errE != nil {
		return nil, errE
	}
	exponent := new(big.Int).SetBytes(e)
	if !exponent.IsInt64() || exponent.Int64() < 3 {
		return nil, fmt.Errorf("bad exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}, nil
}
