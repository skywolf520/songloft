//go:build dev
// +build dev

package app

import (
	"fmt"
	_ "songloft/docs"

	httpSwagger "github.com/swaggo/http-swagger"
)

// registerSwagger 注册Swagger路由
func (a *App) registerSwagger() {
	a.router.Get("/swagger/*", httpSwagger.Handler(
		httpSwagger.URL(fmt.Sprintf("http://localhost:%s%s/swagger/doc.json", a.config.Port, a.config.BasePath)),
	))
}
