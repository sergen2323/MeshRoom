// Package ui — нативное окно приложения (WKWebView на macOS, WebView2 на
// Windows) через webview_go. Собирается только с cgo; для сборок без cgo
// есть запасной вариант в ui_stub.go (обычный браузер).

//go:build cgo

package ui

import (
	webview "github.com/webview/webview_go"
)

// Native — доступно ли нативное окно в этой сборке.
const Native = true

// Run открывает нативное окно с UI и блокируется до его закрытия.
// Должен вызываться из главной горутины процесса (требование macOS).
func Run(url, title string, width, height int) error {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(width, height, webview.HintNone)
	w.Navigate(url)
	w.Run()
	return nil
}
