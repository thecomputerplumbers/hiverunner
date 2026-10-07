package cache

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Identity is what a job's token proves about it: which repository it is
// running for, and which refs it may read caches from and save them to.
type Identity struct {
	// Repo is GitHub's repository id; RepoName is `owner/name` if given.
	Repo     string
	RepoName string
	// Read is the scopes a restore may look in, most specific first.
	Read []string
	// Write is the scope a save goes to, or "" if the job may not save.
	Write string
}

// Verifier decides whether a token is GitHub's and what it proves.
type Verifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}

// ErrToken is any reason a token is not accepted. The reason itself goes to
// the daemon's log, not to the job.
var ErrToken = errors.New("the job's token was not accepted")

// DefaultIssuer signs the tokens GitHub gives jobs on github.com.
const DefaultIssuer = "https://token.actions.githubusercontent.com"

// GitHub verifies a job's runtime token against the issuer's published
// keys. A token is trusted for nothing until its signature checks out: the
// repository it names decides whose cache the job sees.
type GitHub struct {
	Issuer string
	Client *http.Client
	// Now is the clock. Tests replace it.
	Now func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

const (
	// keysFor is how long fetched keys are used before asking again.
	keysFor = time.Hour
	// refetchAfter stops a stream of unknown key ids from becoming a stream
	// of requests to GitHub.
	refetchAfter = time.Minute
	clockSkew    = time.Minute
)

func (g *GitHub) issuer() string {
	if g.Issuer != "" {
		return strings.TrimRight(g.Issuer, "/")
	}
	return DefaultIssuer
}

func (g *GitHub) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

type scope struct {
	Scope      string `json:"Scope"`
	Permission int    `json:"Permission"`
}

// flexible reads a claim GitHub may send as a string or as a number.
type flexible string

func (f *flexible) UnmarshalJSON(raw []byte) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		*f = flexible(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	*f = flexible(number.String())
	return nil
}

type claims struct {
	Issuer     string   `json:"iss"`
	Expires    float64  `json:"exp"`
	NotBefore  float64  `json:"nbf"`
	Scopes     string   `json:"ac"`
	Repository flexible `json:"repository_id"`
	Name       string   `json:"repository"`
}

const (
	permissionRead  = 1
	permissionWrite = 2
)

// Verify checks the token's signature, issuer and dates, and reads from it
// the repository and cache scopes GitHub granted the job.
func (g *GitHub) Verify(ctx context.Context, token string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("%w: not a signed token", ErrToken)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Identity{}, fmt.Errorf("%w: unreadable header", ErrToken)
	}
	// One algorithm, named here and not by the token.
	if header.Algorithm != "RS256" {
		return Identity{}, fmt.Errorf("%w: signed with %q, not RS256", ErrToken, header.Algorithm)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, fmt.Errorf("%w: unreadable signature", ErrToken)
	}
	key, err := g.key(ctx, header.KeyID)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return Identity{}, fmt.Errorf("%w: the signature does not match", ErrToken)
	}

	var body claims
	if err := decodeSegment(parts[1], &body); err != nil {
		return Identity{}, fmt.Errorf("%w: unreadable claims", ErrToken)
	}
	if strings.TrimRight(body.Issuer, "/") != g.issuer() {
		return Identity{}, fmt.Errorf("%w: issued by %q", ErrToken, body.Issuer)
	}
	now := g.now()
	if body.Expires == 0 || now.After(time.Unix(int64(body.Expires), 0).Add(clockSkew)) {
		return Identity{}, fmt.Errorf("%w: expired", ErrToken)
	}
	if body.NotBefore != 0 && now.Before(time.Unix(int64(body.NotBefore), 0).Add(-clockSkew)) {
		return Identity{}, fmt.Errorf("%w: not valid yet", ErrToken)
	}
	if !numeric.MatchString(string(body.Repository)) {
		return Identity{}, fmt.Errorf("%w: names no repository", ErrToken)
	}
	var scopes []scope
	if body.Scopes == "" || json.Unmarshal([]byte(body.Scopes), &scopes) != nil || len(scopes) == 0 {
		return Identity{}, fmt.Errorf("%w: grants no cache scope", ErrToken)
	}

	identity := Identity{Repo: string(body.Repository), RepoName: body.Name}
	// A job reads its own ref before the ones it merely may read, which is
	// the order GitHub restores in. Its own ref is the one it may write.
	sort.SliceStable(scopes, func(i, j int) bool { return scopes[i].Permission > scopes[j].Permission })
	for _, granted := range scopes {
		if granted.Scope == "" {
			continue
		}
		if granted.Permission&permissionRead != 0 {
			identity.Read = append(identity.Read, granted.Scope)
		}
		if granted.Permission&permissionWrite != 0 && identity.Write == "" {
			identity.Write = granted.Scope
		}
	}
	if len(identity.Read) == 0 && identity.Write == "" {
		return Identity{}, fmt.Errorf("%w: grants no cache scope", ErrToken)
	}
	return identity, nil
}

func decodeSegment(segment string, into any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// key returns the issuer's key with this id, fetching the set when it is
// not known or has grown old.
func (g *GitHub) key(ctx context.Context, id string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	age := g.now().Sub(g.fetched)
	if key, ok := g.keys[id]; ok && age < keysFor {
		return key, nil
	}
	// An id not in a fresh set is a rotation, or a forgery. Ask again, but
	// not more than once a minute.
	if g.keys == nil || age >= refetchAfter {
		keys, err := g.fetch(ctx)
		if err != nil {
			if key, ok := g.keys[id]; ok {
				// GitHub being unreachable does not unsign a known key.
				return key, nil
			}
			return nil, fmt.Errorf("%w: GitHub's keys could not be fetched: %v", ErrToken, err)
		}
		g.keys, g.fetched = keys, g.now()
	}
	if key, ok := g.keys[id]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: signed with a key GitHub does not publish", ErrToken)
}

func (g *GitHub) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	// The keys are where the issuer's discovery document says, which is not
	// always under the issuer's own path.
	location := g.issuer() + "/.well-known/jwks"
	var discovery struct {
		Keys string `json:"jwks_uri"`
	}
	if err := g.get(ctx, g.issuer()+"/.well-known/openid-configuration", &discovery); err == nil && strings.HasPrefix(discovery.Keys, "https://") {
		location = discovery.Keys
	}
	var set struct {
		Keys []struct {
			Type     string `json:"kty"`
			ID       string `json:"kid"`
			Modulus  string `json:"n"`
			Exponent string `json:"e"`
		} `json:"keys"`
	}
	if err := g.get(ctx, location, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, published := range set.Keys {
		if published.Type != "RSA" || published.ID == "" {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(published.Modulus)
		exponent, errE := base64.RawURLEncoding.DecodeString(published.Exponent)
		if errN != nil || errE != nil || len(modulus) < 256 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		e := 0
		for _, b := range exponent {
			e = e<<8 | int(b)
		}
		keys[published.ID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: e}
	}
	if len(keys) == 0 {
		return nil, errors.New("the issuer publishes no usable keys")
	}
	return keys, nil
}

func (g *GitHub) get(ctx context.Context, url string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := g.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", url, response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(into)
}
