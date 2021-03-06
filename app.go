package gear

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"reflect"
	"runtime"
	"strings"
)

// Handler interface is used by app.UseHandler as a middleware.
type Handler interface {
	Serve(*Context) error
}

// Renderer interface is used by ctx.Render.
type Renderer interface {
	Render(*Context, io.Writer, string, interface{}) error
}

// OnError interface is use to deal with ctx error.
type OnError interface {
	OnError(*Context, error) *Error
}

// DefaultOnError is default ctx error handler.
type DefaultOnError struct{}

// OnError implemented OnError interface.
func (o *DefaultOnError) OnError(ctx *Context, err error) *Error {
	var code int
	if ctx.Res.Status >= 400 {
		code = ctx.Res.Status
	}
	return ParseError(err, code)
}

// HTTPError interface is used to create a server error that include status code and error message.
type HTTPError interface {
	// Error returns error's message.
	Error() string

	// Status returns error's http status code.
	Status() int
}

// Error represents a numeric error with optional meta. It can be used in middleware as a return result.
type Error struct {
	Code int
	Msg  string
	Meta interface{}
}

// Status implemented HTTPError interface.
func (err *Error) Status() int {
	return err.Code
}

// Error implemented HTTPError interface.
func (err *Error) Error() string {
	return err.Msg
}

// String implemented fmt.Stringer interface.
func (err *Error) String() string {
	return fmt.Sprintf("{Code: %3d, Msg: %s, Meta: %s}", err.Code, err.Msg, err.Meta)
}

// Hook defines a function to process as hook.
type Hook func(*Context)

// Middleware defines a function to process as middleware.
type Middleware func(*Context) error

// NewAppError create a error instance with "[App] " prefix.
func NewAppError(err string) error {
	return fmt.Errorf("[App] %s", err)
}

// ParseError parse a error, textproto.Error or HTTPError to *Error
func ParseError(e error, code ...int) *Error {
	var err *Error
	if !isNil(e) {
		switch e.(type) {
		case *Error:
			err = e.(*Error)
		case *textproto.Error:
			_e := e.(*textproto.Error)
			err = &Error{_e.Code, _e.Msg, e}
		case HTTPError:
			_e := e.(HTTPError)
			err = &Error{_e.Status(), _e.Error(), e}
		default:
			err = &Error{500, e.Error(), e}
			if len(code) > 0 && code[0] > 0 {
				err.Code = code[0]
			}
		}
	}
	return err
}

// App is the top-level framework app instance.
//
// Hello Gear!
//
//  package main
//
//  import "github.com/teambition/gear"
//
//  func main() {
//  	app := gear.New() // Create app
//  	app.Use(func(ctx *gear.Context) error {
//  		return ctx.HTML(200, "<h1>Hello, Gear!</h1>")
//  	})
//  	app.Error(app.Listen(":3000"))
//  }
//
type App struct {
	middleware []Middleware
	settings   map[string]interface{}

	onerror  OnError
	renderer Renderer
	// Default to nil, do not compress response content.
	compress Compress

	// ErrorLog specifies an optional logger for app's errors. Default to nil.
	logger *log.Logger

	Server *http.Server
}

// New creates an instance of App.
func New() *App {
	app := new(App)
	app.Server = new(http.Server)
	app.middleware = make([]Middleware, 0)
	app.settings = make(map[string]interface{})

	app.Set("AppEnv", "development")
	app.Set("AppOnError", &DefaultOnError{})
	app.Set("AppLogger", log.New(os.Stderr, "", log.LstdFlags))

	return app
}

func (app *App) toServeHandler() *serveHandler {
	if len(app.middleware) == 0 {
		panic(NewAppError("no middleware"))
	}
	return &serveHandler{app, app.middleware[:]}
}

// Use uses the given middleware `handle`.
func (app *App) Use(handle Middleware) {
	app.middleware = append(app.middleware, handle)
}

// UseHandler uses a instance that implemented Handler interface.
func (app *App) UseHandler(h Handler) {
	app.middleware = append(app.middleware, h.Serve)
}

// Set add app settings. The settings can be retrieved by ctx.Setting.
// There are 4 build-in app settings:
//
//  app.Set("AppOnError", val gear.OnError)     // Default to gear.DefaultOnError
//  app.Set("AppRenderer", val gear.Renderer)   // No default
//  app.Set("AppLogger", val *log.Logger)       // No default
//  app.Set("AppCompress", val gear.Compress)   // Enable to compress response content.
//  app.Set("AppEnv", val string)               // Default to "development"
//
func (app *App) Set(setting string, val interface{}) {
	switch setting {
	case "AppOnError":
		if onerror, ok := val.(OnError); !ok {
			panic("AppOnError setting must implemented gear.OnError interface")
		} else {
			app.onerror = onerror
		}
	case "AppRenderer":
		if renderer, ok := val.(Renderer); !ok {
			panic("AppRenderer setting must implemented gear.Renderer interface")
		} else {
			app.renderer = renderer
		}
	case "AppLogger":
		if logger, ok := val.(*log.Logger); !ok {
			panic("AppLogger setting must be *log.Logger instance")
		} else {
			app.logger = logger
		}
	case "AppCompress":
		if compress, ok := val.(Compress); !ok {
			panic("AppCompress setting must implemented gear.Compress interface")
		} else {
			app.compress = compress
		}
	case "AppEnv":
		if _, ok := val.(string); !ok {
			panic("AppEnv setting must be string")
		}
	}
	app.settings[setting] = val
}

