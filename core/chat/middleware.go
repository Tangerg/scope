package chat

import "slices"

// CallMiddleware composes synchronous model behavior. Concrete policy belongs to upper modules.
type CallMiddleware func(next Model) Model

type StreamMiddleware func(next Streamer) Streamer

// Wrap composes call middlewares around model. The first middleware is the
// outermost wrapper. Nil entries are ignored so optional middleware can be
// supplied without a separate branch.
func Wrap(model Model, middlewares ...CallMiddleware) Model {
	return compose(model, middlewares)
}

// WrapStream composes stream middlewares around streamer using the same
// outermost-first order as Wrap.
func WrapStream(streamer Streamer, middlewares ...StreamMiddleware) Streamer {
	return compose(streamer, middlewares)
}

func compose[T any, M ~func(T) T](endpoint T, middlewares []M) T {
	wrapped := endpoint
	for _, middleware := range slices.Backward(middlewares) {
		if middleware != nil {
			wrapped = middleware(wrapped)
		}
	}
	return wrapped
}
