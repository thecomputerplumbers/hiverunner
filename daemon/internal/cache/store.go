// Package cache is a GitHub Actions cache kept on the machine that runs the
// jobs. A job's `actions/cache` step talks to it instead of GitHub's, so a
// restore is a copy from this Mac's disk rather than a download.
//
// Entries belong to one repository in one pool and are never visible to
// another. Nothing here is precious: an entry that is lost is a cache miss.
package cache

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Entry is one saved cache: an archive a job uploaded under a key.
type Entry struct {
	// ID is a positive number, because the protocol reports it as one.
	ID string `json:"id"`
	// Pool and Repo say whose it is. Repo is GitHub's repository id.
	Pool string `json:"pool"`
	Repo string `json:"repo"`
	// RepoName is `owner/name` as GitHub's token gave it, for people.
	RepoName string `json:"repoName,omitempty"`
	// Scope is the ref the entry was saved from, as GitHub scopes caches.
	Scope   string `json:"scope"`
	Key     string `json:"key"`
	Version string `json:"version"`
	Size    int64  `json:"size"`
	// Secret makes the download address unguessable: the client that
	// downloads sends no credential.
	Secret  string    `json:"secret"`
	Created time.Time `json:"created"`
	// Accessed is not stored: it is the entry file's modification time.
	Accessed time.Time `json:"-"`
}

// Reasons a save is refused. Each is an ordinary outcome, not a fault.
var (
	ErrExists   = errors.New("a cache with this key already exists")
	ErrNoRoom   = errors.New("this machine's disk is too full to save a cache")
	ErrTooLarge = errors.New("this cache is larger than the machine's whole cache")
	ErrNotFound = errors.New("no such cache")
	ErrSize     = errors.New("the uploaded cache is not the size the job reported")
)

const (
	// minFree is the disk space a save must leave on the machine.
	minFree = 5 << 30
	// staleUpload is how long an unfinished upload is kept.
	staleUpload = 24 * time.Hour
)

