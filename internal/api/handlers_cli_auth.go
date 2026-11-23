package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dvorinka/kern/internal/auth"
	"github.com/dvorinka/kern/internal/store"
)

const (
	cliAuthTTL        = 10 * time.Minute
	cliAuthMaxPending = 512
	// Unambiguous uppercase letters and digits (no 0/O/1/I/L) for human-typed verification codes.
	userCodeLetters = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	userCodeDigits  = "23456789"
)

type cliAuthEntry struct {
	DeviceCode string
	UserCode   string
	Name       string
	Hostname   string
	Days       int
	Status     string // pending | approved | denied
	Token      string
	Record     store.APIToken
	User       store.User
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type cliAuthStore struct {
	mu       sync.Mutex
	byDevice map[string]*cliAuthEntry
	byUser   map[string]*cliAuthEntry
}

func newCLIAuthStore() *cliAuthStore {
	return &cliAuthStore{
		byDevice: make(map[string]*cliAuthEntry),
		byUser:   make(map[string]*cliAuthEntry),
	}
}

func (cs *cliAuthStore) pruneLocked(now time.Time) {
	for dc, e := range cs.byDevice {
		if now.After(e.ExpiresAt) {
			delete(cs.byDevice, dc)
			delete(cs.byUser, e.UserCode)
		}
	}
}

func randomDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomUserCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	chars := userCodeLetters + userCodeDigits
	var out strings.Builder
	for i, v := range b {
		if i == 4 {
			out.WriteByte('-')
		}
		out.WriteByte(chars[int(v)%len(chars)])
	}
	return out.String(), nil
}

// normalizeUserCode turns "abcd1234", "abcd-1234", or " ABCD 1234 " into "ABCD-1234".
func normalizeUserCode(raw string) string {
	var clean strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	s := clean.String()
	if len(s) == 8 {
		return s[:4] + "-" + s[4:]
	}
	return s
}

func (cs *cliAuthStore) start(name, hostname string, days int) (*cliAuthEntry, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	now := time.Now()
	cs.pruneLocked(now)
	if len(cs.byDevice) >= cliAuthMaxPending {
		return nil, fmt.Errorf("too many pending CLI sign-ins; try again shortly")
	}
	dc, err := randomDeviceCode()
	if err != nil {
		return nil, err
	}
	var uc string
	for range 8 {
		uc, err = randomUserCode()
		if err != nil {
			return nil, err
		}
		if _, exists := cs.byUser[uc]; !exists {
			break
		}
	}
	e := &cliAuthEntry{
		DeviceCode: dc,
		UserCode:   uc,
		Name:       name,
		Hostname:   hostname,
		Days:       days,
		Status:     "pending",
		CreatedAt:  now,
		ExpiresAt:  now.Add(cliAuthTTL),
	}
	cs.byDevice[dc] = e
	cs.byUser[uc] = e
	return e, nil
}

func (cs *cliAuthStore) lookup(userCode string) (*cliAuthEntry, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.pruneLocked(time.Now())
	e, ok := cs.byUser[normalizeUserCode(userCode)]
	if !ok {
		return nil, false
	}
	cp := *e
	cp.DeviceCode = ""
	cp.Token = ""
	return &cp, true
}

func (cs *cliAuthStore) approve(userCode, token string, rec store.APIToken, user store.User) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.pruneLocked(time.Now())
	e, ok := cs.byUser[normalizeUserCode(userCode)]
	if !ok || e.Status != "pending" {
		return false
	}
	e.Status = "approved"
	e.Token = token
	e.Record = rec
	e.User = user
	return true
}

func (cs *cliAuthStore) deny(userCode string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.pruneLocked(time.Now())
	e, ok := cs.byUser[normalizeUserCode(userCode)]
	if !ok || e.Status != "pending" {
		return false
	}
	e.Status = "denied"
	return true
}

func (cs *cliAuthStore) poll(deviceCode string) (*cliAuthEntry, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.pruneLocked(time.Now())
	e, ok := cs.byDevice[deviceCode]
	if !ok {
		return nil, false
	}
	cp := *e
	// Consume terminal states immediately so a plaintext token can never be read twice.
	if e.Status == "approved" || e.Status == "denied" {
		delete(cs.byDevice, deviceCode)
		delete(cs.byUser, e.UserCode)
	}
	return &cp, true
}

// cliAuthStart initiates a browser-backed CLI authorization session.
func (s *Server) cliAuthStart(c *gin.Context) {
	key := "cli-start|" + c.ClientIP()
	if !s.limiter.allow(key) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many CLI login attempts; try again later")
		return
	}
	var req struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		Days     int    `json:"days"`
	}
	_ = c.ShouldBindJSON(&req)
	hostname := strings.TrimSpace(req.Hostname)
	if len(hostname) > 120 {
		hostname = hostname[:120]
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if hostname != "" {
			name = "kern-cli (" + hostname + ")"
		} else {
			name = "kern-cli"
		}
	}
	if len(name) > 120 {
		name = name[:120]
	}
	days := req.Days
	if days < 0 {
		days = 0
	}
	if days > 3650 {
		days = 3650
	}
	entry, err := s.cliAuth.start(name, hostname, days)
	if err != nil {
		fail(c, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	}
	base := requestBaseURL(c, s.Cfg.PublicBaseURL)
	c.JSON(http.StatusCreated, gin.H{
		"device_code":      entry.DeviceCode,
		"user_code":        entry.UserCode,
		"verification_url": base + "/auth/cli?code=" + entry.UserCode,
		"expires_in":       int(cliAuthTTL.Seconds()),
		"interval":         2,
	})
}

