package transcript

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// ErrTooLarge is returned for a single export above the store's limit.
var ErrTooLarge = errors.New("export too large")

// File is one rendered export.
type File struct {
	Name        string
	ContentType string
	Data        []byte
	Created     time.Time
}

type entry struct {
	file    File
	expires time.Time
}

// Store keeps rendered exports in memory behind unguessable tokens until
// they expire. The total size is bounded: the oldest exports are dropped to
// make room for a new one.
type Store struct {
	ttl      time.Duration
	maxFile  int
	maxTotal int
	now      func() time.Time

	mu    sync.Mutex
	files map[string]entry
	total int
}

func NewStore(ttl time.Duration, maxFile, maxTotal int) *Store {
	return &Store{ttl: ttl, maxFile: maxFile, maxTotal: maxTotal, now: time.Now, files: make(map[string]entry)}
}

// Put stores a file and returns its token (256 bits) and expiry.
func (s *Store) Put(file File) (string, time.Time, error) {
	if len(file.Data) > s.maxFile {
		return "", time.Time{}, ErrTooLarge
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	for s.total+len(file.Data) > s.maxTotal && len(s.files) > 0 {
		s.dropOldestLocked()
	}
	file.Created = now
	expires := now.Add(s.ttl)
	s.files[token] = entry{file: file, expires: expires}
	s.total += len(file.Data)
	return token, expires, nil
}

// Get returns a file whose link has not expired. Links stay valid until they
// expire, so a download can be retried.
func (s *Store) Get(token string) (File, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.files[token]
	if !ok || !s.now().Before(e.expires) {
		return File{}, false
	}
	return e.file, true
}

func (s *Store) pruneLocked(now time.Time) {
	for token, e := range s.files {
		if !now.Before(e.expires) {
			s.total -= len(e.file.Data)
			delete(s.files, token)
		}
	}
}

func (s *Store) dropOldestLocked() {
	oldest := ""
	var oldestAt time.Time
	for token, e := range s.files {
		if oldest == "" || e.file.Created.Before(oldestAt) {
			oldest, oldestAt = token, e.file.Created
		}
	}
	s.total -= len(s.files[oldest].file.Data)
	delete(s.files, oldest)
}
