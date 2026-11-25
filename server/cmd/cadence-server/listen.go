package main

import "github.com/gofiber/fiber/v3"

func listenConfig() fiber.ListenConfig {
	return fiber.ListenConfig{DisableStartupMessage: true}
}