// Listen starts the HTTP server.
func (app *App) Listen(addr string) error {
	app.Server.Addr = addr
	app.Server.ErrorLog = app.logger
	app.Server.Handler = app.toServeHandler()
	return app.Server.ListenAndServe()
}

// ListenTLS starts the HTTPS server.
func (app *App) ListenTLS(addr, certFile, keyFile string) error {
	app.Server.Addr = addr
	app.Server.ErrorLog = app.logger
	app.Server.Handler = app.toServeHandler()
	return app.Server.ListenAndServeTLS(certFile, keyFile)
}

// Start starts a non-blocking app instance. It is useful for testing.
// If addr omit, the app will listen on a random addr, use ServerListener.Addr() to get it.
// The non-blocking app instance must close by ServerListener.Close().
func (app *App) Start(addr ...string) *ServerListener {
	laddr := "127.0.0.1:0"
	if len(addr) > 0 && addr[0] != "" {
		laddr = addr[0]
	}
	app.Server.ErrorLog = app.logger
	app.Server.Handler = app.toServeHandler()

	l, err := net.Listen("tcp", laddr)
	if err != nil {
		panic(NewAppError(fmt.Sprintf("failed to listen on %v: %v", laddr, err)))
	}

	c := make(chan error)
	go func() {
		c <- app.Server.Serve(l)
	}()
	return &ServerListener{l, c}
}

// Error writes error to underlayer logging system (ErrorLog).
func (app *App) Error(err error) {
	if err := ParseError(err); err != nil {
		app.logger.Println(err.String())
	}
}

type serveHandler struct {
	app        *App
	middleware []Middleware
}

func (h *serveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	ctx := NewContext(h.app, w, r)

	if compressWriter := ctx.handleCompress(); compressWriter != nil {
		defer compressWriter.Close()
	}

	// recover panic error
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			err := &Error{
				Code: 500,
				Msg:  fmt.Sprintf("panic recovered: %v", err),
				Meta: buf,
			}
			h.app.Error(err)
			ctx.Error(err)
		}
	}()

	go func() {
		<-ctx.Done()
		// cancel middleware process if request context canceled
		ctx.ended = true
	}()

	// handle "/abc//efg"
	if strings.Contains(ctx.Path, "//") {
		http.NotFound(ctx.Res, ctx.Req)
		return
	}

	// process app middleware
	for _, handle := range h.middleware {
		if err = handle(ctx); !isNil(err) {
			break
		}
		if ctx.ended {
			break // end up the middleware process
		}
	}

	// ensure that ended is true after middleware process finished.
	ctx.ended = true
	if !isNil(err) {
		ctx.afterHooks = nil // clear afterHooks when error
		// process middleware error with OnError
		if err := h.app.onerror.OnError(ctx, err); err != nil {
			ctx.Status(err.Status())
			// 5xx Server Error will send to app.Error
			if err.Code == 500 || err.Code > 501 || err.Code < 400 {
				h.app.Error(err)
			}
			ctx.String(err.Status(), err.Error())
		}
	}

	// ensure respond
	if err = ctx.Res.respond(); err != nil {
		panic(err)
	}
}

// ServerListener is returned by a non-blocking app instance.
type ServerListener struct {
	l net.Listener
	c <-chan error
}

// Close closes the non-blocking app instance.
func (s *ServerListener) Close() error {
	return s.l.Close()
}

// Addr returns the non-blocking app instance addr.
func (s *ServerListener) Addr() net.Addr {
	return s.l.Addr()
}

// Wait make the non-blocking app instance blocking.
func (s *ServerListener) Wait() error {
	return <-s.c
}

// WrapHandler wrap a http.Handler to Gear Middleware
func WrapHandler(handler http.Handler) Middleware {
	return func(ctx *Context) error {
		handler.ServeHTTP(ctx.Res, ctx.Req)
		return nil
	}
}

// WrapHandlerFunc wrap a http.HandlerFunc to Gear Middleware
func WrapHandlerFunc(fn http.HandlerFunc) Middleware {
	return func(ctx *Context) error {
		fn(ctx.Res, ctx.Req)
		return nil
	}
}

// isNil checks if a specified object is nil or not, without Failing.
func isNil(val interface{}) bool {
	if val == nil {
		return true
	}

	value := reflect.ValueOf(val)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Ptr, reflect.Interface, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
