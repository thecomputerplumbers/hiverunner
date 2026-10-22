package cache

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newStore(t *testing.T, limit int64) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Limit = func() int64 { return limit }
	store.free = func(string) (int64, error) { return 1 << 40, nil }
	return store
}

// save puts one entry in the store the short way.
func save(t *testing.T, store *Store, pool, repo, scope, key, version, content string) Entry {
	t.Helper()
	id, err := store.Begin(Entry{Pool: pool, Repo: repo, Scope: scope, Key: key, Version: version})
	if err != nil {
		t.Fatalf("begin %s: %v", key, err)
	}
	if err := store.PutWhole(pool, id, strings.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("upload %s: %v", key, err)
	}
	entry, err := store.Finalize(pool, repo, scope, key, version, int64(len(content)))
	if err != nil {
		t.Fatalf("finalize %s: %v", key, err)
	}
	return entry
}

func TestRestoreChoosesAsGitHubDoes(t *testing.T) {
	store := newStore(t, 0)
	save(t, store, "acme", "1", "refs/heads/main", "deps-linux-aaa", "v", "main-old")
	time.Sleep(5 * time.Millisecond)
	save(t, store, "acme", "1", "refs/heads/main", "deps-linux-bbb", "v", "main-new")
	save(t, store, "acme", "1", "refs/heads/feature", "deps-linux-ccc", "v", "feature")
	save(t, store, "acme", "1", "refs/heads/main", "deps-linux-zzz", "other-version", "wrong version")

	find := func(scopes []string, keys ...string) string {
		entry, ok := store.Find("acme", "1", scopes, keys, "v")
		if !ok {
			return ""
		}
		return entry.Key
	}
	both := []string{"refs/heads/feature", "refs/heads/main"}
	for _, test := range []struct {
		name   string
		scopes []string
		keys   []string
		want   string
	}{
		{"exact key in the job's own ref", both, []string{"deps-linux-ccc"}, "deps-linux-ccc"},
		{"exact key in the default branch", both, []string{"deps-linux-aaa"}, "deps-linux-aaa"},
		{"a prefix of the key in the job's own ref wins over the default branch", both, []string{"deps-linux-"}, "deps-linux-ccc"},
		{"a restore key falls back to the newest match", []string{"refs/heads/main"}, []string{"deps-linux-nope", "deps-linux-"}, "deps-linux-bbb"},
		{"an earlier restore key wins over a later one", []string{"refs/heads/main"}, []string{"nope", "deps-linux-a", "deps-linux-b"}, "deps-linux-aaa"},
		{"another ref's cache is invisible without its scope", []string{"refs/heads/main"}, []string{"deps-linux-ccc"}, ""},
		{"the version must match", []string{"refs/heads/main"}, []string{"deps-linux-zzz"}, ""},
		{"nothing matches", both, []string{"gradle-"}, ""},
	} {
		if got := find(test.scopes, test.keys...); got != test.want {
			t.Errorf("%s: got %q, want %q", test.name, got, test.want)
		}
	}
}

