package api

import (
	"crypto/subtle"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/momaek/patato/server/internal/apierr"
)

var promHandler = promhttp.Handler()

func (s *Server) metrics(c *gin.Context) {
	want := "Bearer " + s.d.Cfg.MetricsToken
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte(want)) != 1 {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	promHandler.ServeHTTP(c.Writer, c.Request)
}
