package middlewares

import (
	"context"
	"log/slog"
	"net/http"

	lavenderLogger "github.com/cksidharthan/lavender/pkg/logger"
	lavenderOTEL "github.com/cksidharthan/lavender/pkg/otel"
	"github.com/pocketbase/pocketbase/core"
	"go.opentelemetry.io/otel/attribute"
)

type userContextKey struct{}

// Authenticated only lets requests through that carry a valid auth cookie. The user is available through
// UserFromContext. For htmx requests an unauthenticated user is sent to the login page, as htmx follows no 401.
func Authenticated(app core.App, cookieName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := userFromCookie(app, r, cookieName)
			if user == nil {
				lavenderOTEL.RecordUnauthorized(r.Context(), routeOf(r), isHTMX(r))

				if isHTMX(r) {
					w.Header().Set("HX-Redirect", "/login")
					w.WriteHeader(http.StatusOK)

					return
				}

				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)

				return
			}

			NoteUser(r.Context(), user.Id)

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
		})
	}
}

// UserFromContext returns the user set by Authenticated, nil when there is none.
func UserFromContext(ctx context.Context) *core.Record {
	user, _ := ctx.Value(userContextKey{}).(*core.Record)

	return user
}

func userFromCookie(app core.App, r *http.Request, cookieName string) *core.Record {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}

	user, err := app.FindAuthRecordByToken(cookie.Value, core.TokenTypeAuth)
	if err != nil {
		return nil
	}

	return user
}

// userHolder carries the id of the user of a request up to the request logger, which runs above the handlers.
type (
	userHolderKey struct{}
	userHolder    struct{ id string }
)

// NoteUser records who the request is of: on the trace, and for the request log.
func NoteUser(ctx context.Context, userID string) {
	lavenderOTEL.SetAttributes(ctx, attribute.String("enduser.id", userID))

	lavenderLogger.AddToContext(ctx, slog.String(lavenderLogger.UserIDKey, userID))

	if holder, ok := ctx.Value(userHolderKey{}).(*userHolder); ok {
		holder.id = userID
	}
}