func TestOneRepositoryNeverSeesAnothers(t *testing.T) {
	store := newStore(t, 0)
	entry := save(t, store, "acme", "1", "refs/heads/main", "deps", "v", "acme's dependencies")
	scopes := []string{"refs/heads/main"}
	for _, other := range [][2]string{{"acme", "2"}, {"globex", "1"}} {
		if _, ok := store.Find(other[0], other[1], scopes, []string{"deps"}, "v"); ok {
			t.Errorf("pool %s repository %s found another's cache", other[0], other[1])
		}
		if _, _, err := store.Blob(other[0], other[1], entry.ID, entry.Secret); !errors.Is(err, ErrNotFound) {
			t.Errorf("pool %s repository %s downloaded another's cache", other[0], other[1])
		}
	}
	if _, _, err := store.Blob("acme", "1", entry.ID, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Error("a download with the wrong secret was served")
	}
	// The same key may be saved by each of them, and each gets its own.
	save(t, store, "globex", "1", "refs/heads/main", "deps", "v", "globex's")
	file, _, err := store.Blob("acme", "1", entry.ID, entry.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got, _ := io.ReadAll(file); string(got) != "acme's dependencies" {
		t.Errorf("acme's cache now reads %q", got)
	}
	for _, bad := range [][2]string{{"../acme", "1"}, {"acme", "../1"}, {"", "1"}, {"acme", ""}, {"..", "1"}, {"acme/x", "1"}} {
		if _, err := store.Begin(Entry{Pool: bad[0], Repo: bad[1], Scope: "s", Key: "k", Version: "v"}); err == nil {
			t.Errorf("pool %q repository %q was accepted", bad[0], bad[1])
		}
	}
}

func TestACacheIsSavedOnceAndWhole(t *testing.T) {
	store := newStore(t, 0)
	first := save(t, store, "acme", "1", "refs/heads/main", "deps", "v", "first")
	if _, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "refs/heads/main", Key: "deps", Version: "v"}); !errors.Is(err, ErrExists) {
		t.Fatalf("saving an existing key again: %v, want ErrExists", err)
	}
	// The same key in another ref is another cache.
	save(t, store, "acme", "1", "refs/heads/feature", "deps", "v", "feature's")

	// Two jobs racing: both began before either finished.
	a, _ := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "refs/heads/main", Key: "race", Version: "v"})
	b, _ := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "refs/heads/main", Key: "race", Version: "v"})
	_ = store.PutWhole("acme", a, strings.NewReader("from a"), 6)
	_ = store.PutWhole("acme", b, strings.NewReader("from b"), 6)
	one, err := store.Finalize("acme", "1", "refs/heads/main", "race", "v", 6)
	if err != nil {
		t.Fatal(err)
	}
	two, err := store.Finalize("acme", "1", "refs/heads/main", "race", "v", 6)
	if err != nil || two.ID != one.ID {
		t.Fatalf("the second of two racing saves: entry %q err %v, want the first's entry %q", two.ID, err, one.ID)
	}

	// A size that is not what arrived is a failed save, and leaves nothing.
	id, _ := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "refs/heads/main", Key: "short", Version: "v"})
	_ = store.PutWhole("acme", id, strings.NewReader("12345"), 5)
	if _, err := store.Finalize("acme", "1", "refs/heads/main", "short", "v", 500); !errors.Is(err, ErrSize) {
		t.Fatalf("finalize with the wrong size: %v", err)
	}
	if _, ok := store.Find("acme", "1", []string{"refs/heads/main"}, []string{"short"}, "v"); ok {
		t.Fatal("a save that failed is restorable")
	}
	// Finalizing what was never uploaded is not found.
	if _, err := store.Finalize("acme", "1", "refs/heads/main", "never", "v", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finalize without an upload: %v", err)
	}
	// Another pool cannot write into this pool's upload.
	id, _ = store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "refs/heads/main", Key: "mine", Version: "v"})
	if err := store.PutWhole("globex", id, strings.NewReader("poison"), 6); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another pool wrote to an upload: %v", err)
	}
	if first.Secret == "" || first.ID == "" {
		t.Fatal("an entry needs an id and a secret")
	}
}

func TestBlocksAreAssembledInTheListedOrder(t *testing.T) {
	store := newStore(t, 0)
	id, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "k", Version: "v"})
	if err != nil {
		t.Fatal(err)
	}
	// Sent out of order and at once, as a parallel upload does.
	var wait sync.WaitGroup
	for _, block := range [][2]string{{"Yjo=", "world"}, {"YTo=", "hello "}, {"Yzo=", "!"}} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := store.PutBlock("acme", id, block[0], strings.NewReader(block[1]), int64(len(block[1]))); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if err := store.Commit("acme", id, []string{"YTo=", "Yjo=", "missing"}); err == nil {
		t.Fatal("a list naming a block that was never sent was committed")
	}
	if err := store.Commit("acme", id, []string{"YTo=", "Yjo=", "Yzo="}); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Finalize("acme", "1", "s", "k", "v", 12)
	if err != nil {
		t.Fatal(err)
	}
	file, _, err := store.Blob("acme", "1", entry.ID, entry.Secret)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got, _ := io.ReadAll(file); string(got) != "hello world!" {
		t.Fatalf("assembled %q", got)
	}
}

func TestTheCacheStaysWithinItsLimit(t *testing.T) {
	store := newStore(t, 1000)
	kilobyte := strings.Repeat("x", 300)
	old := save(t, store, "acme", "1", "s", "old", "v", kilobyte)
	save(t, store, "acme", "1", "s", "middle", "v", kilobyte)
	save(t, store, "acme", "2", "s", "recent", "v", kilobyte)
	// Wanted again: `old` is now the most recently used.
	past := time.Now().Add(-time.Hour)
	for _, name := range []string{"middle", "recent"} {
		entry, _ := store.Find("acme", map[string]string{"middle": "1", "recent": "2"}[name], []string{"s"}, []string{name}, "v")
		dir, _ := store.repoDir(entry.Pool, entry.Repo)
		_ = os.Chtimes(filepath.Join(dir, entry.ID, "entry.json"), past, past)
		past = past.Add(time.Minute)
	}
	store.Find("acme", "1", []string{"s"}, []string{"old"}, "v")

	// 900 bytes held, 300 more makes 1200: over. Eviction goes to 900.
	newest := save(t, store, "acme", "1", "s", "newest", "v", kilobyte)
	_, total := store.Usage()
	if total > 900 {
		t.Fatalf("the cache holds %d bytes after eviction, over nine tenths of its limit", total)
	}
	has := func(repo, key string) bool {
		_, ok := store.Find("acme", repo, []string{"s"}, []string{key}, "v")
		return ok
	}
	if has("1", "middle") {
		t.Error("the entry wanted longest ago survived")
	}
	if !has("1", "old") || !has("1", "newest") {
		t.Error("a recently wanted entry, or the one just saved, was evicted")
	}
	_ = old
	_ = newest

	// One archive larger than the whole cache is refused, and nothing else
	// is thrown out to make room for it.
	id, _ := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "huge", Version: "v"})
	if err := store.PutWhole("acme", id, strings.NewReader(strings.Repeat("y", 2000)), 2000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an archive over the limit: %v", err)
	}
	// And when the size was not declared up front.
	if err := store.PutWhole("acme", id, strings.NewReader(strings.Repeat("y", 2000)), -1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an undeclared archive over the limit: %v", err)
	}
	if !has("1", "newest") {
		t.Error("a refused save evicted something")
	}

	// Lowering the limit trims at once.
	store.Limit = func() int64 { return 300 }
	store.Trim()
	if _, total := store.Usage(); total > 300 {
		t.Fatalf("after lowering the limit the cache holds %d bytes", total)
	}
}

