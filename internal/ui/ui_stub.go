//go:build !cgo

package ui

import "fmt"

// Native — доступно ли нативное окно в этой сборке.
const Native = false

// Run в сборке без cgo недоступен — вызывающий код откроет браузер.
func Run(url, title string, width, height int) error {
	return fmt.Errorf("native window not available in this build")
}