// cliAuthPoll checks whether a browser user has approved or denied a pending CLI sign-in.
func (s *Server) cliAuthPoll(c *gin.Context) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.DeviceCode) == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "device_code is required")
		return
	}
	entry, ok := s.cliAuth.poll(strings.TrimSpace(req.DeviceCode))
	if !ok {
		fail(c, http.StatusGone, "expired", "CLI login session expired or was already used")
		return
	}
	switch entry.Status {
	case "pending":
		c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
	case "denied":
		fail(c, http.StatusForbidden, "denied", "CLI login request was denied in the browser")
	case "approved":
		c.JSON(http.StatusOK, gin.H{
			"status": "approved",
			"token":  entry.Token,
			"record": entry.Record,
			"user": gin.H{
				"id":     entry.User.ID,
				"email":  entry.User.Email,
				"name":   entry.User.Name,
				"role":   entry.User.Role,
				"org_id": entry.User.OrgID,
			},
		})
	default:
		fail(c, http.StatusGone, "expired", "CLI login session is no longer valid")
	}
}

// cliAuthLookup lets the signed-in web UI inspect a pending CLI code before approval.
func (s *Server) cliAuthLookup(c *gin.Context) {
	code := normalizeUserCode(c.Query("code"))
	if len(code) < 8 {
		fail(c, http.StatusBadRequest, "invalid_code", "verification code is required")
		return
	}
	entry, ok := s.cliAuth.lookup(code)
	if !ok {
		fail(c, http.StatusNotFound, "not_found", "verification code not found or expired")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user_code":  entry.UserCode,
		"name":       entry.Name,
		"hostname":   entry.Hostname,
		"days":       entry.Days,
		"status":     entry.Status,
		"expires_at": entry.ExpiresAt,
	})
}

// cliAuthApprove approves or denies a pending CLI sign-in from the web UI.
func (s *Server) cliAuthApprove(c *gin.Context) {
	// Only an interactive web session may authorize a browser login — prevents a
	// restricted API token from escalating into a full-scope token.
	if c.GetString(ctxAuthKind) != "session" {
		fail(c, http.StatusForbidden, "session_required", "CLI login approval requires a browser session")
		return
	}
	user, _ := currentUser(c)
	var req struct {
		UserCode string `json:"user_code"`
		Approve  *bool  `json:"approve"`
		Name     string `json:"name"`
		Days     *int   `json:"days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.UserCode) == "" {
		fail(c, http.StatusBadRequest, "invalid_body", "user_code is required")
		return
	}
	code := normalizeUserCode(req.UserCode)
	entry, ok := s.cliAuth.lookup(code)
	if !ok {
		fail(c, http.StatusNotFound, "not_found", "verification code not found or expired")
		return
	}
	if entry.Status != "pending" {
		fail(c, http.StatusConflict, "already_resolved", "verification code was already "+entry.Status)
		return
	}
	if req.Approve != nil && !*req.Approve {
		if !s.cliAuth.deny(code) {
			fail(c, http.StatusConflict, "already_resolved", "verification code was already resolved")
			return
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "cli_auth.deny", entry.Name)
		c.JSON(http.StatusOK, gin.H{"status": "denied"})
		return
	}

	tokenName := strings.TrimSpace(req.Name)
	if tokenName == "" {
		tokenName = entry.Name
	}
	if tokenName == "" {
		tokenName = "kern-cli"
	}
	days := entry.Days
	if req.Days != nil {
		days = *req.Days
	}
	var expires *time.Time
	if days > 0 {
		t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		expires = &t
	}
	token, hash, err := auth.NewToken(auth.APIPrefix)
	if err != nil {
		s.internalError(c, err)
		return
	}
	rec, err := s.Store.CreateAPIToken(c.Request.Context(), user.ID, tokenName,
		auth.TokenPrefix(token), hash, []string{"read", "write", "admin"}, expires, nil, nil, 0)
	if err != nil {
		s.internalError(c, err)
		return
	}
	if !s.cliAuth.approve(code, token, rec, user) {
		_ = s.Store.DeleteAPIToken(c.Request.Context(), user.ID, rec.ID)
		fail(c, http.StatusConflict, "already_resolved", "verification code expired or was already resolved")
		return
	}
	s.Store.AuditMeta(c.Request.Context(), user.OrgID, &user.ID, "token.create", tokenName, map[string]any{
		"via": "cli_browser", "hostname": entry.Hostname, "days": days,
	})
	c.JSON(http.StatusOK, gin.H{
		"status":       "approved",
		"token_prefix": rec.Prefix,
		"name":         rec.Name,
	})
}
