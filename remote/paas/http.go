package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yinqiwen/gsnova/common/event"
	"github.com/yinqiwen/gsnova/remote"
)

func readRequestBuffer(r *http.Request) *bytes.Buffer {
	b := make([]byte, r.ContentLength)
	io.ReadFull(r.Body, b)
	r.Body.Close()
	reqbuf := bytes.NewBuffer(b)
	return reqbuf
}

// handleWebsocket connection. Update to
func httpInvoke(w http.ResponseWriter, r *http.Request) {
	ctx := remote.NewConnContext()
	writeEvents := func(evs []event.Event) error {
		if len(evs) > 0 {
			var buf bytes.Buffer
			for _, ev := range evs {
				if nil != ev {
					event.EncryptEvent(&buf, ev, ctx.IV)
				}
			}
			if buf.Len() > 0 {
				_, err := w.Write(buf.Bytes())
				if nil == err {
					w.(http.Flusher).Flush()
				}
				return err
			}
		}
		return nil
	}
	reqbuf := readRequestBuffer(r)

	ress, err := remote.HandleRequestBuffer(reqbuf, ctx)
	if nil != err {
		log.Printf("[ERROR]connection %s:%d error:%v with path:%s ", ctx.User, ctx.ConnIndex, err, r.URL.Path)
		w.WriteHeader(400)
	} else {
		w.WriteHeader(200)
		begin := time.Now()
		if strings.HasSuffix(r.URL.Path, "pull") {
			writeEvents(ress)
			queue := remote.GetEventQueue(ctx.ConnId, true)
			defer remote.ReleaseEventQueue(queue)
			for {
				if time.Now().After(begin.Add(10 * time.Second)) {
					log.Printf("Stop puller after 10s for conn:%d", ctx.ConnIndex)
					break
				}
				evs, err := queue.PeekMulti(2, 1*time.Millisecond)
				if nil != err {
					continue
				}
				err = writeEvents(evs)
				if nil != err {
					log.Printf("Websoket write error:%v", err)
					return
				} else {
					queue.DiscardPeeks()
				}
			}
			remote.ReleaseEventQueue(queue)
		}
	}
}
