//go:build !windows

package main

import "net/http"

func npipeTransport(string) http.RoundTripper { return nil }
