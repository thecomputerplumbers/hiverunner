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
	ErrTooLarge = errors.New("this cache is larger than this machine will keep")
	ErrBusy     = errors.New("too many caches are being saved on this machine at once")
	ErrNotFound = errors.New("no such cache")
	ErrSize     = errors.New("the uploaded cache is not the size the job reported")
)

const (
	// minFree is the disk space a save must leave on the machine.
	minFree = 5 << 30
	// checkEvery is how many bytes a write takes between looks at the disk.
	checkEvery = 64 << 20
	// maxEntry is the largest one cache may be, as on GitHub.
	maxEntry = 10 << 30
	// maxUploads is how many saves may be unfinished at once, and maxBlocks
	// how many blocks they may hold between them: as many as one blob may
	// have in blob storage.
	maxUploads = 64
	maxBlocks  = 50_000
	// An unfinished upload is abandoned once its job has sent it nothing for
	// idleUpload, or it began more than oldUpload ago.
	idleUpload = 10 * time.Minute
	oldUpload  = 2 * time.Hour
)

var (
	safeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	numeric  = regexp.MustCompile(`^[0-9]{1,20}$`)
	hexName  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Store keeps entries as files under one directory. What is on disk is the
// whole truth about entries: nothing is remembered between calls, so `hived
// cache clear` and a running daemon cannot disagree. Only what unfinished
// uploads hold is counted in memory, since bytes still arriving are on no
// disk yet.
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
	// quota guards held. It may be taken while holding mu, never the
	// other way around.
	quota sync.Mutex
	held  *held
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

// most is the largest one cache may be.
func (s *Store) most() int64 {
	if limit := s.limit(); limit > 0 {
		return min(limit, maxEntry)
	}
	return maxEntry
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
	if err := s.admit(id); err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, "uploads", id)
	if err := os.MkdirAll(filepath.Join(dir, "blocks"), 0o700); err != nil {
		s.forget(id)
		return "", err
	}
	entry.ID, entry.Secret, entry.Size = "", "", 0
	if err := writeJSON(filepath.Join(dir, "upload.json"), upload{Entry: entry, Started: time.Now()}); err != nil {
		s.forget(id)
		os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

// sweepUploads removes saves that were started and abandoned. The caller
// holds mu, so no upload is being begun or finalized meanwhile.
func (s *Store) sweepUploads() {
	listed, _ := os.ReadDir(filepath.Join(s.root, "uploads"))
	started := map[string]time.Time{}
	for _, item := range listed {
		if pending, ok := readUpload(filepath.Join(s.root, "uploads", item.Name())); ok {
			started[item.Name()] = pending.Started
		}
	}
	var doomed []string
	s.quota.Lock()
	account := s.account()
	for _, item := range listed {
		began, ok := started[item.Name()]
		upload := account.uploads[item.Name()]
		if ok && upload != nil && time.Since(upload.heard) < idleUpload && time.Since(began) < oldUpload {
			continue
		}
		account.forget(item.Name())
		doomed = append(doomed, filepath.Join(s.root, "uploads", item.Name()))
	}
	// An upload whose directory is gone holds nothing.
	for id := range account.uploads {
		if _, ok := started[id]; !ok {
			account.forget(id)
		}
	}
	s.quota.Unlock()
	for _, dir := range doomed {
		os.RemoveAll(dir)
	}
}

// room refuses bytes that could never be kept, before they are written.
func (s *Store) room(dir string, more int64) error {
	if more > s.most() {
		return ErrTooLarge
	}
	if free, err := s.free(dir); err == nil && free-more < minFree {
		return ErrNoRoom
	}
	return nil
}

// held is what the unfinished uploads hold between them, on the disk and
// still arriving, so that together they stay within the limit however
// their bytes are sent.
type held struct {
	uploads map[string]*heldUpload
	bytes   int64
	blocks  int
}

// heldUpload is one unfinished upload's share.
type heldUpload struct {
	bytes  int64
	blocks int
	// heard is when its job last sent it anything.
	heard time.Time
	// gone is set once it is finalized or swept, so a write still arriving
	// for it stops and does not give back what is no longer counted.
	gone bool
}

// account returns what the uploads hold, reading it from the disk the
// first time: the uploads a daemon left behind still take up room. The
// caller holds quota.
func (s *Store) account() *held {
	if s.held != nil {
		return s.held
	}
	s.held = &held{uploads: map[string]*heldUpload{}}
	listed, _ := os.ReadDir(filepath.Join(s.root, "uploads"))
	for _, item := range listed {
		dir := filepath.Join(s.root, "uploads", item.Name())
		pending, ok := readUpload(dir)
		if !ok {
			continue
		}
		upload := &heldUpload{heard: pending.Started}
		_ = filepath.WalkDir(dir, func(path string, file os.DirEntry, err error) error {
			if err != nil || file.IsDir() || path == filepath.Join(dir, "upload.json") {
				return nil
			}
			info, err := file.Info()
			if err != nil {
				return nil
			}
			upload.bytes += info.Size()
			if filepath.Dir(path) == filepath.Join(dir, "blocks") {
				upload.blocks++
			}
			if info.ModTime().After(upload.heard) {
				upload.heard = info.ModTime()
			}
			return nil
		})
		s.held.uploads[item.Name()] = upload
		s.held.bytes += upload.bytes
		s.held.blocks += upload.blocks
	}
	return s.held
}

// forget stops counting an upload. The caller holds quota.
func (h *held) forget(id string) {
	upload := h.uploads[id]
	if upload == nil {
		return
	}
	upload.gone = true
	h.bytes -= upload.bytes
	h.blocks -= upload.blocks
	delete(h.uploads, id)
}

func (s *Store) forget(id string) {
	s.quota.Lock()
	defer s.quota.Unlock()
	s.account().forget(id)
}

// admit starts counting a new upload, if there is room for one.
func (s *Store) admit(id string) error {
	limit := s.limit()
	s.quota.Lock()
	defer s.quota.Unlock()
	account := s.account()
	if len(account.uploads) >= maxUploads {
		return ErrBusy
	}
	if limit > 0 && account.bytes >= limit {
		return ErrBusy
	}
	account.uploads[id] = &heldUpload{heard: time.Now()}
	return nil
}

// lease is one write's claim on room for its upload.
type lease struct {
	store  *Store
	upload *heldUpload
	dir    string
	// limit is the most all uploads may hold, and most what this one may.
	limit, most int64
	// claimed is what this write has taken from the account and not yet
	// settled, and arrived how many bytes of its body have been read.
	claimed, arrived int64
	// The disk is looked at again once arrived reaches check.
	check int64
}

// lease begins a write to an upload. A block is refused once the uploads
// hold as many as they may.
func (s *Store) lease(id, dir string, block bool) (*lease, error) {
	limit, most := s.limit(), s.most()
	s.quota.Lock()
	defer s.quota.Unlock()
	account := s.account()
	upload := account.uploads[id]
	if upload == nil {
		return nil, ErrNotFound
	}
	if block && account.blocks >= maxBlocks {
		return nil, ErrBusy
	}
	upload.heard = time.Now()
	return &lease{store: s, upload: upload, dir: dir, limit: limit, most: most}, nil
}

// claim takes room for n more bytes, if the upload and all the uploads
// together stay within their limits.
func (l *lease) claim(n int64) error {
	s := l.store
	s.quota.Lock()
	defer s.quota.Unlock()
	if l.upload.gone {
		return ErrNotFound
	}
	l.upload.heard = time.Now()
	if l.upload.bytes+n > l.most {
		return ErrTooLarge
	}
	if l.limit > 0 && s.held.bytes+n > l.limit {
		return ErrBusy
	}
	l.upload.bytes += n
	s.held.bytes += n
	l.claimed += n
	return nil
}

// arrive counts n bytes of the body as read, before they are written.
func (l *lease) arrive(n int64) error {
	l.arrived += n
	if l.arrived >= l.check {
		if free, err := l.store.free(l.dir); err == nil && free-checkEvery < minFree {
			return ErrNoRoom
		}
		l.check = l.arrived + checkEvery
	}
	if more := l.arrived - l.claimed; more > 0 {
		return l.claim(more)
	}
	return nil
}

// settle ends the write. Of what it claimed, kept bytes stay on the disk
// and the rest is given back, with the freed bytes it replaced or removed.
// blocks is how many blocks it added, or removed if negative. Settling
// again gives back nothing more.
func (l *lease) settle(kept, freed int64, blocks int) {
	s := l.store
	s.quota.Lock()
	defer s.quota.Unlock()
	change := kept - l.claimed - freed
	l.claimed = 0
	if l.upload.gone {
		return
	}
	// Writes racing on one block can make the disk hold less than was
	// counted, never more; the account only errs on the side of full.
	change = max(change, -l.upload.bytes)
	blocks = max(blocks, -l.upload.blocks)
	l.upload.bytes += change
	s.held.bytes += change
	l.upload.blocks += blocks
	s.held.blocks += blocks
}

// metered claims room for a body's bytes as they arrive, and stops it once
// its upload would be too large or the disk too full.
type metered struct {
	body  io.Reader
	lease *lease
}

func (m *metered) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 {
		if refused := m.lease.arrive(int64(n)); refused != nil {
			return 0, refused
		}
	}
	return n, err
}

// write copies a request body to a file that only appears once it is
// whole. Its bytes count against the limits as they arrive, so a body
// whose length was never declared is held to them too.
func (s *Store) write(id, path string, body io.Reader, expected int64, block bool) error {
	dir := filepath.Dir(path)
	lease, err := s.lease(id, dir, block)
	if err != nil {
		return err
	}
	// Whatever was claimed and not kept is given back.
	defer lease.settle(0, 0, 0)
	if expected > 0 {
		if err := s.room(dir, expected); err != nil {
			return err
		}
		if err := lease.claim(expected); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(dir, ".part-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	written, err := io.Copy(temporary, &metered{body: body, lease: lease})
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	var replaced int64
	added := 0
	if info, err := os.Stat(path); err == nil {
		replaced = info.Size()
	} else if block {
		added = 1
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	lease.settle(written, replaced, added)
	return nil
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
	return s.write(id, filepath.Join(dir, "blocks", hex.EncodeToString([]byte(blockID))), body, expected, true)
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
	lease, err := s.lease(id, dir, false)
	if err != nil {
		return err
	}
	defer lease.settle(0, 0, 0)
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
	// The archive replaces every block. A block listed twice makes it
	// larger than what arrived, and the difference needs room of its own.
	blocks, count := stored(dir)
	if total > blocks {
		if err := lease.claim(total - blocks); err != nil {
			return err
		}
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
	var replaced int64
	if info, err := os.Stat(filepath.Join(dir, "blob")); err == nil {
		replaced = info.Size()
	}
	if err := os.Rename(temporary.Name(), filepath.Join(dir, "blob")); err != nil {
		return err
	}
	// The blocks have been copied; do not keep the archive twice.
	err = os.RemoveAll(filepath.Join(dir, "blocks"))
	lease.settle(total, blocks+replaced, -count)
	return err
}

// stored is what an upload's finished blocks hold. A block still arriving
// is left out: its own write counts it.
func stored(dir string) (bytes int64, count int) {
	listed, _ := os.ReadDir(filepath.Join(dir, "blocks"))
	for _, item := range listed {
		if strings.HasPrefix(item.Name(), ".part-") {
			continue
		}
		if info, err := item.Info(); err == nil {
			bytes += info.Size()
			count++
		}
	}
	return bytes, count
}

// PutWhole stores an upload sent as a single request.
func (s *Store) PutWhole(pool, id string, body io.Reader, expected int64) error {
	dir, err := s.pending(pool, id)
	if err != nil {
		return err
	}
	return s.write(id, filepath.Join(dir, "blob"), body, expected, false)
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
	defer func() {
		s.forget(filepath.Base(dir))
		os.RemoveAll(dir)
	}()

	blob, err := os.Stat(filepath.Join(dir, "blob"))
	if err != nil {
		return Entry{}, ErrNotFound
	}
	if size >= 0 && blob.Size() != size {
		return Entry{}, ErrSize
	}
	if blob.Size() > s.most() {
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

// Trim brings the cache within its limit now. The daemon calls it on a
// timer, which is how a lowered limit takes effect without a save.
func (s *Store) Trim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Also the saves a cancelled job began and never finished.
	s.sweepUploads()
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
