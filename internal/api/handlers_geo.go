package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/dvorinka/kern/internal/geoip"
	"github.com/dvorinka/kern/internal/store"
)

// clientIP is the address the control plane sees for a request: Cloudflare's
// connecting-IP header behind the tunnel, the first X-Forwarded-For hop, then
// the peer address. It locates the runner's egress, not the runner itself.
func clientIP(c *gin.Context) string {
	if ip := c.GetHeader("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// geoForRunner decides whether a heartbeat/registration should trigger a
// fresh egress-IP lookup: manual overrides are never touched, and an address
// that already produced a location is not re-resolved.
func geoForRunner(r store.Runner, ip string) bool {
	if r.GeoSource == "manual" || !geoip.IsPublic(ip) {
		return false
	}
	return r.GeoIP != ip
}

// resolveRunnerGeo performs the external lookup off the request path; a
// failed lookup leaves the runner unlocated and is debug-logged only.
func (s *Server) resolveRunnerGeo(id uuid.UUID, ip string) {
	if s.Geo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, err := s.Geo.Lookup(ctx, ip)
	if err != nil {
		s.Log.Debug("runner geo lookup failed", zap.String("ip", ip), zap.Error(err))
		return
	}
	if err := s.Store.SetRunnerGeoAuto(ctx, id, ip, g.Lat, g.Lng, g.City, g.Country); err != nil {
		s.Log.Warn("runner geo store", zap.String("runner_id", id.String()), zap.Error(err))
	}
}

type runnerGeoRequest struct {
	Auto    *bool    `json:"auto"`
	Lat     *float64 `json:"lat"`
	Lng     *float64 `json:"lng"`
	City    string   `json:"city"`
	Country string   `json:"country"`
}

func validLatLng(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// patchRunnerGeo places a runner manually ({lat,lng,city,country}) or flips it
// back to auto-detection ({auto:true}), which re-resolves the last seen egress
// IP immediately and lets heartbeats take over after that.
func (s *Server) patchRunnerGeo(c *gin.Context) {
	user, _ := currentUser(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid_id", "runner id must be a uuid")
		return
	}
	var req runnerGeoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "geo body must carry lat/lng or auto:true")
		return
	}
	if req.Auto != nil && *req.Auto {
		ip, err := s.Store.MarkRunnerGeoAuto(c.Request.Context(), user.OrgID, id)
		if err != nil {
			s.internalError(c, err)
			return
		}
		if ip != "" {
			go s.resolveRunnerGeo(id, ip)
		}
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.geo_auto", id.String())
		c.JSON(http.StatusOK, gin.H{"ok": true, "geo_source": "auto"})
		return
	}
	if req.Lat == nil || req.Lng == nil || !validLatLng(*req.Lat, *req.Lng) {
		fail(c, http.StatusBadRequest, "invalid_body", "lat must be -90..90 and lng -180..180")
		return
	}
	if err := s.Store.SetRunnerGeoManual(c.Request.Context(), user.OrgID, id,
		*req.Lat, *req.Lng, req.City, req.Country); err != nil {
		s.internalError(c, err)
		return
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "runner.geo", id.String())
	c.JSON(http.StatusOK, gin.H{"ok": true, "geo_source": "manual"})
}

// controlGeo reads the control-plane location stored as org settings.
func controlGeo(raw map[string]string) (geoip.Geo, string, bool) {
	lat, err1 := strconv.ParseFloat(raw["map.lat"], 64)
	lng, err2 := strconv.ParseFloat(raw["map.lng"], 64)
	if err1 != nil || err2 != nil {
		return geoip.Geo{}, "", false
	}
	return geoip.Geo{
		Lat: lat, Lng: lng,
		City: raw["map.city"], Country: raw["map.country"],
	}, raw["map.source"], true
}

// resolveControlGeo finds the control plane's own egress location once per
// restart when the operator has not placed it. Writes land in org settings so
// the next read is a local one.
func (s *Server) resolveControlGeo(orgID uuid.UUID) {
	if s.Geo == nil || !s.geoSelfMu.TryLock() {
		return
	}
	defer s.geoSelfMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, err := s.Geo.Lookup(ctx, "")
	if err != nil {
		s.Log.Debug("control-plane geo lookup failed", zap.Error(err))
		return
	}
	writes := map[string]string{
		"map.lat":     strconv.FormatFloat(g.Lat, 'f', 4, 64),
		"map.lng":     strconv.FormatFloat(g.Lng, 'f', 4, 64),
		"map.city":    g.City,
		"map.country": g.Country,
		"map.source":  "auto",
	}
	for k, v := range writes {
		if err := s.Store.PutOrgSetting(ctx, orgID, k, v); err != nil {
			s.Log.Warn("control geo store", zap.String("key", k), zap.Error(err))
		}
	}
}

// fleetMap feeds the dashboard world map: runner locations plus one arc
// (control plane -> runner) per in-flight job.
func (s *Server) fleetMap(c *gin.Context) {
	user, _ := currentUser(c)
	runners, err := s.Store.Runners(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	live, err := s.Store.InFlightJobs(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	byRunner := map[uuid.UUID][]store.LiveJob{}
	for _, j := range live {
		byRunner[j.RunnerID] = append(byRunner[j.RunnerID], j)
	}
	type runnerPin struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Status    string          `json:"status"`
		Lat       *float64        `json:"lat"`
		Lng       *float64        `json:"lng"`
		City      string          `json:"city"`
		Country   string          `json:"country"`
		GeoSource string          `json:"geo_source"`
		Jobs      []store.LiveJob `json:"jobs"`
	}
	pins := make([]runnerPin, 0, len(runners))
	for _, r := range runners {
		jobs := byRunner[r.ID]
		if jobs == nil {
			jobs = []store.LiveJob{}
		}
		pins = append(pins, runnerPin{
			ID: r.ID.String(), Name: r.Name, Status: r.Status,
			Lat: r.GeoLat, Lng: r.GeoLng, City: r.GeoCity, Country: r.GeoCountry,
			GeoSource: r.GeoSource, Jobs: jobs,
		})
	}
	raw, err := s.Store.OrgSettings(c.Request.Context(), user.OrgID)
	if err != nil {
		s.internalError(c, err)
		return
	}
	var settings map[string]string
	_ = json.Unmarshal(raw, &settings)
	resp := gin.H{"runners": pins, "control": nil}
	if g, source, ok := controlGeo(settings); ok {
		resp["control"] = gin.H{
			"lat": g.Lat, "lng": g.Lng, "city": g.City,
			"country": g.Country, "source": source,
		}
	} else {
		go s.resolveControlGeo(user.OrgID)
	}
	c.JSON(http.StatusOK, resp)
}

// patchControlGeo places the control-plane marker manually or re-runs the
// egress auto-detection ({auto:true}).
func (s *Server) patchControlGeo(c *gin.Context) {
	user, _ := currentUser(c)
	var req runnerGeoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_body", "geo body must carry lat/lng or auto:true")
		return
	}
	if req.Auto != nil && *req.Auto {
		for _, k := range []string{"map.lat", "map.lng", "map.city", "map.country", "map.source"} {
			if err := s.Store.PutOrgSetting(c.Request.Context(), user.OrgID, k, ""); err != nil {
				s.internalError(c, err)
				return
			}
		}
		go s.resolveControlGeo(user.OrgID)
		s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "settings.map_geo", "auto")
		c.JSON(http.StatusOK, gin.H{"ok": true, "source": "auto"})
		return
	}
	if req.Lat == nil || req.Lng == nil || !validLatLng(*req.Lat, *req.Lng) {
		fail(c, http.StatusBadRequest, "invalid_body", "lat must be -90..90 and lng -180..180")
		return
	}
	writes := map[string]string{
		"map.lat":     strconv.FormatFloat(*req.Lat, 'f', 4, 64),
		"map.lng":     strconv.FormatFloat(*req.Lng, 'f', 4, 64),
		"map.city":    req.City,
		"map.country": req.Country,
		"map.source":  "manual",
	}
	for k, v := range writes {
		if err := s.Store.PutOrgSetting(c.Request.Context(), user.OrgID, k, v); err != nil {
			s.internalError(c, err)
			return
		}
	}
	s.Store.Audit(c.Request.Context(), user.OrgID, &user.ID, "settings.map_geo", "manual")
	c.JSON(http.StatusOK, gin.H{"ok": true, "source": "manual"})
}