var (
	safeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	numeric  = regexp.MustCompile(`^[0-9]{1,20}$`)
	hexName  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Store keeps entries as files under one directory. What is on disk is the
// whole truth: nothing is remembered between calls, so `hived cache clear`
// and a running daemon cannot disagree.
type Store struct {
	root string
	// Limit is the most the entries may occupy, in bytes. It is asked on
	// every save, so the owner can change it while the daemon runs.
	Limit func() int64
	// free reports the bytes available on the volume. Tests replace it.
	free func(dir string) (int64, error)

	// mu orders saves, evictions and clears. Uploading bytes and
	// downloading do not hold it.
	mu sync.Mutex
}

// Open returns the store rooted at dir, creating it.
func Open(dir string) (*Store, error) {
	for _, sub := range []string{"pools", "uploads"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{root: dir, free: freeBytes}, nil
}

// DefaultDir is where the cache lives: with the user's other caches, which
// backups skip. HIVE_CACHE overrides it.
func DefaultDir() (string, error) {
	if override := os.Getenv("HIVE_CACHE"); override != "" {
		return override, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "HiveWorker", "actions-cache"), nil
}

// GuestPort is the port of the private socket a VM reaches the cache on.
const GuestPort = 47071

// AutoLimit is the cache size an owner who has not chosen one gets: a tenth
// of the disk, but never less than 10 GB or more than 50.
func AutoLimit(dir string) int64 {
	const gib = 1 << 30
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 20 * gib
	}
	tenth := int64(stat.Blocks) * int64(stat.Bsize) / 10 / gib * gib
	return min(max(tenth, 10*gib), 50*gib)
}

func freeBytes(dir string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func (s *Store) limit() int64 {
	if s.Limit == nil {
		return 0
	}
	return s.Limit()
}

func (s *Store) repoDir(pool, repo string) (string, error) {
	if !safeName.MatchString(pool) || strings.Trim(pool, ".") == "" {
		return "", fmt.Errorf("unusable pool id")
	}
	if !numeric.MatchString(repo) {
		return "", fmt.Errorf("unusable repository id")
	}
	return filepath.Join(s.root, "pools", pool, repo), nil
}

func readEntry(dir string) (Entry, bool) {
	meta := filepath.Join(dir, "entry.json")
	raw, err := os.ReadFile(meta)
	if err != nil {
		return Entry{}, false
	}
	var entry Entry
	if json.Unmarshal(raw, &entry) != nil || entry.ID == "" {
		return Entry{}, false
	}
	blob, err := os.Stat(filepath.Join(dir, "blob"))
	if err != nil || blob.Size() != entry.Size {
		return Entry{}, false
	}
	if info, err := os.Stat(meta); err == nil {
		entry.Accessed = info.ModTime()
	}
	return entry, true
}

// entries lists a repository's entries. One that is damaged is skipped, not
// an error: it will be evicted like any other in time.
func (s *Store) entries(pool, repo string) []Entry {
	dir, err := s.repoDir(pool, repo)
	if err != nil {
		return nil
	}
	listed, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []Entry
	for _, item := range listed {
		if entry, ok := readEntry(filepath.Join(dir, item.Name())); ok && entry.Pool == pool && entry.Repo == repo {
			found = append(found, entry)
		}
	}
	return found
}

// all lists every entry of every repository.
func (s *Store) all() []Entry {
	var found []Entry
	pools, _ := os.ReadDir(filepath.Join(s.root, "pools"))
	for _, pool := range pools {
		repos, _ := os.ReadDir(filepath.Join(s.root, "pools", pool.Name()))
		for _, repo := range repos {
			found = append(found, s.entries(pool.Name(), repo.Name())...)
		}
	}
	return found
}

// Find returns the entry a restore should get, as GitHub chooses one: in
// each scope in turn, and within a scope each key in turn, first the entry
// with exactly that key and then the newest whose key starts with it. The
// version must always match exactly.
func (s *Store) Find(pool, repo string, scopes, keys []string, version string) (Entry, bool) {
	candidates := s.entries(pool, repo)
	// Newest first, so the first prefix match is the newest.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Created.After(candidates[j].Created) })
	for _, scope := range scopes {
		for _, key := range keys {
			if key == "" {
				continue
			}
			for _, prefix := range []bool{false, true} {
				for _, entry := range candidates {
					if entry.Scope != scope || entry.Version != version {
						continue
					}
					if entry.Key == key || (prefix && strings.HasPrefix(entry.Key, key)) {
						s.touch(entry)
						return entry, true
					}
				}
			}
		}
	}
	return Entry{}, false
}

// touch records that an entry was wanted, which is what keeps it from being
// the next one evicted.
func (s *Store) touch(entry Entry) {
	if dir, err := s.repoDir(entry.Pool, entry.Repo); err == nil {
		now := time.Now()
		_ = os.Chtimes(filepath.Join(dir, entry.ID, "entry.json"), now, now)
	}
}

// Blob opens an entry's archive for download. The secret must be the
// entry's own.
func (s *Store) Blob(pool, repo, id, secret string) (*os.File, Entry, error) {
	dir, err := s.repoDir(pool, repo)
	if err != nil || !numeric.MatchString(id) {
		return nil, Entry{}, ErrNotFound
	}
	entry, ok := readEntry(filepath.Join(dir, id))
	if !ok || entry.Pool != pool || entry.Repo != repo || !sameSecret(entry.Secret, secret) {
		return nil, Entry{}, ErrNotFound
	}
	file, err := os.Open(filepath.Join(dir, id, "blob"))
	if err != nil {
		return nil, Entry{}, ErrNotFound
	}
	return file, entry, nil
}

func sameSecret(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// upload is a save in progress. It becomes an Entry when finalized.
type upload struct {
	Entry
	Started time.Time `json:"started"`
}

func (s *Store) uploadDir(id string) (string, error) {
	if !hexName.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.root, "uploads", id), nil
}

// pending finds an upload that belongs to this pool. Another pool's upload
// does not exist, as far as this one can tell.
func (s *Store) pending(pool, id string) (string, error) {
	dir, err := s.uploadDir(id)
	if err != nil {
		return "", err
	}
	if found, ok := readUpload(dir); !ok || found.Pool != pool {
		return "", ErrNotFound
	}
	return dir, nil
}

func readUpload(dir string) (upload, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "upload.json"))
	if err != nil {
		return upload{}, false
	}
	var pending upload
	if json.Unmarshal(raw, &pending) != nil {
		return upload{}, false
	}
	return pending, true
}

// Begin reserves a save and returns the id its bytes are uploaded under. It
// refuses a key that is already saved in that scope: caches are immutable,
// as GitHub's are.
func (s *Store) Begin(entry Entry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.repoDir(entry.Pool, entry.Repo); err != nil {
		return "", err
	}
	if entry.Key == "" || entry.Version == "" || entry.Scope == "" {
		return "", fmt.Errorf("a cache needs a key, a version and a scope")
	}
	for _, existing := range s.entries(entry.Pool, entry.Repo) {
		if existing.Scope == entry.Scope && existing.Key == entry.Key && existing.Version == entry.Version {
			return "", ErrExists
		}
	}
	s.sweepUploads()
	if free, err := s.free(s.root); err == nil && free < minFree {
		return "", ErrNoRoom
	}
	id, err := randomHex()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, "uploads", id)
	if err := os.MkdirAll(filepath.Join(dir, "blocks"), 0o700); err != nil {
		return "", err
	}
	entry.ID, entry.Secret, entry.Size = "", "", 0
	if err := writeJSON(filepath.Join(dir, "upload.json"), upload{Entry: entry, Started: time.Now()}); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

