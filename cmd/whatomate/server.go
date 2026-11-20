package main

import (
	"time"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/valyala/fasthttp"
)

func newHTTPServer(handler fasthttp.RequestHandler, cfg config.ServerConfig) *fasthttp.Server {
	return &fasthttp.Server{
		Handler: handler,
		// This buffer includes the request target. OIDC callbacks can exceed
		// fasthttp's 4 KiB default once the code, cookies and proxy headers combine.
		ReadBufferSize:     32 * 1024,
		ReadTimeout:        time.Duration(cfg.ReadTimeout) * time.Second,
		WriteTimeout:       time.Duration(cfg.WriteTimeout) * time.Second,
		MaxRequestBodySize: 15 * 1024 * 1024,
		Name:               "Whatomate",
	}
}
