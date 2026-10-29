package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"seven-air-link/internal/client"
	"seven-air-link/internal/model"
	"seven-air-link/internal/store"
)

func (a *application) file(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: file send PATH | list | get ID --out PATH | delete ID")
	}
	c, e := a.api()
	if e != nil {
		return e
	}
	defer c.Close()
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("file list takes no arguments")
		}
		var out any
		if e = c.Do(ctx, "GET", "/api/v1/files", nil, &out); e != nil {
			return e
		}
		return a.print(out)
	case "send":
		if len(args) < 2 {
			return errors.New("file path required")
		}
		f := a.flags("file send")
		to := f.String("to", "shared", "shared or principal ID")
		if e = parse(f, args[2:]); e != nil {
			return e
		}
		fh, e := os.Open(args[1])
		if e != nil {
			return e
		}
		defer fh.Close()
		info, e := fh.Stat()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > store.MaxFileBytes {
			return errors.New("regular file up to 200 MiB required")
		}
		q := url.Values{"name": {filepath.Base(args[1])}, "to": {*to}}
		request, e := http.NewRequestWithContext(ctx, "POST", c.Config.Server+"/api/v1/files?"+q.Encode(), fh)
		if e != nil {
			return e
		}
		request.ContentLength = info.Size()
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("Authorization", "Bearer "+c.Config.Token)
		c.HTTP.Timeout = 5 * time.Minute
		if tr, ok := c.HTTP.Transport.(*http.Transport); ok {
			tr.ResponseHeaderTimeout = 5 * time.Minute
		}
		resp, e := c.HTTP.Do(request)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 201 {
			return client.DecodeError(resp)
		}
		var out model.File
		if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); e != nil {
			return e
		}
		return a.print(out)
	case "get":
		if len(args) < 2 || !safeID(args[1]) {
			return errors.New("file ID required")
		}
		f := a.flags("file get")
		out := f.String("out", "", "new destination file; refuses overwrite")
		if e = parse(f, args[2:]); e != nil {
			return e
		}
		if e = absentFile(*out); e != nil {
			return e
		}
		var meta model.File
		if e = c.Do(ctx, "GET", "/api/v1/files/"+args[1]+"/meta", nil, &meta); e != nil {
			return e
		}
		// A temp file is never exposed as a complete download until hash verification.
		tmp, e := os.CreateTemp(filepath.Dir(*out), ".slb-download-*")
		if e != nil {
			return e
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		c.HTTP.Timeout = 5 * time.Minute
		resp, e := c.Request(ctx, "GET", "/api/v1/files/"+args[1], nil)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return client.DecodeError(resp)
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, store.MaxFileBytes+1))
		if e != nil {
			return e
		}
		if n != meta.Size || hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
			return errors.New("download checksum/size mismatch; temporary file removed")
		}
		if e = tmp.Sync(); e != nil {
			return e
		}
		if e = tmp.Close(); e != nil {
			return e
		}
		// Link is atomic and refuses an existing destination on supported platforms.
		// Fall back to exclusive creation (not rename) where hard links are unavailable.
		if e = os.Link(tmp.Name(), *out); e != nil {
			dest, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			source, err := os.Open(tmp.Name())
			if err != nil {
				dest.Close()
				os.Remove(*out)
				return err
			}
			_, err = io.Copy(dest, source)
			source.Close()
			if err == nil {
				err = dest.Sync()
			}
			ce := dest.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				os.Remove(*out)
				return err
			}
		}
		return a.print(map[string]any{"fileId": meta.ID, "path": *out, "size": n, "sha256": meta.SHA256, "verified": true})
	case "delete":
		if len(args) != 2 || !safeID(args[1]) {
			return errors.New("usage: file delete FILE_ID")
		}
		var out any
		if e = c.Do(ctx, "DELETE", "/api/v1/files/"+args[1], nil, &out); e != nil {
			return e
		}
		return a.print(out)
	}
	return fmt.Errorf("unknown file command %q", args[0])
}
