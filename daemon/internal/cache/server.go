package cache

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// DefaultUpstream is GitHub's results service on github.com. A job is told
// to send its results traffic here instead, so everything that is not a
// cache call is passed on to it untouched: artifacts, above all.
const DefaultUpstream = "https://results-receiver.actions.githubusercontent.com"

const servicePath = "/twirp/github.actions.results.api.v1.CacheService/"

// HealthPath answers 200 to anyone. A guest asks it before trusting the
// cache with a job's steps.
const HealthPath = "/hive/health"

// Server answers the cache calls of the jobs on this machine.
type Server struct {
	Store    *Store
	Verifier Verifier
	Log      *slog.Logger
	// Upstream is where everything that is not a cache call goes. Nil is
	// DefaultUpstream.
	Upstream *url.URL
}

// New returns a server over a store, verifying tokens with GitHub.
func New(store *Store, log *slog.Logger) *Server {
	return &Server{Store: store, Verifier: &GitHub{}, Log: log}
}

func (s *Server) upstream() *url.URL {
	if s.Upstream != nil {
		return s.Upstream
	}
	upstream, _ := url.Parse(DefaultUpstream)
	return upstream
}

// Handler serves one VM. pool says whose job the VM is running, or "" when
// it has none yet; the VM cannot choose it. Each VM gets its own handler on
// its own private socket, so a request's pool is never taken on trust.
func (s *Server) Handler(pool func() string) http.Handler {
	upstream := s.upstream()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(upstream)
			request.Out.Host = upstream.Host
		},
		// Results calls are small and should not sit in a buffer.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.Log.Warn("could not pass a results call on to GitHub", "path", r.URL.Path, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST "+servicePath+"{method}", func(w http.ResponseWriter, r *http.Request) {
		s.call(w, r, pool())
	})
	// The path GitHub's upload client expects of a storage account, so it
	// reads the address as one.
	mux.HandleFunc("PUT /devstoreaccount1/upload/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.upload(w, r, pool())
	})
	for _, method := range []string{"GET", "HEAD"} {
		mux.HandleFunc(method+" /download/{repo}/{id}/{secret}", func(w http.ResponseWriter, r *http.Request) {
			s.download(w, r, pool())
		})
	}
	mux.Handle("/", proxy)
	return mux
}

// base is the address the job reached this server at, which is the one its
// uploads and downloads must use too.
func base(r *http.Request) string { return "http://" + r.Host }

