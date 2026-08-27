package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"roadmap/internal/auth"
	"roadmap/internal/store"
)

// Agent bearer traffic is bounded by an actor-keyed request bucket, while
// mutation traffic also passes through a deliberately conservative mutation
// bucket. Human sessions are not sent through either limiter; bearer requests
// are keyed by the actor which owns the token, so rotating tokens cannot create
// a fresh allowance.
const (
	// Bearer requests share this bounded actor-keyed bucket. The sustained
	// allowance is intentionally generous for normal board polling while still
	// putting a ceiling on authenticated read floods: 20 requests per second
	// with a burst of 40 per actor.
	defaultAgentRequestRate       = 20.0 // requests per second
	defaultAgentRequestBurst      = 40
	defaultAgentRequestMaxActors  = 1024
	defaultAgentRequestIdleWindow = 15 * time.Minute

	defaultAgentMutationRate       = 1.0 // token per second
	defaultAgentMutationBurst      = 10
	defaultAgentMutationMaxActors  = 1024
	defaultAgentMutationIdleWindow = 15 * time.Minute
)

type mutationRateEntry struct {
	tokens float64
	last   time.Time
}

// mutationRateLimiter is a bounded, actor-keyed token bucket. The map is
// protected as requests for one actor can arrive concurrently on many HTTP
// goroutines. Bounded entries are important here: actor IDs are authenticated
// data, but they are still not suitable as an unbounded allocation key.
type mutationRateLimiter struct {
	mu         sync.Mutex
	entries    map[string]mutationRateEntry
	rate       float64
	burst      float64
	maxEntries int
	idleWindow time.Duration
	now        func() time.Time
}

func newDefaultMutationRateLimiter() *mutationRateLimiter {
	return newMutationRateLimiter(defaultAgentMutationRate, defaultAgentMutationBurst, defaultAgentMutationMaxActors, defaultAgentMutationIdleWindow)
}

func newDefaultAgentRequestLimiter() *mutationRateLimiter {
	return newMutationRateLimiter(defaultAgentRequestRate, defaultAgentRequestBurst, defaultAgentRequestMaxActors, defaultAgentRequestIdleWindow)
}

func newDefaultBearerCredentialLimiter() *mutationRateLimiter {
	return newMutationRateLimiter(defaultAgentRequestRate, defaultAgentRequestBurst, defaultAgentRequestMaxActors, defaultAgentRequestIdleWindow)
}

func newMutationRateLimiter(ratePerSecond float64, burst, maxEntries int, idleWindow time.Duration) *mutationRateLimiter {
	if ratePerSecond <= 0 || math.IsNaN(ratePerSecond) || math.IsInf(ratePerSecond, 0) {
		ratePerSecond = defaultAgentMutationRate
	}
	if burst < 1 {
		burst = defaultAgentMutationBurst
	}
	if maxEntries < 1 {
		maxEntries = defaultAgentMutationMaxActors
	}
	if idleWindow <= 0 {
		idleWindow = defaultAgentMutationIdleWindow
	}
	return &mutationRateLimiter{
		entries:    make(map[string]mutationRateEntry),
		rate:       ratePerSecond,
		burst:      float64(burst),
		maxEntries: maxEntries,
		idleWindow: idleWindow,
		now:        time.Now,
	}
}

