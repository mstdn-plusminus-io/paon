package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
)

const (
	donationCampaignBodyLimit        = 1 << 20
	donationCampaignCacheTTL         = time.Hour
	donationCampaignStoplightTTL     = time.Minute
	donationCampaignFailureThreshold = 10
)

func (s *Server) donationCampaigns(c *echo.Context) error {
	account, _, err := s.requireAccount(c)
	if err != nil {
		return err
	}
	endpoint := strings.TrimSpace(s.cfg.DonationCampaignsURL)
	if endpoint == "" {
		return c.NoContent(http.StatusNoContent)
	}
	locale := s.webLocale(c, &account.User)
	seed := rubyRandomPercent(account.ID)
	requestKey := "donation_campaign_request:" + strconv.Itoa(seed) + ":" + locale
	if cached := s.cachedDonationCampaign(c.Request().Context(), requestKey); len(cached) != 0 {
		return c.Blob(http.StatusOK, "application/json; charset=utf-8", cached)
	}
	if s.donationCampaignStoplightOpen(c.Request().Context()) {
		return c.NoContent(http.StatusNoContent)
	}
	requestURL, err := donationCampaignRequestURL(endpoint, seed, locale, s.cfg.DonationCampaignsEnvironment)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		return c.NoContent(http.StatusNoContent)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", paonUserAgent(s.cfg))
	client := activityHTTPClientClone(10 * time.Second)
	response, err := client.Do(req)
	if err != nil {
		s.trackDonationCampaignFailure(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return c.NoContent(http.StatusNoContent)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, donationCampaignBodyLimit+1))
	if err != nil || len(body) > donationCampaignBodyLimit || !json.Valid(body) {
		s.trackDonationCampaignFailure(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	}
	s.clearDonationCampaignFailures(c.Request().Context())
	s.cacheDonationCampaign(c.Request().Context(), requestKey, body)
	return c.Blob(http.StatusOK, "application/json; charset=utf-8", body)
}

func donationCampaignRequestURL(endpoint string, seed int, locale string, environment string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("invalid donation campaigns URL")
	}
	query := parsed.Query()
	query.Set("platform", "web")
	query.Set("seed", strconv.Itoa(seed))
	query.Set("locale", locale)
	if strings.TrimSpace(environment) != "" {
		query.Set("environment", strings.TrimSpace(environment))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *Server) cachedDonationCampaign(ctx context.Context, requestKey string) []byte {
	campaignValue, err := s.redisCommand(ctx, "GET", requestKey)
	campaignKey, ok := redisStringValue(campaignValue)
	if err != nil || !ok || strings.TrimSpace(campaignKey) == "" {
		return nil
	}
	rawValue, err := s.redisCommand(ctx, "GET", "donation_campaign:"+campaignKey)
	raw, ok := redisStringValue(rawValue)
	if err != nil || !ok || !json.Valid([]byte(raw)) {
		return nil
	}
	return []byte(raw)
}

func (s *Server) cacheDonationCampaign(ctx context.Context, requestKey string, body []byte) {
	var campaign map[string]any
	if json.Unmarshal(body, &campaign) != nil {
		return
	}
	id := strings.TrimSpace(fmt.Sprint(campaign["id"]))
	locale := strings.TrimSpace(fmt.Sprint(campaign["locale"]))
	if id == "" || id == "<nil>" {
		return
	}
	if locale == "<nil>" {
		locale = ""
	}
	key := id + ":" + locale
	seconds := strconv.FormatInt(int64(donationCampaignCacheTTL/time.Second), 10)
	_, _ = s.redisCommand(ctx, "SETEX", requestKey, seconds, key)
	_, _ = s.redisCommand(ctx, "SETEX", "donation_campaign:"+key, seconds, string(body))
}

func (s *Server) donationCampaignStoplightOpen(ctx context.Context) bool {
	value, err := s.redisCommand(ctx, "GET", redisConfig(s.cfg).prefix+"donation_campaigns:stoplight")
	return err == nil && redisInt(value) >= donationCampaignFailureThreshold
}

func (s *Server) trackDonationCampaignFailure(ctx context.Context) {
	key := redisConfig(s.cfg).prefix + "donation_campaigns:stoplight"
	if _, err := s.redisCommand(ctx, "INCR", key); err == nil {
		_, _ = s.redisCommand(ctx, "EXPIRE", key, strconv.FormatInt(int64(donationCampaignStoplightTTL/time.Second), 10))
	}
}

func (s *Server) clearDonationCampaignFailures(ctx context.Context) {
	_, _ = s.redisCommand(ctx, "DEL", redisConfig(s.cfg).prefix+"donation_campaigns:stoplight")
}

type mt19937 struct {
	state [624]uint32
	index int
}

func newRubyMT(seed uint64) *mt19937 {
	rng := &mt19937{}
	if seed <= uint64(^uint32(0)) {
		rng.state[0] = uint32(seed)
		for i := 1; i < len(rng.state); i++ {
			rng.state[i] = 1812433253*(rng.state[i-1]^(rng.state[i-1]>>30)) + uint32(i)
		}
		rng.index = len(rng.state)
		return rng
	}
	keys := []uint32{uint32(seed), uint32(seed >> 32)}
	rng.state[0] = 19650218
	for i := 1; i < len(rng.state); i++ {
		rng.state[i] = 1812433253*(rng.state[i-1]^(rng.state[i-1]>>30)) + uint32(i)
	}
	i, j := 1, 0
	for count := len(rng.state); count > 0; count-- {
		rng.state[i] = (rng.state[i] ^ ((rng.state[i-1] ^ (rng.state[i-1] >> 30)) * 1664525)) + keys[j] + uint32(j)
		i++
		j++
		if i >= len(rng.state) {
			rng.state[0] = rng.state[len(rng.state)-1]
			i = 1
		}
		if j >= len(keys) {
			j = 0
		}
	}
	for count := len(rng.state) - 1; count > 0; count-- {
		rng.state[i] = (rng.state[i] ^ ((rng.state[i-1] ^ (rng.state[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= len(rng.state) {
			rng.state[0] = rng.state[len(rng.state)-1]
			i = 1
		}
	}
	rng.state[0] = 0x80000000
	rng.index = len(rng.state)
	return rng
}

func (rng *mt19937) uint32() uint32 {
	if rng.index >= len(rng.state) {
		for i := range rng.state {
			y := (rng.state[i] & 0x80000000) | (rng.state[(i+1)%624] & 0x7fffffff)
			rng.state[i] = rng.state[(i+397)%624] ^ (y >> 1)
			if y&1 != 0 {
				rng.state[i] ^= 0x9908b0df
			}
		}
		rng.index = 0
	}
	y := rng.state[rng.index]
	rng.index++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

func rubyRandomPercent(accountID int64) int {
	rng := newRubyMT(uint64(accountID))
	for {
		value := int(rng.uint32() & 127)
		if value < 100 {
			return value
		}
	}
}
