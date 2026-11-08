package providerhttp

// A transport or response reader cannot establish this provider's Tool outcome.
// Its error remains diagnostic; only the current request context may establish
// cancellation independently of that cause.
type exchangeError struct{ cause error }

func (e *exchangeError) Error() string { return e.cause.Error() }

func (e *exchangeError) Cause() error { return e.cause }