func TestAFullDiskRefusesSavesAndKeepsRestores(t *testing.T) {
	store := newStore(t, 0)
	save(t, store, "acme", "1", "s", "kept", "v", "data")
	store.free = func(string) (int64, error) { return minFree - 1, nil }
	if _, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "more", Version: "v"}); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("a save on a full disk: %v", err)
	}
	if _, ok := store.Find("acme", "1", []string{"s"}, []string{"kept"}, "v"); !ok {
		t.Fatal("a full disk lost a cache that was already saved")
	}
}

func TestClearAndUsage(t *testing.T) {
	store := newStore(t, 0)
	id, _ := store.Begin(Entry{Pool: "acme", Repo: "1", RepoName: "acme/web", Scope: "s", Key: "a", Version: "v"})
	_ = store.PutWhole("acme", id, strings.NewReader("12345"), 5)
	if _, err := store.Finalize("acme", "1", "s", "a", "v", 5); err != nil {
		t.Fatal(err)
	}
	save(t, store, "acme", "2", "s", "b", "v", "1234567890")
	repos, total := store.Usage()
	if total != 15 || len(repos) != 2 || repos[0].Repo != "2" || repos[1].RepoName != "acme/web" {
		t.Fatalf("usage: %d bytes, %+v", total, repos)
	}
	if freed, cleared := store.Clear("ACME/WEB"); freed != 5 || cleared != 1 {
		t.Fatalf("clearing one repository freed %d bytes of %d entries", freed, cleared)
	}
	if freed, _ := store.Clear(""); freed != 10 {
		t.Fatalf("clearing everything freed %d bytes", freed)
	}
	if _, total := store.Usage(); total != 0 {
		t.Fatalf("%d bytes left after clearing", total)
	}
}

// issuer is a stand-in for GitHub's token service: it publishes a key and
// signs tokens with it.
type issuer struct {
	server  *httptest.Server
	key     *rsa.PrivateKey
	fetches int
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &issuer{key: key}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks" {
			http.NotFound(w, r)
			return
		}
		fake.fetches++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (i *issuer) sign(t *testing.T, header, claims map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signed := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// token is what GitHub gives a job: the repository, and the refs it may
// read caches from and write them to.
func (i *issuer) token(t *testing.T, repo string, change func(claims map[string]any)) string {
	claims := map[string]any{
		"iss":           i.server.URL,
		"exp":           time.Now().Add(time.Hour).Unix(),
		"nbf":           time.Now().Add(-time.Minute).Unix(),
		"repository_id": repo,
		"repository":    "acme/web",
		"ac":            `[{"Scope":"refs/heads/main","Permission":1},{"Scope":"refs/heads/feature","Permission":3}]`,
	}
	if change != nil {
		change(claims)
	}
	return i.sign(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}, claims, i.key)
}

