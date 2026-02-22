package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	mapascii "github.com/Kivayan/map-ascii"

	"map-ascii-generator/api/internal/ratelimit"
)

const (
	defaultListenAddr     = ":8081"
	defaultMinWidth       = 20
	defaultMaxWidth       = 240
	defaultMaxMargin      = 12
	defaultMinSupersample = 1
	defaultMaxSupersample = 5
	defaultMinCharAspect  = 1.0
	defaultMaxCharAspect  = 3.5
	defaultRateLimit      = 20
	defaultRateWindow     = time.Minute
	defaultMaxBodyBytes   = 64 * 1024
	defaultReadTimeout    = 10 * time.Second
	defaultWriteTimeout   = 30 * time.Second
	defaultIdleTimeout    = 60 * time.Second
)

var allowedColorModes = map[string]struct{}{
	"never":  {},
	"always": {},
}

var allowedColors = map[string]struct{}{
	"":               {},
	"black":          {},
	"red":            {},
	"green":          {},
	"yellow":         {},
	"blue":           {},
	"magenta":        {},
	"cyan":           {},
	"white":          {},
	"bright-black":   {},
	"bright-red":     {},
	"bright-green":   {},
	"bright-yellow":  {},
	"bright-blue":    {},
	"bright-magenta": {},
	"bright-cyan":    {},
	"bright-white":   {},
}

type config struct {
	listenAddr string

	minWidth       int
	maxWidth       int
	maxMargin      int
	minSupersample int
	maxSupersample int
	minCharAspect  float64
	maxCharAspect  float64

	rateLimit    int
	rateWindow   time.Duration
	maxBodyBytes int64
}

type server struct {
	mask    *mapascii.LandMask
	limiter *ratelimit.FixedWindowLimiter
	cfg     config
}

type generateRequest struct {
	Width       int     `json:"width"`
	Supersample int     `json:"supersample"`
	CharAspect  float64 `json:"char_aspect"`
	Margin      int     `json:"margin"`
	Frame       bool    `json:"frame"`
	Marker      struct {
		Enabled    bool    `json:"enabled"`
		Lon        float64 `json:"lon"`
		Lat        float64 `json:"lat"`
		Center     string  `json:"center"`
		Horizontal string  `json:"horizontal"`
		Vertical   string  `json:"vertical"`
		ArmX       int     `json:"arm_x"`
		ArmY       int     `json:"arm_y"`
	} `json:"marker"`
	Color struct {
		Mode        string `json:"mode"`
		MapColor    string `json:"map_color"`
		FrameColor  string `json:"frame_color"`
		MarkerColor string `json:"marker_color"`
	} `json:"color"`
}