// sweepUploads removes saves that were started and abandoned.
func (s *Store) sweepUploads() {
	listed, _ := os.ReadDir(filepath.Join(s.root, "uploads"))
	for _, item := range listed {
		dir := filepath.Join(s.root, "uploads", item.Name())
		pending, ok := readUpload(dir)
		if !ok || time.Since(pending.Started) > staleUpload {
			os.RemoveAll(dir)
		}
	}
}

// room refuses bytes that could never be kept, before they are written.
func (s *Store) room(dir string, more int64) error {
	if limit := s.limit(); limit > 0 && more > limit {
		return ErrTooLarge
	}
	if free, err := s.free(dir); err == nil && free-more < minFree {
		return ErrNoRoom
	}
	return nil
}

// write copies a request body to a file that only appears once it is whole.
func (s *Store) write(path string, body io.Reader, expected int64) error {
	if expected > 0 {
		if err := s.room(filepath.Dir(path), expected); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".part-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	limit := s.limit()
	if limit <= 0 {
		limit = 1 << 62
	}
	// One byte past the limit is enough to know it was exceeded.
	written, err := io.Copy(temporary, io.LimitReader(body, limit+1))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > limit {
		return ErrTooLarge
	}
	return os.Rename(temporary.Name(), path)
}

// PutBlock stores one block of an upload. Blocks may arrive in any order and
// at the same time; a block sent twice replaces itself.
func (s *Store) PutBlock(pool, id, blockID string, body io.Reader, expected int64) error {
	dir, err := s.pending(pool, id)
	if err != nil {
		return err
	}
	if blockID == "" || len(blockID) > 512 {
		return fmt.Errorf("unusable block id")
	}
	// Block ids are the client's; their hex is a safe file name.
	return s.write(filepath.Join(dir, "blocks", hex.EncodeToString([]byte(blockID))), body, expected)
}

// Commit assembles an upload from its blocks, in the order given.
func (s *Store) Commit(pool, id string, blockIDs []string) error {
	dir, err := s.pending(pool, id)
	if err != nil {
		return err
	}
	if len(blockIDs) == 0 {
		return fmt.Errorf("an upload needs at least one block")
	}
	var total int64
	for _, blockID := range blockIDs {
		info, err := os.Stat(filepath.Join(dir, "blocks", hex.EncodeToString([]byte(blockID))))
		if err != nil {
			return fmt.Errorf("the block list names a block that was never uploaded")
		}
		total += info.Size()
	}
	if err := s.room(dir, total); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".blob-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	for _, blockID := range blockIDs {
		block, err := os.Open(filepath.Join(dir, "blocks", hex.EncodeToString([]byte(blockID))))
		if err != nil {
			temporary.Close()
			return err
		}
		_, err = io.Copy(temporary, block)
		block.Close()
		if err != nil {
			temporary.Close()
			return err
		}
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), filepath.Join(dir, "blob")); err != nil {
		return err
	}
	// The blocks have been copied; do not keep the archive twice.
	return os.RemoveAll(filepath.Join(dir, "blocks"))
}

// PutWhole stores an upload sent as a single request.
func (s *Store) PutWhole(pool, id string, body io.Reader, expected int64) error {
	dir, err := s.pending(pool, id)
	if err != nil {
		return err
	}
	return s.write(filepath.Join(dir, "blob"), body, expected)
}