func TestOnlyGitHubsTokensAreBelieved(t *testing.T) {
	fake := newIssuer(t)
	verifier := &GitHub{Issuer: fake.server.URL}
	ctx := context.Background()

	identity, err := verifier.Verify(ctx, fake.token(t, "42", nil))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Repo != "42" || identity.RepoName != "acme/web" || identity.Write != "refs/heads/feature" ||
		len(identity.Read) != 2 || identity.Read[0] != "refs/heads/feature" || identity.Read[1] != "refs/heads/main" {
		t.Fatalf("identity: %+v", identity)
	}
	// A repository id sent as a number is the same repository.
	if identity, err := verifier.Verify(ctx, fake.token(t, "", func(c map[string]any) { c["repository_id"] = 42 })); err != nil || identity.Repo != "42" {
		t.Fatalf("numeric repository id: %+v, %v", identity, err)
	}
	// A read-only token may restore and may not save.
	readOnly := fake.token(t, "42", func(c map[string]any) { c["ac"] = `[{"Scope":"refs/heads/main","Permission":1}]` })
	if identity, err := verifier.Verify(ctx, readOnly); err != nil || identity.Write != "" || len(identity.Read) != 1 {
		t.Fatalf("read-only token: %+v, %v", identity, err)
	}

	stranger, _ := rsa.GenerateKey(rand.Reader, 2048)
	good := fake.token(t, "42", nil)
	parts := strings.Split(good, ".")
	forgedClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + fake.server.URL + `","exp":9999999999,"repository_id":"1","ac":"[{\"Scope\":\"s\",\"Permission\":3}]"}`))
	for name, token := range map[string]string{
		"signed by someone else":       fake.sign(t, map[string]any{"alg": "RS256", "kid": "k1"}, map[string]any{"iss": fake.server.URL, "exp": time.Now().Add(time.Hour).Unix(), "repository_id": "42", "ac": `[{"Scope":"s","Permission":3}]`}, stranger),
		"claims swapped after signing": parts[0] + "." + forgedClaims + "." + parts[2],
		"no signature at all":          fake.sign(t, map[string]any{"alg": "none", "kid": "k1"}, map[string]any{"iss": fake.server.URL, "exp": time.Now().Add(time.Hour).Unix(), "repository_id": "42"}, fake.key),
		"an unknown key":               fake.sign(t, map[string]any{"alg": "RS256", "kid": "k2"}, map[string]any{"iss": fake.server.URL, "exp": time.Now().Add(time.Hour).Unix(), "repository_id": "42", "ac": `[{"Scope":"s","Permission":3}]`}, fake.key),
		"expired":                      fake.token(t, "42", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"no expiry":                    fake.token(t, "42", func(c map[string]any) { delete(c, "exp") }),
		"not valid yet":                fake.token(t, "42", func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }),
		"another issuer":               fake.token(t, "42", func(c map[string]any) { c["iss"] = "https://example.com" }),
		"no repository":                fake.token(t, "", func(c map[string]any) { delete(c, "repository_id") }),
		"a repository that is a path":  fake.token(t, "../7", nil),
		"no cache scopes":              fake.token(t, "42", func(c map[string]any) { delete(c, "ac") }),
		"scopes granting nothing":      fake.token(t, "42", func(c map[string]any) { c["ac"] = `[{"Scope":"s","Permission":0}]` }),
		"not a token":                  "hello",
		"empty":                        "",
	} {
		if identity, err := verifier.Verify(ctx, token); !errors.Is(err, ErrToken) {
			t.Errorf("%s: accepted as %+v (err %v)", name, identity, err)
		}
	}
	// All of that asked GitHub for its keys once, plus once for the unknown
	// key, and not once per token.
	if fake.fetches > 2 {
		t.Errorf("the keys were fetched %d times", fake.fetches)
	}
}

// harness is a cache server as one VM sees it, with a token issuer to match.
type harness struct {
	t      *testing.T
	server *httptest.Server
	issuer *issuer
	store  *Store
	pool   string
}

