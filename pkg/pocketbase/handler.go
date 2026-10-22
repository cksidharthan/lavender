package pocketbase

import (
	"fmt"
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/ui"
)

// NewHandler builds the pocketbase HTTP handler (REST API, realtime and admin dashboard)
// so it can be served by the main http server instead of a dedicated pocketbase one.
//
// It mirrors apis.Serve minus the listener, the installer and the CORS/TLS setup. The OnServe
// hook is triggered here, which starts the pocketbase cron jobs, so call it even if the
// returned handler is not going to be served.
func NewHandler(app core.App) (http.Handler, error) {
	pbRouter, err := apis.NewRouter(app)
	if err != nil {
		return nil, fmt.Errorf("error creating pocketbase router: %w", err)
	}

	if ui.DistDirFS != nil {
		pbRouter.GET("/_/{path...}", apis.Static(ui.DistDirFS, false)).Bind(apis.Gzip())
	}

	var handler http.Handler

	serveEvent := new(core.ServeEvent)
	serveEvent.App = app
	serveEvent.Router = pbRouter
	serveEvent.Server = new(http.Server)

	err = app.OnServe().Trigger(serveEvent, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		if err != nil {
			return err
		}

		handler = mux

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error building pocketbase handler: %w", err)
	}

	return handler, nil
}