// Finalize turns the upload for a key into an entry restores can find, then
// makes room for it if the cache is over its limit. A size of zero or more
// must match what was uploaded; a negative size is not checked.
func (s *Store) Finalize(pool, repo, scope, key, version string, size int64) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.repoDir(pool, repo)
	if err != nil {
		return Entry{}, err
	}
	// The newest finished upload for this key is the one being finalized.
	var dir string
	var pending upload
	listed, _ := os.ReadDir(filepath.Join(s.root, "uploads"))
	for _, item := range listed {
		candidateDir := filepath.Join(s.root, "uploads", item.Name())
		candidate, ok := readUpload(candidateDir)
		if !ok || candidate.Pool != pool || candidate.Repo != repo || candidate.Scope != scope || candidate.Key != key || candidate.Version != version {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidateDir, "blob")); err != nil {
			continue
		}
		if dir == "" || candidate.Started.After(pending.Started) {
			dir, pending = candidateDir, candidate
		}
	}
	if dir == "" {
		return Entry{}, ErrNotFound
	}
	// Whatever happens next, this upload is finished with.
	defer os.RemoveAll(dir)

	blob, err := os.Stat(filepath.Join(dir, "blob"))
	if err != nil {
		return Entry{}, ErrNotFound
	}
	if size >= 0 && blob.Size() != size {
		return Entry{}, ErrSize
	}
	if limit := s.limit(); limit > 0 && blob.Size() > limit {
		return Entry{}, ErrTooLarge
	}
	// Two jobs may save the same key at once. The first to finish is the
	// cache; the second is told it succeeded, since what it wanted exists.
	for _, existing := range s.entries(pool, repo) {
		if existing.Scope == scope && existing.Key == key && existing.Version == version {
			return existing, nil
		}
	}

	entry := pending.Entry
	entry.Size = blob.Size()
	entry.Created = time.Now()
	if entry.Secret, err = randomHex(); err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return Entry{}, err
	}
	for attempt := 0; ; attempt++ {
		if entry.ID, err = randomNumber(); err != nil {
			return Entry{}, err
		}
		if _, err := os.Stat(filepath.Join(target, entry.ID)); errors.Is(err, os.ErrNotExist) {
			break
		}
		if attempt > 8 {
			return Entry{}, fmt.Errorf("could not name the cache entry")
		}
	}
	_ = os.Remove(filepath.Join(dir, "upload.json"))
	_ = os.RemoveAll(filepath.Join(dir, "blocks"))
	if err := writeJSON(filepath.Join(dir, "entry.json"), entry); err != nil {
		return Entry{}, err
	}
	// The rename is the moment the entry exists: whole, or not at all.
	if err := os.Rename(dir, filepath.Join(target, entry.ID)); err != nil {
		return Entry{}, err
	}
	entry.Accessed = entry.Created
	s.evict(entry.ID)
	return entry, nil
}

// evict removes the entries wanted longest ago until the cache is back
// under nine tenths of its limit, so it is not evicting on every save. The
// entry just saved is spared.
func (s *Store) evict(spare string) {
	limit := s.limit()
	if limit <= 0 {
		return
	}
	entries := s.all()
	var total int64
	for _, entry := range entries {
		total += entry.Size
	}
	if total <= limit {
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Accessed.Before(entries[j].Accessed) })
	for _, entry := range entries {
		if total <= limit/10*9 {
			return
		}
		if entry.ID == spare {
			continue
		}
		if s.remove(entry) == nil {
			total -= entry.Size
		}
	}
}

// remove deletes an entry so that no restore can find it half gone: it is
// moved out of sight first. A download already reading it keeps its file.
func (s *Store) remove(entry Entry) error {
	dir, err := s.repoDir(entry.Pool, entry.Repo)
	if err != nil {
		return err
	}
	name, err := randomHex()
	if err != nil {
		return err
	}
	// Under uploads, where a crash between the two steps is swept up.
	doomed := filepath.Join(s.root, "uploads", name)
	if err := os.Rename(filepath.Join(dir, entry.ID), doomed); err != nil {
		return err
	}
	return os.RemoveAll(doomed)
}

// Trim brings the cache within its limit now. The daemon calls it when the
// owner lowers the limit.
func (s *Store) Trim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict("")
}

// RepoUsage is what one repository's caches occupy.
type RepoUsage struct {
	Pool     string
	Repo     string
	RepoName string
	Entries  int
	Bytes    int64
	// Used is when any of its entries was last wanted.
	Used time.Time
}

// Usage reports what the cache holds, largest repository first.
func (s *Store) Usage() (repos []RepoUsage, total int64) {
	byRepo := map[string]*RepoUsage{}
	for _, entry := range s.all() {
		id := entry.Pool + "/" + entry.Repo
		usage := byRepo[id]
		if usage == nil {
			usage = &RepoUsage{Pool: entry.Pool, Repo: entry.Repo}
			byRepo[id] = usage
		}
		if entry.RepoName != "" {
			usage.RepoName = entry.RepoName
		}
		usage.Entries++
		usage.Bytes += entry.Size
		if entry.Accessed.After(usage.Used) {
			usage.Used = entry.Accessed
		}
		total += entry.Size
	}
	for _, usage := range byRepo {
		repos = append(repos, *usage)
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Bytes != repos[j].Bytes {
			return repos[i].Bytes > repos[j].Bytes
		}
		return repos[i].Pool+repos[i].Repo < repos[j].Pool+repos[j].Repo
	})
	return repos, total
}

// Clear deletes entries and returns how many bytes that freed. An empty
// match clears everything; otherwise it clears the repositories whose name
// or id is the match.
func (s *Store) Clear(match string) (freed int64, cleared int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.all() {
		if match != "" && !strings.EqualFold(entry.RepoName, match) && entry.Repo != match {
			continue
		}
		if s.remove(entry) == nil {
			freed += entry.Size
			cleared++
		}
	}
	return freed, cleared
}

func writeJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func randomHex() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// randomNumber is a positive integer a JavaScript client can hold exactly.
func randomNumber() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<52))
	if err != nil {
		return "", err
	}
	return n.Add(n, big.NewInt(1)).String(), nil
}