func newHarness(t *testing.T, upstream string) *harness {
	t.Helper()
	fake := newIssuer(t)
	h := &harness{t: t, issuer: fake, store: newStore(t, 0), pool: "acme"}
	server := &Server{Store: h.store, Verifier: &GitHub{Issuer: fake.server.URL}, Log: quiet}
	if upstream != "" {
		server.Upstream, _ = url.Parse(upstream)
	}
	h.server = httptest.NewServer(server.Handler(func() string { return h.pool }))
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) call(token, method string, body any, into any) int {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	request, _ := http.NewRequest("POST", h.server.URL+servicePath+method, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(response.Body)
	if into != nil {
		if err := json.Unmarshal(answer, into); err != nil {
			h.t.Fatalf("%s answered %q", method, answer)
		}
	}
	return response.StatusCode
}

func put(t *testing.T, address string, body string) int {
	t.Helper()
	request, _ := http.NewRequest("PUT", address, strings.NewReader(body))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

type answer struct {
	OK          bool   `json:"ok"`
	UploadURL   string `json:"signed_upload_url"`
	DownloadURL string `json:"signed_download_url"`
	MatchedKey  string `json:"matched_key"`
	EntryID     string `json:"entry_id"`
	Message     string `json:"message"`
}

// The whole exchange `actions/cache` has with the service, request for
// request: a miss, a save sent as blocks, and a hit that downloads it.
func TestAJobSavesAndRestoresACache(t *testing.T) {
	h := newHarness(t, "")
	token := h.issuer.token(t, "42", nil)
	lookup := map[string]any{"key": "deps-abc", "restore_keys": []string{"deps-"}, "version": "v1"}

	var miss answer
	if status := h.call(token, "GetCacheEntryDownloadURL", lookup, &miss); status != 200 || miss.OK {
		t.Fatalf("lookup in an empty cache: %d %+v", status, miss)
	}

	var created answer
	if status := h.call(token, "CreateCacheEntry", map[string]any{"key": "deps-abc", "version": "v1"}, &created); status != 200 || !created.OK {
		t.Fatalf("create: %d %+v", status, created)
	}
	if !strings.HasPrefix(created.UploadURL, h.server.URL+"/devstoreaccount1/upload/") {
		t.Fatalf("the upload address %q is not on the server the job reached", created.UploadURL)
	}
	if status := put(t, created.UploadURL+"?comp=block&blockid=QUFB", "hello "); status != 201 {
		t.Fatalf("first block: %d", status)
	}
	if status := put(t, created.UploadURL+"?comp=block&blockid=QkJC", "cache"); status != 201 {
		t.Fatalf("second block: %d", status)
	}
	list := `<?xml version="1.0" encoding="utf-8"?><BlockList><Latest>QUFB</Latest><Latest>QkJC</Latest></BlockList>`
	if status := put(t, created.UploadURL+"?comp=blocklist", list); status != 201 {
		t.Fatalf("block list: %d", status)
	}

	// Not restorable until it is finalized.
	if h.call(token, "GetCacheEntryDownloadURL", lookup, &miss); miss.OK {
		t.Fatal("an upload was restorable before it was finalized")
	}
	var finalized answer
	// The size arrives as a string, as GitHub's client sends 64-bit numbers.
	if status := h.call(token, "FinalizeCacheEntryUpload", map[string]any{"key": "deps-abc", "version": "v1", "size_bytes": "11"}, &finalized); status != 200 || !finalized.OK || finalized.EntryID == "" {
		t.Fatalf("finalize: %d %+v", status, finalized)
	}

	var hit answer
	if status := h.call(token, "GetCacheEntryDownloadURL", lookup, &hit); status != 200 || !hit.OK || hit.MatchedKey != "deps-abc" {
		t.Fatalf("lookup after saving: %d %+v", status, hit)
	}
	// The download carries no credential but its address.
	response, err := http.Get(hit.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(got) != "hello cache" || response.Header.Get("Content-Length") != "11" {
		t.Fatalf("download: %d %q length %s", response.StatusCode, got, response.Header.Get("Content-Length"))
	}
	// In pieces, as clients that fetch in parallel do.
	ranged, _ := http.NewRequest("GET", hit.DownloadURL, nil)
	ranged.Header.Set("Range", "bytes=6-10")
	response, _ = http.DefaultClient.Do(ranged)
	got, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 206 || string(got) != "cache" {
		t.Fatalf("ranged download: %d %q", response.StatusCode, got)
	}

	// Saving the key again is refused, quietly: it is not worth a warning.
	var again answer
	if h.call(token, "CreateCacheEntry", map[string]any{"key": "deps-abc", "version": "v1"}, &again); again.OK || again.Message != "" {
		t.Fatalf("saving an existing key: %+v", again)
	}
	// A restore key finds it from the default branch's scope too, but a job
	// on another ref cannot see a cache saved from this one.
	mainOnly := h.issuer.token(t, "42", func(c map[string]any) { c["ac"] = `[{"Scope":"refs/heads/main","Permission":3}]` })
	if h.call(mainOnly, "GetCacheEntryDownloadURL", lookup, &miss); miss.OK {
		t.Fatal("a job on main restored a cache saved from a feature branch")
	}
}

func TestASmallCacheIsUploadedInOneRequest(t *testing.T) {
	h := newHarness(t, "")
	token := h.issuer.token(t, "42", nil)
	var created, finalized, hit answer
	h.call(token, "CreateCacheEntry", map[string]any{"key": "k", "version": "v"}, &created)
	if status := put(t, created.UploadURL, "one shot"); status != 201 {
		t.Fatalf("upload: %d", status)
	}
	if h.call(token, "FinalizeCacheEntryUpload", map[string]any{"key": "k", "version": "v", "sizeBytes": 8}, &finalized); !finalized.OK {
		t.Fatalf("finalize: %+v", finalized)
	}
	if h.call(token, "GetCacheEntryDownloadURL", map[string]any{"key": "k", "restoreKeys": []string{}, "version": "v"}, &hit); !hit.OK {
		t.Fatal("not restorable")
	}
	// A size that does not match is a failed save, said plainly.
	h.call(token, "CreateCacheEntry", map[string]any{"key": "k2", "version": "v"}, &created)
	put(t, created.UploadURL, "one shot")
	var wrong answer
	if h.call(token, "FinalizeCacheEntryUpload", map[string]any{"key": "k2", "version": "v", "size_bytes": "9"}, &wrong); wrong.OK || wrong.Message == "" {
		t.Fatalf("finalize with the wrong size: %+v", wrong)
	}
}

func TestTheServerTrustsNothingTheJobSays(t *testing.T) {
	h := newHarness(t, "")
	token := h.issuer.token(t, "42", nil)
	lookup := map[string]any{"key": "k", "version": "v"}
	var created, finalized, hit answer
	h.call(token, "CreateCacheEntry", lookup, &created)
	put(t, created.UploadURL, "secret build output")
	h.call(token, "FinalizeCacheEntryUpload", map[string]any{"key": "k", "version": "v", "size_bytes": "19"}, &finalized)
	h.call(token, "GetCacheEntryDownloadURL", lookup, &hit)

	if status := h.call("", "GetCacheEntryDownloadURL", lookup, nil); status != 401 {
		t.Errorf("no token: %d", status)
	}
	if status := h.call("not.a.token", "GetCacheEntryDownloadURL", lookup, nil); status != 401 {
		t.Errorf("a made-up token: %d", status)
	}
	// Another repository's job, with a perfectly good token of its own.
	other := h.issuer.token(t, "43", nil)
	var miss answer
	if h.call(other, "GetCacheEntryDownloadURL", lookup, &miss); miss.OK {
		t.Error("another repository restored this one's cache")
	}
	// Its download address, guessed from this one's.
	guessed := strings.Replace(hit.DownloadURL, "/download/42/", "/download/43/", 1)
	if response, _ := http.Get(guessed); response.StatusCode != 404 {
		t.Errorf("a download under another repository's id: %d", response.StatusCode)
	}
	// A read-only job cannot save.
	readOnly := h.issuer.token(t, "42", func(c map[string]any) { c["ac"] = `[{"Scope":"refs/heads/main","Permission":1}]` })
	var denied answer
	if h.call(readOnly, "CreateCacheEntry", map[string]any{"key": "new", "version": "v"}, &denied); denied.OK || !strings.HasPrefix(denied.Message, "cache write denied:") {
		t.Errorf("a read-only job saving: %+v", denied)
	}
	// The same repository's job in another pool is another world.
	h.pool = "globex"
	if h.call(token, "GetCacheEntryDownloadURL", lookup, &miss); miss.OK {
		t.Error("another pool restored this pool's cache")
	}
	if response, _ := http.Get(hit.DownloadURL); response.StatusCode != 404 {
		t.Errorf("another pool downloaded this pool's cache: %d", response.StatusCode)
	}
	// A VM with no job is served nothing.
	h.pool = ""
	if status := h.call(token, "GetCacheEntryDownloadURL", lookup, nil); status != 403 {
		t.Errorf("a VM with no job: %d", status)
	}
	if response, _ := http.Get(hit.DownloadURL); response.StatusCode != 403 {
		t.Errorf("a VM with no job downloading: %d", response.StatusCode)
	}
	h.pool = "acme"
	// Calls this cache does not have are refused here, not sent to GitHub.
	if status := h.call(token, "DeleteCacheEntry", lookup, nil); status != 404 {
		t.Errorf("an unknown cache call: %d", status)
	}
	for _, address := range []string{
		h.server.URL + "/devstoreaccount1/upload/../../etc",
		h.server.URL + "/devstoreaccount1/upload/zzzz",
		h.server.URL + "/devstoreaccount1/upload/" + strings.Repeat("a", 32),
	} {
		if status := put(t, address, "x"); status == 201 {
			t.Errorf("an upload to %s was accepted", address)
		}
	}
}

// Everything that is not a cache call belongs to GitHub and must reach it
// exactly as the job sent it: artifacts depend on this.
func TestEverythingElseGoesToGitHubUntouched(t *testing.T) {
	var seen *http.Request
	var body []byte
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		fmt.Fprint(w, `{"ok":true,"signed_upload_url":"https://blob.example/x"}`)
	}))
	defer github.Close()
	h := newHarness(t, github.URL)
	h.pool = "" // Even a VM with no job: this traffic is not ours to judge.

	path := "/twirp/github.actions.results.api.v1.ArtifactService/CreateArtifact?x=1"
	request, _ := http.NewRequest("POST", h.server.URL+path, strings.NewReader(`{"name":"build"}`))
	request.Header.Set("Authorization", "Bearer the-jobs-own-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 || !strings.Contains(string(got), "blob.example") {
		t.Fatalf("the job got %d %q", response.StatusCode, got)
	}
	upstream, _ := url.Parse(github.URL)
	if seen == nil || seen.URL.RequestURI() != path || seen.Host != upstream.Host || string(body) != `{"name":"build"}` ||
		seen.Header.Get("Authorization") != "Bearer the-jobs-own-token" {
		t.Fatalf("GitHub received %+v body %q", seen, body)
	}
	// GitHub being unreachable is an error for the job to see, not a hang.
	github.Close()
	response, err = http.Post(h.server.URL+path, "application/json", strings.NewReader("{}"))
	if err != nil || response.StatusCode != 502 {
		t.Fatalf("with GitHub down: %v %v", response, err)
	}
}