func (s *Server) call(w http.ResponseWriter, r *http.Request, pool string) {
	if pool == "" {
		writeFault(w, http.StatusForbidden, "permission_denied", "this VM is not running a job")
		return
	}
	var which call
	switch r.PathValue("method") {
	case "CreateCacheEntry":
		which = callCreate
	case "FinalizeCacheEntryUpload":
		which = callFinalize
	case "GetCacheEntryDownloadURL":
		which = callLookup
	default:
		writeFault(w, http.StatusNotFound, "bad_route", "this cache has no such call")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeFault(w, http.StatusUnauthorized, "unauthenticated", "the job sent no token")
		return
	}
	identity, err := s.Verifier.Verify(r.Context(), token)
	if err != nil {
		// Why goes to the owner's log. The job learns only that it failed.
		s.Log.Warn("refused a cache call", "pool", pool, "reason", err)
		writeFault(w, http.StatusUnauthorized, "unauthenticated", ErrToken.Error())
		return
	}
	body, err := readCall(r, which)
	if err != nil {
		writeFault(w, http.StatusBadRequest, "malformed", err.Error())
		return
	}
	if body.Key == "" || body.Version == "" {
		writeFault(w, http.StatusBadRequest, "invalid_argument", "a cache call needs a key and a version")
		return
	}
	repo := identity.RepoName
	if repo == "" {
		repo = identity.Repo
	}

	switch which {
	case callLookup:
		keys := append([]string{body.Key}, body.RestoreKeys...)
		entry, found := s.Store.Find(pool, identity.Repo, identity.Read, keys, body.Version)
		if !found {
			s.Log.Info("cache miss", "repository", repo, "key", body.Key)
			writeCall(w, r, which, response{})
			return
		}
		s.Log.Info("cache hit", "repository", repo, "key", entry.Key, "bytes", entry.Size)
		writeCall(w, r, which, response{
			OK:          true,
			DownloadURL: base(r) + "/download/" + entry.Repo + "/" + entry.ID + "/" + entry.Secret,
			MatchedKey:  entry.Key,
		})

	case callCreate:
		if identity.Write == "" {
			// The prefix is the one GitHub's client knows as a refusal it
			// need not warn about.
			writeCall(w, r, which, response{Message: "cache write denied: this job may not save caches"})
			return
		}
		id, err := s.Store.Begin(Entry{
			Pool: pool, Repo: identity.Repo, RepoName: identity.RepoName,
			Scope: identity.Write, Key: body.Key, Version: body.Version,
		})
		if errors.Is(err, ErrExists) {
			// Ordinary: two jobs built the same thing. With no message
			// GitHub's client notes it and moves on, without a warning on
			// the run.
			writeCall(w, r, which, response{})
			return
		}
		if err != nil {
			s.Log.Warn("could not start saving a cache", "repository", repo, "key", body.Key, "error", err)
			writeCall(w, r, which, response{Message: err.Error()})
			return
		}
		writeCall(w, r, which, response{OK: true, UploadURL: base(r) + "/devstoreaccount1/upload/" + id})

	case callFinalize:
		if identity.Write == "" {
			writeCall(w, r, which, response{Message: "cache write denied: this job may not save caches"})
			return
		}
		size := int64(-1)
		if body.hasSize {
			size = body.SizeBytes
		}
		entry, err := s.Store.Finalize(pool, identity.Repo, identity.Write, body.Key, body.Version, size)
		if err != nil {
			s.Log.Warn("could not save a cache", "repository", repo, "key", body.Key, "error", err)
			writeCall(w, r, which, response{Message: err.Error()})
			return
		}
		s.Log.Info("cache saved", "repository", repo, "key", entry.Key, "bytes", entry.Size)
		writeCall(w, r, which, response{OK: true, EntryID: entry.ID})
	}
}

// upload receives a cache's bytes the way GitHub's client sends them to
// blob storage: whole in one request, or as blocks followed by the list
// that orders them. The id in the address is the only credential; it was
// handed to the job that began the save and is useless to any other pool.
func (s *Server) upload(w http.ResponseWriter, r *http.Request, pool string) {
	if pool == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	var err error
	switch comp := r.URL.Query().Get("comp"); comp {
	case "":
		err = s.Store.PutWhole(pool, id, r.Body, r.ContentLength)
	case "block":
		err = s.Store.PutBlock(pool, id, r.URL.Query().Get("blockid"), r.Body, r.ContentLength)
	case "blocklist":
		var ids []string
		if ids, err = blockList(r.Body); err == nil {
			err = s.Store.Commit(pool, id, ids)
		}
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case err == nil:
		// What blob storage answers, as far as its clients look.
		w.Header().Set("x-ms-request-id", id)
		w.Header().Set("x-ms-version", "2021-08-06")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"`+id+`"`)
		w.WriteHeader(http.StatusCreated)
	case errors.Is(err, ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
	case errors.Is(err, ErrTooLarge):
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrNoRoom):
		w.WriteHeader(http.StatusInsufficientStorage)
	default:
		s.Log.Warn("a cache upload failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, pool string) {
	if pool == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	file, entry, err := s.Store.Blob(pool, r.PathValue("repo"), r.PathValue("id"), r.PathValue("secret"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	// Serves ranges and HEAD, which some clients use to fetch in pieces.
	http.ServeContent(w, r, "", entry.Created, file)
}