// allow reserves one request from actorID's bucket. retryAfter is non-zero
// only when the request is rejected and is safe to expose as Retry-After.
func (l *mutationRateLimiter) allow(actorID string) (allowed bool, retryAfter time.Duration) {
	if l == nil {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[actorID]
	if !ok {
		l.evictIdleOrOldest(now)
		entry = mutationRateEntry{tokens: l.burst, last: now}
	}
	if elapsed := now.Sub(entry.last); elapsed > 0 {
		entry.tokens = math.Min(l.burst, entry.tokens+elapsed.Seconds()*l.rate)
		entry.last = now
	} else if entry.last.IsZero() {
		entry.last = now
	}
	if entry.tokens >= 1 {
		entry.tokens--
		l.entries[actorID] = entry
		return true, 0
	}
	// Keep the last-seen timestamp fresh even for rejected requests so an
	// actively abusive actor cannot be evicted and immediately reset.
	l.entries[actorID] = entry
	seconds := (1 - entry.tokens) / l.rate
	if seconds < 1.0 {
		seconds = 1.0
	}
	return false, time.Duration(math.Ceil(seconds)) * time.Second
}

func (l *mutationRateLimiter) evictIdleOrOldest(now time.Time) {
	if len(l.entries) < l.maxEntries {
		return
	}
	oldestKey := ""
	var oldest time.Time
	for key, entry := range l.entries {
		if now.Sub(entry.last) >= l.idleWindow {
			delete(l.entries, key)
			if len(l.entries) < l.maxEntries {
				return
			}
			continue
		}
		if oldestKey == "" || entry.last.Before(oldest) {
			oldestKey, oldest = key, entry.last
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func mutationIdentityIsLimited(identity auth.Identity) bool {
	return identity.IsToken || identity.Actor.Kind != "human"
}

func (s *Server) getMutationLimiter() *mutationRateLimiter {
	s.mutationLimiterOnce.Do(func() {
		if s.mutationLimiter == nil {
			s.mutationLimiter = newDefaultMutationRateLimiter()
		}
	})
	return s.mutationLimiter
}

func (s *Server) getAgentRequestLimiter() *mutationRateLimiter {
	s.agentRequestLimiterOnce.Do(func() {
		if s.agentRequestLimiter == nil {
			s.agentRequestLimiter = newDefaultAgentRequestLimiter()
		}
	})
	return s.agentRequestLimiter
}

func (s *Server) getBearerCredentialLimiter() *mutationRateLimiter {
	s.bearerCredentialLimiterOnce.Do(func() {
		if s.bearerCredentialLimiter == nil {
			s.bearerCredentialLimiter = newDefaultBearerCredentialLimiter()
		}
	})
	return s.bearerCredentialLimiter
}

func (s *Server) admitBearerCredential(w http.ResponseWriter, r *http.Request) bool {
	key, ok := bearerCredentialKey(r)
	if !ok {
		return true
	}
	limiter := s.getBearerCredentialLimiter()
	if allowed, retryAfter := limiter.allow(key); !allowed {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many authentication attempts; retry later", map[string]any{
			"retry_after_seconds": seconds,
		})
		return false
	}
	return true
}

func (s *Server) admitBearerRequest(w http.ResponseWriter, r *http.Request, identity auth.Identity) bool {
	if !identity.IsToken {
		return true
	}
	limiter := s.getAgentRequestLimiter()
	if allowed, retryAfter := limiter.allow(identity.Actor.ID); !allowed {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many agent requests; retry later", map[string]any{
			"retry_after_seconds": seconds,
		})
		return false
	}
	return true
}

// admitMutation runs immediately before a new mutation handler execution. It
// reserves the persistent actor budget only after the short-lived mutation
// rate check succeeds, so rejected bursts do not burn the lifetime allowance.
func (s *Server) admitMutation(w http.ResponseWriter, r *http.Request, identity auth.Identity) bool {
	if !isMutationMethod(r.Method) || !mutationIdentityIsLimited(identity) {
		return true
	}
	limiter := s.getMutationLimiter()
	if allowed, retryAfter := limiter.allow(identity.Actor.ID); !allowed {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		s.writeError(w, http.StatusTooManyRequests, "rate_limited", "too many agent mutations; retry later", map[string]any{
			"retry_after_seconds": seconds,
		})
		return false
	}
	if err := s.Store.ReserveAgentMutation(r.Context(), identity.Actor.ID, len(bodyBytes(r))); err != nil {
		if errors.Is(err, store.ErrResourceLimit) {
			s.writeError(w, http.StatusInsufficientStorage, "resource_limit", "agent mutation resource budget exhausted", map[string]any{
				"operator_reset": "ResetAgentMutationUsage",
			})
			return false
		}
		s.writeInternal(w, err)
		return false
	}
	return true
}
