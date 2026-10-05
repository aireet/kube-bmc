package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HtpasswdKey is the Secret key that holds the user list.
const HtpasswdKey = "htpasswd"

// ErrTooManyAttempts is returned while a username or client is locked out after
// repeated failed sign-ins.
var ErrTooManyAttempts = errors.New("too many failed sign-in attempts; try again later")

// Passwords verifies username and password pairs against an htpasswd file stored in a
// Secret. Only bcrypt hashes are accepted. The Secret is read on every sign-in, so users
// can be added or removed without restarting the server.
type Passwords struct {
	Reader    client.Reader
	Namespace string
	Secret    string

	limiter limiter
}

// hashCost is the bcrypt cost of new password hashes.
var hashCost = 12

// dummyHash is compared for unknown users so that response times do not reveal
// which usernames exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("kube-bmc"), bcrypt.DefaultCost)

// Verify returns the identity for valid credentials. client identifies the caller,
// typically its IP address, for rate limiting.
func (p *Passwords) Verify(ctx context.Context, username, password, client string) (Identity, error) {
	keys := []string{"user\x00" + username, "client\x00" + client}
	if err := p.limiter.check(keys...); err != nil {
		return Identity{}, err
	}
	users, err := p.users(ctx)
	if err != nil {
		return Identity{}, err
	}
	hash, ok := users[username]
	if !ok {
		hash = dummyHash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !ok {
		p.limiter.fail(keys...)
		return Identity{}, fmt.Errorf("%w: invalid username or password", ErrUnauthenticated)
	}
	p.limiter.reset(keys...)
	return Identity{Username: username, Method: MethodPassword}, nil
}

func (p *Passwords) users(ctx context.Context) (map[string][]byte, error) {
	s := &corev1.Secret{}
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: p.Secret}, s); err != nil {
		return nil, fmt.Errorf("users secret %s/%s: %w", p.Namespace, p.Secret, err)
	}
	return ParseHtpasswd(string(s.Data[HtpasswdKey]))
}

// ParseHtpasswd parses `user:bcrypt-hash` lines. Blank lines and lines starting with #
// are ignored.
func ParseHtpasswd(content string) (map[string][]byte, error) {
	users := map[string][]byte{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		user, hash, ok := strings.Cut(line, ":")
		if !ok || user == "" {
			return nil, fmt.Errorf("htpasswd line %d: expected user:hash", n)
		}
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return nil, fmt.Errorf("htpasswd line %d: user %s: only bcrypt hashes are supported", n, user)
		}
		users[user] = []byte(hash)
	}
	return users, sc.Err()
}

// HashPassword returns an htpasswd line for user.
func HashPassword(user, password string) (string, error) {
	if strings.ContainsAny(user, ":\n") || user == "" {
		return "", errors.New("username must not be empty or contain ':'")
	}
	if len(password) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), hashCost)
	if err != nil {
		return "", err
	}
	return user + ":" + string(h), nil
}

// limiter locks out a username or client after repeated failures. The lockout doubles
// with every further failure, up to maxLockout.
type limiter struct {
	mu       sync.Mutex
	failures map[string]*failure
	now      func() time.Time
}

type failure struct {
	count int
	until time.Time
}

const (
	freeAttempts = 5
	baseLockout  = 30 * time.Second
	maxLockout   = 15 * time.Minute
)

func (l *limiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *limiter) check(keys ...string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	for _, k := range keys {
		if f := l.failures[k]; f != nil && now.Before(f.until) {
			return ErrTooManyAttempts
		}
	}
	return nil
}

func (l *limiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failures == nil {
		l.failures = map[string]*failure{}
	}
	if len(l.failures) > 10000 {
		clear(l.failures)
	}
	now := l.clock()
	for _, k := range keys {
		f := l.failures[k]
		if f == nil {
			f = &failure{}
			l.failures[k] = f
		}
		f.count++
		if f.count >= freeAttempts {
			lockout := baseLockout << min(f.count-freeAttempts, 10)
			f.until = now.Add(min(lockout, maxLockout))
		}
	}
}

func (l *limiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.failures, k)
	}
}
