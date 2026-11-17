//go:build windows

package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// npipeTransport speaks HTTP to a Windows named pipe (Docker Desktop's
// \\.\pipe\docker_engine). os.OpenFile yields a synchronous handle on which a
// pending read blocks writes, so net/http's concurrent read/write loops would
// deadlock. This round-tripper is strictly sequential: write, then read.
func npipeTransport(path string) http.RoundTripper {
	return pipeRT{path}
}

type pipeRT struct{ path string }

func (p pipeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f, err := os.OpenFile(p.path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	closeF := func() { once.Do(func() { f.Close() }) }
	// Best-effort cancellation: close the handle when the request context ends.
	stop := make(chan struct{})
	go func() {
		select {
		case <-req.Context().Done():
			closeF()
		case <-stop:
		}
	}()
	req.Close = true
	if err := req.Write(f); err != nil {
		close(stop)
		closeF()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(f), req)
	if err != nil {
		close(stop)
		closeF()
		return nil, err
	}
	resp.Body = &pipeBody{ReadCloser: resp.Body, done: func() { close(stop); closeF() }}
	return resp, nil
}

type pipeBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *pipeBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}

var _ = time.Second
