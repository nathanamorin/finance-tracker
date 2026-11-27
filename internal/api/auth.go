package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// Identity is the authenticated SSO user.
type Identity struct {
	UserID string `json:"user_id"`
	Email  string `json:"email,omitempty"`
}

const identityKey = "identity"

// authMiddleware trusts the identity headers oauth2-proxy injects via
// Istio ext_authz once a request passes the Pocket ID SSO
// AuthorizationPolicy at the Gateway (see k8s-deployment
// deployments/networking/pocket-id-sso.yaml and the extensionProviders in
// deployments/platform/istio/02-istiod.yaml).
//
// Unlike local-podcasts, this fails closed: a missing header is a 401, not
// an "anonymous" user, because each user's database holds financial data.
// DevUser exists only for local development and must never be set in a
// deployment. The header is only trustworthy because the pod's
// NetworkPolicy admits ingress solely from the Istio Gateway; see
// deploy/README.md.
func authMiddleware(userHeader, emailHeader, devUser string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := Identity{
				UserID: strings.TrimSpace(c.Request().Header.Get(userHeader)),
				Email:  strings.TrimSpace(c.Request().Header.Get(emailHeader)),
			}
			if id.UserID == "" && devUser != "" {
				id = Identity{UserID: devUser, Email: devUser + "@localhost"}
			}
			if id.UserID == "" {
				return echo.NewHTTPError(http.StatusUnauthorized, "not authenticated: missing SSO identity header")
			}
			if len(id.UserID) > 256 {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid identity")
			}
			c.Set(identityKey, id)
			return next(c)
		}
	}
}

func identity(c echo.Context) Identity {
	id, _ := c.Get(identityKey).(Identity)
	return id
}