// Some clients speak protobuf instead of JSON, and are answered in kind.
func TestProtobufCallsAreAnsweredInProtobuf(t *testing.T) {
	h := newHarness(t, "")
	token := h.issuer.token(t, "42", nil)
	text := func(field int, value string) []byte {
		out := binary.AppendUvarint(nil, uint64(field<<3|2))
		out = binary.AppendUvarint(out, uint64(len(value)))
		return append(out, value...)
	}
	call := func(method string, body []byte) []byte {
		request, _ := http.NewRequest("POST", h.server.URL+servicePath+method, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/protobuf")
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 || response.Header.Get("Content-Type") != "application/protobuf" {
			t.Fatalf("%s: %d %s", method, response.StatusCode, response.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(response.Body)
		return raw
	}
	// A metadata message in field 1 is skipped, as an unknown field is.
	metadata := text(1, "ignored")
	created := call("CreateCacheEntry", append(append(metadata, text(2, "k")...), text(3, "v")...))
	if len(created) < 4 || created[0] != 8 || created[1] != 1 {
		t.Fatalf("create answered %v", created)
	}
	address := string(created[4:])
	if !strings.HasPrefix(address, h.server.URL) {
		t.Fatalf("upload address %q", address)
	}
	put(t, address, "12345")
	size := append(binary.AppendUvarint(nil, 3<<3|0), 5)
	finalized := call("FinalizeCacheEntryUpload", append(append(text(2, "k"), size...), text(4, "v")...))
	if len(finalized) < 3 || finalized[0] != 8 || finalized[1] != 1 || finalized[2] != 2<<3 {
		t.Fatalf("finalize answered %v", finalized)
	}
	hit := call("GetCacheEntryDownloadURL", append(append(text(2, "nope"), text(3, "k")...), text(4, "v")...))
	if !bytes.Contains(hit, []byte("/download/42/")) || !bytes.HasSuffix(hit, text(3, "k")) {
		t.Fatalf("lookup answered %q", hit)
	}
	if miss := call("GetCacheEntryDownloadURL", append(text(2, "nope"), text(4, "v")...)); len(miss) != 0 {
		t.Fatalf("a miss answered %v", miss)
	}
}

// chunked is a body whose length is not known until it ends, as a request
// sent with chunked transfer encoding has.
func chunked(content string) io.Reader { return io.MultiReader(strings.NewReader(content)) }

// leftovers lists the files under the uploads that are not an upload's own
// record: the bytes a save has left on the disk.
func leftovers(t *testing.T, store *Store) (files []string, bytes int64) {
	t.Helper()
	_ = filepath.WalkDir(filepath.Join(store.root, "uploads"), func(path string, item os.DirEntry, err error) error {
		if err != nil || item.IsDir() || item.Name() == "upload.json" {
			return nil
		}
		if info, err := item.Info(); err == nil {
			files = append(files, strings.TrimPrefix(path, store.root))
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

func TestAChunkedUploadCannotPassTheLimit(t *testing.T) {
	store := newStore(t, 1000)
	id, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "k", Version: "v"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBlock("acme", id, "YTo=", chunked(strings.Repeat("a", 600)), -1); err != nil {
		t.Fatalf("a block within the limit: %v", err)
	}
	// Each block is under the limit, but the upload no longer would be.
	if err := store.PutBlock("acme", id, "Yjo=", chunked(strings.Repeat("b", 600)), -1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a block that takes the upload past the limit: %v", err)
	}
	if err := store.PutWhole("acme", id, chunked(strings.Repeat("c", 600)), -1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a whole archive that takes the upload past the limit: %v", err)
	}
	if files, bytes := leftovers(t, store); bytes != 600 || len(files) != 1 {
		t.Fatalf("after refusing the rest, the upload holds %d bytes in %v", bytes, files)
	}
	// What was kept is still a cache.
	if err := store.Commit("acme", id, []string{"YTo="}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize("acme", "1", "s", "k", "v", 600); err != nil {
		t.Fatal(err)
	}

	// The disk is checked as the bytes arrive, not only when their length
	// was declared.
	store.free = func(string) (int64, error) { return minFree + 100, nil }
	id, _ = store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "full", Version: "v"})
	if err := store.PutBlock("acme", id, "YTo=", chunked(strings.Repeat("x", 500)), -1); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("a chunked block onto a nearly full disk: %v", err)
	}
	if files, _ := leftovers(t, store); len(files) != 0 {
		t.Fatalf("a refused block left %v", files)
	}

	// And over HTTP, where a chunked body has no Content-Length.
	h := newHarness(t, "")
	h.store.Limit = func() int64 { return 1000 }
	token := h.issuer.token(t, "42", nil)
	var created answer
	h.call(token, "CreateCacheEntry", map[string]any{"key": "k", "version": "v"}, &created)
	send := func(block, content string) int {
		request, _ := http.NewRequest("PUT", created.UploadURL+"?comp=block&blockid="+block, chunked(content))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := send("YTo=", strings.Repeat("a", 600)); status != 201 {
		t.Fatalf("a chunked block within the limit: %d", status)
	}
	if status := send("Yjo=", strings.Repeat("b", 600)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a chunked block past the limit: %d", status)
	}
	if _, bytes := leftovers(t, h.store); bytes != 600 {
		t.Fatalf("the refused block left the upload holding %d bytes", bytes)
	}
}

func TestUnfinishedUploadsShareTheLimit(t *testing.T) {
	store := newStore(t, 1000)
	var ids []string
	for i := range 20 {
		id, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: fmt.Sprint("k", i), Version: "v"})
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	// Every upload at once, each well within the limit on its own.
	var wait sync.WaitGroup
	var mu sync.Mutex
	var kept []string
	for _, id := range ids {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if store.PutBlock("acme", id, "YTo=", chunked(strings.Repeat("x", 250)), -1) == nil {
				mu.Lock()
				kept = append(kept, id)
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if _, bytes := leftovers(t, store); bytes > 1000 {
		t.Fatalf("%d unfinished uploads hold %d bytes, over the limit of 1000", len(kept), bytes)
	}
	if len(kept) != 4 {
		t.Fatalf("%d of the uploads were kept, want the 4 that fit", len(kept))
	}
	var waiting string
	for _, id := range ids {
		if !contains(kept, id) {
			waiting = id
			break
		}
	}
	// A declared length is held to the same account.
	if err := store.PutBlock("acme", waiting, "YTo=", strings.NewReader(strings.Repeat("y", 250)), 250); err == nil {
		t.Fatal("a declared block past the limit was kept")
	}
	// While the uploads already fill it, a new save is not begun.
	if _, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: "later", Version: "v"}); err == nil {
		t.Fatal("a save was begun while unfinished uploads fill the limit")
	}

	// Finishing one gives its room back.
	done := kept[0]
	if err := store.Commit("acme", done, []string{"YTo="}); err != nil {
		t.Fatal(err)
	}
	var key string
	for i, id := range ids {
		if id == done {
			key = fmt.Sprint("k", i)
		}
	}
	if _, err := store.Finalize("acme", "1", "s", key, "v", 250); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBlock("acme", waiting, "YTo=", chunked(strings.Repeat("z", 250)), -1); err != nil {
		t.Fatalf("an upload after another finished: %v", err)
	}

	// However small, uploads cannot be begun without end.
	small := newStore(t, 1000)
	begun := 0
	for i := range 1000 {
		if _, err := small.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: fmt.Sprint("k", i), Version: "v"}); err != nil {
			break
		}
		begun++
	}
	if begun == 1000 {
		t.Fatal("a thousand saves were begun and left unfinished")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestAbandonedUploadsAreSweptSoon(t *testing.T) {
	store := newStore(t, 0)
	begin := func(key string) string {
		id, err := store.Begin(Entry{Pool: "acme", Repo: "1", Scope: "s", Key: key, Version: "v"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutBlock("acme", id, "YTo=", strings.NewReader("bytes"), 5); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// age makes an upload look as if it began, and last heard from its
	// job, some time ago.
	age := func(id string, started, heard time.Duration) {
		dir := filepath.Join(store.root, "uploads", id)
		pending, _ := readUpload(dir)
		pending.Started = time.Now().Add(-started)
		if err := writeJSON(filepath.Join(dir, "upload.json"), pending); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-heard)
		_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, _ error) error {
			return os.Chtimes(path, when, when)
		})
	}
	idle, slow, busy := begin("idle"), begin("slow"), begin("busy")
	age(idle, 30*time.Minute, 30*time.Minute)
	age(slow, 3*time.Hour, time.Minute)
	age(busy, time.Minute, time.Minute)

	// As the daemon finds them when it starts again.
	reopened, err := Open(store.root)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Limit, reopened.free = store.Limit, store.free
	reopened.Trim()
	exists := func(id string) bool {
		_, err := os.Stat(filepath.Join(store.root, "uploads", id))
		return err == nil
	}
	if exists(idle) {
		t.Error("an upload not heard from in half an hour was kept")
	}
	if exists(slow) {
		t.Error("an upload begun three hours ago was kept")
	}
	if !exists(busy) {
		t.Error("an upload in progress was swept")
	}
}
