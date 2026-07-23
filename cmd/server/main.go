package main

import (
	"log"

	"github.com/labstack/echo/v4"

	"go-avatar-service/internal/api"
)

func main() {
	e := echo.New()

	server := api.NewAvatarServer()
	strictHandler := api.NewStrictHandler(server, nil)
	api.RegisterHandlers(e, strictHandler)

	log.Println("Starting server on :8080")
	if err := e.Start(":8080"); err != nil {
		log.Fatal(err)
	}
}
