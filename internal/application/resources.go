package application

import "context"

// Resource is anything the application starts and has to shut down in order.
type Resource interface {
	Start() <-chan error
	Shutdown(ctx context.Context) error
}

type namedResource struct {
	name     string
	resource Resource
}

// resources lists what the application starts, in startup order. Shutdown walks
// this list backwards, so the order declared here is also the order in which
// things are taken down — which is why it lives in one readable place instead
// of spread across the wiring.
func (app *Application) resources() []namedResource {
	var list []namedResource

	// Ahead of the server, so the reload loop is only stopped once nothing is
	// left to answer a handshake.
	if app.certificateReloader != nil {
		list = append(list, namedResource{name: "certificate reload", resource: app.certificateReloader})
	}

	return append(list, namedResource{name: "http server", resource: app.httpServer})
}

func (app *Application) registerResource(entry namedResource) {
	app.graceful.
		Register(entry.name, entry.resource.Shutdown).
		Watch(entry.resource.Start())
}
