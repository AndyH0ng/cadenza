package metadata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	webBundleRE = regexp.MustCompile(`src="(/assets/index[^"]*\.js)"`)
	jwtRE       = regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+`)
)

// tokenMargin renews the token this long before it expires.
const tokenMargin = time.Hour

// developerToken returns the Apple Music developer token that the Classical
// web player embeds in its JavaScript bundle, cached until shortly before it
// expires. It is unofficial: a change to the web player breaks it.
func (c *Client) developerToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	token, exp := c.token, c.tokenExp
	c.mu.Unlock()
	if token != "" && time.Until(exp) > tokenMargin {
		return token, nil
	}

	page, err := c.get(ctx, c.WebBase+"/"+c.Storefront, nil)
	if err != nil {
		return "", fmt.Errorf("metadata: developer token: %w", err)
	}
	m := webBundleRE.FindSubmatch(page)
	if m == nil {
		return "", fmt.Errorf("%w: developer token: no script bundle at %s", ErrAPIChanged, c.WebBase)
	}
	bundle, err := c.get(ctx, c.WebBase+string(m[1]), nil)
	if err != nil {
		return "", fmt.Errorf("metadata: developer token: %w", err)
	}
	token = string(jwtRE.Find(bundle))
	if token == "" {
		return "", fmt.Errorf("%w: developer token: none in %s", ErrAPIChanged, m[1])
	}
	if exp, err = jwtExpiry(token); err != nil {
		return "", fmt.Errorf("%w: developer token: %v", ErrAPIChanged, err)
	}

	c.mu.Lock()
	c.token, c.tokenExp = token, exp
	c.mu.Unlock()
	return token, nil
}

// dropToken forgets the cached token, e.g. after the API rejected it.
func (c *Client) dropToken() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func jwtExpiry(token string) (time.Time, error) {
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("decode JWT: %w", err)
	}
	return time.Unix(claims.Exp, 0), nil
}