type generateResponse struct {
	Plain string `json:"plain"`
	ANSI  string `json:"ansi"`
	Meta  struct {
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		Supersample int     `json:"supersample"`
		CharAspect  float64 `json:"char_aspect"`
		DurationMS  int64   `json:"duration_ms"`
		Bytes       int     `json:"bytes"`
	} `json:"meta"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	cfg := loadConfig()

	mask, err := mapascii.LoadEmbeddedDefaultLandMask()
	if err != nil {
		log.Fatalf("failed to load embedded land mask: %v", err)
	}

	srv := &server{
		mask:    mask,
		limiter: ratelimit.NewFixedWindowLimiter(cfg.rateLimit, cfg.rateWindow),
		cfg:     cfg,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", srv.handleHealth)
	mux.HandleFunc("/api/generate", srv.handleGenerate)

	httpServer := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	log.Printf("api listening on %s", cfg.listenAddr)
	log.Printf("limits: width=%d..%d supersample=%d..%d margin<=%d rate=%d/%s", cfg.minWidth, cfg.maxWidth, cfg.minSupersample, cfg.maxSupersample, cfg.maxMargin, cfg.rateLimit, cfg.rateWindow)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	clientKey := clientIdentifier(r)
	if !s.limiter.Allow(clientKey, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	req, err := decodeGenerateRequest(w, r, s.cfg.maxBodyBytes)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.validateRequest(req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	marker, err := requestMarkerToModel(req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	start := time.Now()

	plain, err := mapascii.RenderWorldASCIIWithOptions(s.mask, req.Width, req.Supersample, req.CharAspect, marker, &mapascii.RenderOptions{
		VerticalMarginRows: req.Margin,
		Frame:              req.Frame,
		ColorMode:          "never",
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("render plain output failed: %v", err))
		return
	}

	ansi := plain
	if req.Color.Mode == "always" {
		ansi, err = mapascii.RenderWorldASCIIWithOptions(s.mask, req.Width, req.Supersample, req.CharAspect, marker, &mapascii.RenderOptions{
			VerticalMarginRows: req.Margin,
			Frame:              req.Frame,
			ColorMode:          req.Color.Mode,
			MapColor:           req.Color.MapColor,
			FrameColor:         req.Color.FrameColor,
			MarkerColor:        req.Color.MarkerColor,
		})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("render ansi output failed: %v", err))
			return
		}
	}

	duration := time.Since(start)
	height := int(math.Round(float64(req.Width) / (2.0 * req.CharAspect)))

	resp := generateResponse{
		Plain: plain,
		ANSI:  ansi,
	}
	resp.Meta.Width = req.Width
	resp.Meta.Height = height
	resp.Meta.Supersample = req.Supersample
	resp.Meta.CharAspect = req.CharAspect
	resp.Meta.DurationMS = duration.Milliseconds()
	resp.Meta.Bytes = len(plain)

	writeJSON(w, http.StatusOK, resp)
}

func (s *server) validateRequest(req generateRequest) error {
	if req.Width < s.cfg.minWidth || req.Width > s.cfg.maxWidth {
		return fmt.Errorf("width must be between %d and %d", s.cfg.minWidth, s.cfg.maxWidth)
	}
	if req.Supersample < s.cfg.minSupersample || req.Supersample > s.cfg.maxSupersample {
		return fmt.Errorf("supersample must be between %d and %d", s.cfg.minSupersample, s.cfg.maxSupersample)
	}
	if req.Margin < 0 || req.Margin > s.cfg.maxMargin {
		return fmt.Errorf("margin must be between 0 and %d", s.cfg.maxMargin)
	}
	if !isFinite(req.CharAspect) || req.CharAspect < s.cfg.minCharAspect || req.CharAspect > s.cfg.maxCharAspect {
		return fmt.Errorf("char_aspect must be between %.1f and %.1f", s.cfg.minCharAspect, s.cfg.maxCharAspect)
	}

	req.Color.Mode = strings.ToLower(strings.TrimSpace(req.Color.Mode))
	if _, ok := allowedColorModes[req.Color.Mode]; !ok {
		return fmt.Errorf("color.mode must be one of: never, always")
	}

	req.Color.MapColor = strings.ToLower(strings.TrimSpace(req.Color.MapColor))
	req.Color.FrameColor = strings.ToLower(strings.TrimSpace(req.Color.FrameColor))
	req.Color.MarkerColor = strings.ToLower(strings.TrimSpace(req.Color.MarkerColor))

	if _, ok := allowedColors[req.Color.MapColor]; !ok {
		return fmt.Errorf("color.map_color is not a supported ANSI 16 color")
	}
	if _, ok := allowedColors[req.Color.FrameColor]; !ok {
		return fmt.Errorf("color.frame_color is not a supported ANSI 16 color")
	}
	if _, ok := allowedColors[req.Color.MarkerColor]; !ok {
		return fmt.Errorf("color.marker_color is not a supported ANSI 16 color")
	}

	if req.Marker.Enabled {
		if !isFinite(req.Marker.Lon) || req.Marker.Lon < -180.0 || req.Marker.Lon > 180.0 {
			return fmt.Errorf("marker.lon must be between -180 and 180")
		}
		if !isFinite(req.Marker.Lat) || req.Marker.Lat < -90.0 || req.Marker.Lat > 90.0 {
			return fmt.Errorf("marker.lat must be between -90 and 90")
		}
		if req.Marker.ArmX < -1 || req.Marker.ArmY < -1 {
			return fmt.Errorf("marker arm lengths must be -1 or greater")
		}

		if _, err := parseASCIIRune(req.Marker.Center, 'O', "marker.center"); err != nil {
			return err
		}
		if _, err := parseASCIIRune(req.Marker.Horizontal, '-', "marker.horizontal"); err != nil {
			return err
		}
		if _, err := parseASCIIRune(req.Marker.Vertical, '|', "marker.vertical"); err != nil {
			return err
		}
	}

	return nil
}

func requestMarkerToModel(req generateRequest) (*mapascii.Marker, error) {
	if !req.Marker.Enabled {
		return nil, nil
	}

	center, err := parseASCIIRune(req.Marker.Center, 'O', "marker.center")
	if err != nil {
		return nil, err
	}
	horizontal, err := parseASCIIRune(req.Marker.Horizontal, '-', "marker.horizontal")
	if err != nil {
		return nil, err
	}
	vertical, err := parseASCIIRune(req.Marker.Vertical, '|', "marker.vertical")
	if err != nil {
		return nil, err
	}

	marker := &mapascii.Marker{
		Lon:        req.Marker.Lon,
		Lat:        req.Marker.Lat,
		Center:     center,
		Horizontal: horizontal,
		Vertical:   vertical,
		ArmX:       req.Marker.ArmX,
		ArmY:       req.Marker.ArmY,
	}

	return marker, nil
}

func decodeGenerateRequest(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) (generateRequest, error) {
	req := defaultGenerateRequest()

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		return generateRequest{}, fmt.Errorf("invalid JSON payload: %w", err)
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return generateRequest{}, fmt.Errorf("invalid JSON payload: trailing data")
	}

	req.Color.Mode = strings.ToLower(strings.TrimSpace(req.Color.Mode))
	req.Color.MapColor = strings.ToLower(strings.TrimSpace(req.Color.MapColor))
	req.Color.FrameColor = strings.ToLower(strings.TrimSpace(req.Color.FrameColor))
	req.Color.MarkerColor = strings.ToLower(strings.TrimSpace(req.Color.MarkerColor))

	return req, nil
}

func defaultGenerateRequest() generateRequest {
	var req generateRequest
	req.Width = 120
	req.Supersample = 3
	req.CharAspect = 2.0
	req.Margin = 2
	req.Frame = true

	req.Marker.Enabled = false
	req.Marker.Center = "O"
	req.Marker.Horizontal = "-"
	req.Marker.Vertical = "|"
	req.Marker.ArmX = -1
	req.Marker.ArmY = -1

	req.Color.Mode = "always"
	req.Color.MapColor = "green"
	req.Color.FrameColor = "bright-white"
	req.Color.MarkerColor = "bright-red"

	return req
}

func parseASCIIRune(value string, fallback rune, fieldName string) (rune, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}

	runes := []rune(value)
	if len(runes) != 1 {
		return 0, fmt.Errorf("%s must be a single ASCII character", fieldName)
	}
	if runes[0] > 127 {
		return 0, fmt.Errorf("%s must be ASCII", fieldName)
	}

	return runes[0], nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(payload); err != nil {
		log.Printf("failed to write JSON response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, errorResponse{Error: message})
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func clientIdentifier(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(ip); parsed != nil {
				return parsed.String()
			}
		}
	}

	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		if parsed := net.ParseIP(xrip); parsed != nil {
			return parsed.String()
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		if parsed := net.ParseIP(host); parsed != nil {
			return parsed.String()
		}
	}

	return "anonymous"
}

func loadConfig() config {
	return config{
		listenAddr:     getEnv("API_LISTEN_ADDR", defaultListenAddr),
		minWidth:       getEnvInt("API_MIN_WIDTH", defaultMinWidth),
		maxWidth:       getEnvInt("API_MAX_WIDTH", defaultMaxWidth),
		maxMargin:      getEnvInt("API_MAX_MARGIN", defaultMaxMargin),
		minSupersample: getEnvInt("API_MIN_SUPERSAMPLE", defaultMinSupersample),
		maxSupersample: getEnvInt("API_MAX_SUPERSAMPLE", defaultMaxSupersample),
		minCharAspect:  getEnvFloat("API_MIN_CHAR_ASPECT", defaultMinCharAspect),
		maxCharAspect:  getEnvFloat("API_MAX_CHAR_ASPECT", defaultMaxCharAspect),
		rateLimit:      getEnvInt("API_RATE_LIMIT", defaultRateLimit),
		rateWindow:     getEnvDuration("API_RATE_WINDOW", defaultRateWindow),
		maxBodyBytes:   int64(getEnvInt("API_MAX_BODY_BYTES", defaultMaxBodyBytes)),
	}
}

func getEnv(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func getEnvInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid integer for %s (%q), using fallback %d", name, value, fallback)
		return fallback
	}

	return parsed
}

func getEnvFloat(name string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("invalid float for %s (%q), using fallback %.2f", name, value, fallback)
		return fallback
	}

	return parsed
}

func getEnvDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("invalid duration for %s (%q), using fallback %s", name, value, fallback)
		return fallback
	}

	return parsed
}
