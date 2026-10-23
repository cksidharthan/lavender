package logger

const (
	ErrorKey   = "error"
	PortKey    = "port"
	AddressKey = "address"
	ModuleKey  = "module"

	// trace correlation, added to every log record that is written inside a traced request
	TraceIDKey = "trace_id"
	SpanIDKey  = "span_id"

	// http request log keys
	MethodKey     = "method"
	PathKey       = "path"
	RouteKey      = "route"
	UserIDKey     = "user_id"
	RequestIDKey  = "request_id"
	StatusKey     = "status"
	BytesKey      = "bytes"
	DurationKey   = "duration"
	RemoteAddrKey = "remote_addr"
	UserAgentKey  = "user_agent"
)
